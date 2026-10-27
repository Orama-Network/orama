package serverless

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Orama-Network/orama/core/pkg/ipfs"
	"github.com/Orama-Network/orama/core/pkg/rqlite"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// registryCacheTTL bounds how long function metadata + env vars are cached
// in-process before re-reading rqlite. Bugboard #708: every function_invoke
// previously did 3 uncached `weak` reads (Get, a redundant Get inside
// CanInvoke, and GetEnvVars), each forwarded to the raft leader — ~820ms of
// pure pre-flight tax per op when the leader is a distant node. With a short
// TTL + explicit invalidation on deploy/enable/disable/delete, a burst of RPCs
// (e.g. a call setup) reads metadata once instead of N times. The TTL is a
// backstop; correctness comes from the explicit invalidation, so cross-node
// propagation of a deploy/disable is bounded to this TTL.
const registryCacheTTL = 5 * time.Second

type fnCacheEntry struct {
	fn *Function
	at time.Time
}

type envCacheEntry struct {
	env map[string]string
	at  time.Time
}

// Ensure Registry implements FunctionRegistry and InvocationLogger interfaces.
var _ FunctionRegistry = (*Registry)(nil)
var _ InvocationLogger = (*Registry)(nil)

// Registry manages function metadata in RQLite and bytecode in IPFS.
// It implements the FunctionRegistry interface.
type Registry struct {
	db         rqlite.Client
	ipfs       ipfs.IPFSClient
	ipfsAPIURL string
	logger     *zap.Logger
	tableName  string

	// Metadata cache (bugboard #708) — see registryCacheTTL.
	cacheTTL time.Duration
	cacheMu  sync.RWMutex
	fnCache  map[string]fnCacheEntry  // key: namespace\x00name\x00version
	envCache map[string]envCacheEntry // key: functionID
}

// RegistryConfig holds configuration for the Registry.
type RegistryConfig struct {
	IPFSAPIURL string // IPFS API URL for content retrieval
}

// NewRegistry creates a new function registry.
func NewRegistry(db rqlite.Client, ipfsClient ipfs.IPFSClient, cfg RegistryConfig, logger *zap.Logger) *Registry {
	return &Registry{
		db:         db,
		ipfs:       ipfsClient,
		ipfsAPIURL: cfg.IPFSAPIURL,
		logger:     logger,
		tableName:  "functions",
		cacheTTL:   registryCacheTTL,
		fnCache:    make(map[string]fnCacheEntry),
		envCache:   make(map[string]envCacheEntry),
	}
}

// --- metadata cache (bugboard #708) ---
//
// The cached *Function and env map are SHARED with all callers and MUST be
// treated as read-only — no consumer in pkg/serverless mutates them today, and
// none may, or it would corrupt the cache for concurrent readers.

func fnCacheKey(namespace, name string, version int) string {
	return namespace + "\x00" + name + "\x00" + strconv.Itoa(version)
}

func (r *Registry) cachedFn(key string) (*Function, bool) {
	r.cacheMu.RLock()
	e, ok := r.fnCache[key]
	r.cacheMu.RUnlock()
	if !ok || time.Since(e.at) > r.cacheTTL {
		return nil, false
	}
	return e.fn, true
}

func (r *Registry) storeFn(key string, fn *Function) {
	r.cacheMu.Lock()
	r.fnCache[key] = fnCacheEntry{fn: fn, at: time.Now()}
	r.cacheMu.Unlock()
}

func (r *Registry) cachedEnv(functionID string) (map[string]string, bool) {
	r.cacheMu.RLock()
	e, ok := r.envCache[functionID]
	r.cacheMu.RUnlock()
	if !ok || time.Since(e.at) > r.cacheTTL {
		return nil, false
	}
	return e.env, true
}

func (r *Registry) storeEnv(functionID string, env map[string]string) {
	r.cacheMu.Lock()
	r.envCache[functionID] = envCacheEntry{env: env, at: time.Now()}
	r.cacheMu.Unlock()
}

// invalidateFn drops every cached version of (namespace, name). Called on
// deploy/enable/disable/delete so a metadata change is never masked by the
// cache beyond the write itself.
func (r *Registry) invalidateFn(namespace, name string) {
	prefix := strings.TrimSpace(namespace) + "\x00" + strings.TrimSpace(name) + "\x00"
	r.cacheMu.Lock()
	for k := range r.fnCache {
		if strings.HasPrefix(k, prefix) {
			delete(r.fnCache, k)
		}
	}
	r.cacheMu.Unlock()
}

// invalidateEnv drops the cached env vars for a function ID, so an env-var
// rewrite is never masked by the cache for up to the TTL.
func (r *Registry) invalidateEnv(functionID string) {
	r.cacheMu.Lock()
	delete(r.envCache, functionID)
	r.cacheMu.Unlock()
}

// Register deploys a function as a new version: the first deploy is version 1,
// every later one inserts the next version and keeps the rows before it (up to
// MaxRetainedFunctionVersions). It returns the version it superseded, or nil.
func (r *Registry) Register(ctx context.Context, fn *FunctionDefinition, wasmBytes []byte) (*Function, error) {
	if fn == nil {
		return nil, &ValidationError{Field: "definition", Message: "cannot be nil"}
	}
	fn.Name = strings.TrimSpace(fn.Name)
	fn.Namespace = strings.TrimSpace(fn.Namespace)

	if fn.Name == "" {
		return nil, &ValidationError{Field: "name", Message: "cannot be empty"}
	}
	if fn.Namespace == "" {
		return nil, &ValidationError{Field: "namespace", Message: "cannot be empty"}
	}
	if len(wasmBytes) == 0 {
		return nil, &ValidationError{Field: "wasmBytes", Message: "cannot be empty"}
	}
	if err := ValidateWSAuth(fn.WSAuth); err != nil {
		return nil, err
	}
	if fn.WSAuth == WSAuthCapability && fn.IsInternal {
		return nil, &ValidationError{Field: "ws_auth", Message: "an internal function is never opened on a capability"}
	}

	// Check if function already exists (regardless of status) to get old metadata for invalidation
	oldFn, err := r.getByNameInternal(ctx, fn.Namespace, fn.Name)
	if err != nil && err != ErrFunctionNotFound {
		return nil, &DeployError{FunctionName: fn.Name, Cause: err}
	}

	// The digest is of the bytes as received: the upload seals them, so the
	// CID cannot name the code (migration 093).
	wasmSHA256 := wasmDigest(wasmBytes)
	wasmCID, err := r.uploadWASM(ctx, wasmBytes, fn.Name)
	if err != nil {
		return nil, &DeployError{FunctionName: fn.Name, Cause: err}
	}

	// Apply defaults
	memoryLimit := fn.MemoryLimitMB
	if memoryLimit == 0 {
		memoryLimit = 64
	}
	timeout := fn.TimeoutSeconds
	if timeout == 0 {
		timeout = 30
	}
	retryDelay := fn.RetryDelaySeconds
	if retryDelay == 0 {
		retryDelay = 5
	}

	now := time.Now()
	// Every deploy is a new row with its own id: the previous versions stay
	// (migration 068), so `name@N` keeps resolving after `name@N+1` ships.
	id := uuid.New().String()
	version := 1
	if oldFn != nil {
		version = oldFn.Version + 1
	}

	query := `
		INSERT INTO functions (
			id, name, namespace, version, wasm_cid, wasm_sha256,
			memory_limit_mb, timeout_seconds, is_public, is_internal,
			retry_count, retry_delay_seconds, dlq_topic,
			status, created_at, updated_at, created_by,
			ws_persistent, ws_idle_timeout_sec, ws_max_frame_bytes, ws_max_inflight_per_conn,
			raw_http_response, ws_auth,
			rate_limit_caller_requests, rate_limit_caller_window_sec,
			rate_limit_ip_requests, rate_limit_ip_window_sec
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	callerRequests, callerWindow, ipRequests, ipWindow := fn.RateLimit.columns()
	_, err = r.db.Exec(ctx, query,
		id, fn.Name, fn.Namespace, version, wasmCID, wasmSHA256,
		memoryLimit, timeout, fn.IsPublic, fn.IsInternal,
		fn.RetryCount, retryDelay, fn.DLQTopic,
		string(FunctionStatusActive), now, now, fn.Namespace,
		fn.WSPersistent, fn.WSIdleTimeoutSec, fn.WSMaxFrameBytes, fn.WSMaxInflightPerConn,
		fn.RawHTTPResponse, fn.WSAuth,
		callerRequests, callerWindow, ipRequests, ipWindow,
	)
	if err != nil {
		return nil, &DeployError{FunctionName: fn.Name, Cause: fmt.Errorf("failed to register function: %w", err)}
	}

	// Save environment variables
	if err := r.saveEnvVars(ctx, id, fn.EnvVars); err != nil {
		return nil, &DeployError{FunctionName: fn.Name, Cause: err}
	}

	if err := r.adoptVersionState(ctx, fn.Namespace, fn.Name, id); err != nil {
		return nil, &DeployError{FunctionName: fn.Name, Cause: err}
	}

	r.invalidateFn(fn.Namespace, fn.Name)
	r.invalidateEnv(id)

	r.logger.Info("Function registered",
		zap.String("id", id),
		zap.String("name", fn.Name),
		zap.String("namespace", fn.Namespace),
		zap.String("wasm_cid", wasmCID),
		zap.String("wasm_sha256", wasmSHA256),
		zap.Int("version", version),
		zap.Bool("updated", oldFn != nil),
	)

	return oldFn, nil
}

// Get retrieves a function by name and optional version.
// If version is 0, returns the current version: the highest active one. A
// version number resolves only when it is that current version; a version a
// deploy or a rollback replaced is ErrVersionSuperseded, and a missing,
// disabled or deleted one ErrVersionNotFound. Every invocation path (HTTP,
// /v1/invoke, the WebSocket, triggers, function_invoke) resolves through here.
func (r *Registry) Get(ctx context.Context, namespace, name string, version int) (*Function, error) {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)

	// Cache hit (bugboard #708): skip the leader-routed weak read entirely.
	cacheKey := fnCacheKey(namespace, name, version)
	if fn, ok := r.cachedFn(cacheKey); ok {
		return fn, nil
	}

	var query string
	var args []interface{}

	if version == 0 {
		// Get latest version
		query = `
			SELECT id, name, namespace, version, wasm_cid, wasm_sha256, source_cid,
				memory_limit_mb, timeout_seconds, is_public, is_internal,
				retry_count, retry_delay_seconds, dlq_topic,
				status, created_at, updated_at, created_by,
				ws_persistent, ws_idle_timeout_sec, ws_max_frame_bytes, ws_max_inflight_per_conn,
			raw_http_response, ws_auth,
			rate_limit_caller_requests, rate_limit_caller_window_sec,
			rate_limit_ip_requests, rate_limit_ip_window_sec
			FROM functions
			WHERE namespace = ? AND name = ? AND status = ?
			ORDER BY version DESC
			LIMIT 1
		`
		args = []interface{}{namespace, name, string(FunctionStatusActive)}
	} else {
		// The row of that version whatever its status, so a replaced version
		// can say so. An active row with a higher active row beside it is
		// superseded too: a gateway older than the superseded status deploys
		// without marking the version it replaced.
		query = `
			SELECT id, name, namespace, version, wasm_cid, wasm_sha256, source_cid,
				memory_limit_mb, timeout_seconds, is_public, is_internal,
				retry_count, retry_delay_seconds, dlq_topic,
				CASE WHEN status = ? AND EXISTS (
					SELECT 1 FROM functions newer
					WHERE newer.namespace = functions.namespace AND newer.name = functions.name
					  AND newer.status = ? AND newer.version > functions.version
				) THEN ? ELSE status END AS status,
				created_at, updated_at, created_by,
				ws_persistent, ws_idle_timeout_sec, ws_max_frame_bytes, ws_max_inflight_per_conn,
			raw_http_response, ws_auth,
			rate_limit_caller_requests, rate_limit_caller_window_sec,
			rate_limit_ip_requests, rate_limit_ip_window_sec
			FROM functions
			WHERE namespace = ? AND name = ? AND version = ?
		`
		args = []interface{}{
			string(FunctionStatusActive), string(FunctionStatusActive), string(FunctionStatusSuperseded),
			namespace, name, version,
		}
	}

	var functions []functionRow
	if err := r.db.Query(ctx, &functions, query, args...); err != nil {
		return nil, fmt.Errorf("failed to query function: %w", err)
	}

	// Misses are NOT cached — a just-deployed function must be visible
	// immediately on the next call, not after the TTL.
	if len(functions) == 0 {
		if version == 0 {
			return nil, ErrFunctionNotFound
		}
		return nil, ErrVersionNotFound
	}
	switch FunctionStatus(functions[0].Status) {
	case FunctionStatusActive:
	case FunctionStatusSuperseded:
		return nil, ErrVersionSuperseded
	default:
		return nil, ErrVersionNotFound
	}

	fn := r.rowToFunction(&functions[0])
	r.storeFn(cacheKey, fn)
	return fn, nil
}

// List returns all functions for a namespace.
func (r *Registry) List(ctx context.Context, namespace string) ([]*Function, error) {
	// Get latest version of each function in the namespace
	query := `
		SELECT f.id, f.name, f.namespace, f.version, f.wasm_cid, f.wasm_sha256, f.source_cid,
			f.memory_limit_mb, f.timeout_seconds, f.is_public, f.is_internal,
			f.retry_count, f.retry_delay_seconds, f.dlq_topic,
			f.status, f.created_at, f.updated_at, f.created_by,
			f.ws_persistent, f.ws_idle_timeout_sec, f.ws_max_frame_bytes, f.ws_max_inflight_per_conn,
			f.raw_http_response, f.ws_auth,
			f.rate_limit_caller_requests, f.rate_limit_caller_window_sec,
			f.rate_limit_ip_requests, f.rate_limit_ip_window_sec
		FROM functions f
		INNER JOIN (
			SELECT namespace, name, MAX(version) as max_version
			FROM functions
			WHERE namespace = ? AND status = ?
			GROUP BY namespace, name
		) latest ON f.namespace = latest.namespace 
			AND f.name = latest.name 
			AND f.version = latest.max_version
		ORDER BY f.name
	`

	var rows []functionRow
	if err := r.db.Query(ctx, &rows, query, namespace, string(FunctionStatusActive)); err != nil {
		return nil, fmt.Errorf("failed to list functions: %w", err)
	}

	functions := make([]*Function, len(rows))
	for i, row := range rows {
		functions[i] = r.rowToFunction(&row)
	}

	return functions, nil
}

// SetEnabled flips a function's current version between active and inactive
// without redeploying (plan 11.5 disable/enable), so a disable pauses the
// whole function: the current version is the only one that runs. Superseded
// and deleted versions are left as they are, so enabling never revives code
// the owner replaced or deleted. Returns ErrFunctionNotFound when the function
// has no current version.
func (r *Registry) SetEnabled(ctx context.Context, namespace, name string, enabled bool) error {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "" || name == "" {
		return fmt.Errorf("namespace and name required")
	}
	status := FunctionStatusInactive
	if enabled {
		status = FunctionStatusActive
	}
	query := `UPDATE functions SET status = ?, updated_at = ? WHERE namespace = ? AND name = ? AND status IN (?, ?)`
	result, err := r.db.Exec(ctx, query, string(status), time.Now(), namespace, name,
		string(FunctionStatusActive), string(FunctionStatusInactive))
	if err != nil {
		return fmt.Errorf("failed to set function enabled state: %w", err)
	}
	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return ErrFunctionNotFound
	}
	r.invalidateFn(namespace, name)
	r.logger.Info("Function enabled-state updated",
		zap.String("namespace", namespace),
		zap.String("name", name),
		zap.String("status", string(status)),
	)
	return nil
}

// Delete marks a function deleted. If version is 0, marks all versions. The
// rows stay, so `list --all` still shows them; Purge removes them.
func (r *Registry) Delete(ctx context.Context, namespace, name string, version int) error {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)

	var query string
	var args []interface{}

	if version == 0 {
		// Mark all versions as deleted (soft delete)
		query = `UPDATE functions SET status = ?, updated_at = ? WHERE namespace = ? AND name = ?`
		args = []interface{}{string(FunctionStatusDeleted), time.Now(), namespace, name}
	} else {
		query = `UPDATE functions SET status = ?, updated_at = ? WHERE namespace = ? AND name = ? AND version = ?`
		args = []interface{}{string(FunctionStatusDeleted), time.Now(), namespace, name, version}
	}

	result, err := r.db.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to delete function: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		if version == 0 {
			return ErrFunctionNotFound
		}
		return ErrVersionNotFound
	}

	r.invalidateFn(namespace, name)

	r.logger.Info("Function deleted",
		zap.String("namespace", namespace),
		zap.String("name", name),
		zap.Int("version", version),
	)

	return nil
}

// WASM-fetch tuning. The IPFS `cat` must NOT consume the function's whole
// execution budget on a single stalled request (bugboard #137: a 15s function
// spent all 15s on one cold fetch). Each attempt gets its own short deadline and
// we retry a few times so bitswap can resolve a not-yet-local block from a pin
// holder (the warm path is ~450ms, so 3×4s is ample headroom).
const (
	wasmFetchTimeout     = 4 * time.Second
	wasmFetchMaxAttempts = 3
	wasmFetchRetryBase   = 250 * time.Millisecond
)

// Pin-verification tuning. IPFS-Cluster's POST /pins returns 200/202 IMMEDIATELY
// and pins ASYNC per-peer, so a deploy that only fires-and-forgets the pin can
// report success while the block is still "pinning" (or never converges) on the
// peers — exactly the bugboard #137 failure (a node cold-fetches a block no peer
// actually holds and times out on invoke). After pinning we poll PinStatus until
// every peer reports "pinned" (or we time out and hard-fail the deploy).
// These are vars (not consts) only so tests can shorten them to keep the
// pin-verification timeout path fast; production never mutates them.
var (
	wasmPinVerifyTimeout  = 30 * time.Second
	wasmPinVerifyInterval = 2 * time.Second
	// wasmRecoveryWait bounds the re-pin-and-wait recovery on a cold read miss:
	// re-issuing the pin makes the local cluster peer re-fetch the block from a
	// peer that has it, then we poll (capped by this) for local availability
	// before one final fetch attempt.
	wasmRecoveryWait = 10 * time.Second
)

// GetWASMBytes retrieves the compiled WASM bytecode for a function. On a cold
// block it retries with a bounded per-attempt deadline and returns the typed
// ErrWASMFetchTimeout (retryable infra failure) so callers don't misreport a
// transient IPFS stall as a permanent function error. WASM the cluster's
// pinset does not list is ErrWASMNotStored at once.
func (r *Registry) GetWASMBytes(ctx context.Context, wasmCID string) ([]byte, error) {
	if wasmCID == "" {
		return nil, &ValidationError{Field: "wasmCID", Message: "cannot be empty"}
	}

	var lastErr error
	for attempt := 1; attempt <= wasmFetchMaxAttempts; attempt++ {
		data, err := r.fetchWASMOnce(ctx, wasmCID)
		if err == nil {
			return data, nil
		}
		// A CID the cluster's pinset does not list is stored nowhere it knows
		// of: no retry, re-pin or wait can bring it back, and each would only
		// spend the invocation's time (bugboard #432).
		if errors.Is(err, ipfs.ErrNotInPinset) {
			return nil, fmt.Errorf("%w: cid=%s: %v", ErrWASMNotStored, wasmCID, err)
		}
		lastErr = err

		// Respect caller cancellation / overall deadline — don't keep retrying
		// into an already-dead context.
		if ctx.Err() != nil {
			break
		}
		if attempt < wasmFetchMaxAttempts {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("%w: cid=%s: %v", ErrWASMFetchTimeout, wasmCID, ctx.Err())
			case <-time.After(time.Duration(attempt) * wasmFetchRetryBase):
			}
		}
	}

	// Every local attempt timed out: the block isn't on this node's daemon. The
	// local cluster peer may simply never have fetched it — re-pinning triggers
	// it to pull the block from a peer that has it, after which one more fetch
	// can succeed (bugboard #137). If the block is gone everywhere, this can't
	// recover it and we still fail (correct). Skip recovery if the caller's
	// context is already dead — a cancelled request must not trigger a re-pin it
	// cannot await.
	if ctx.Err() == nil {
		if data, err := r.recoverWASMByRepin(ctx, wasmCID); err == nil {
			return data, nil
		}
	}

	return nil, fmt.Errorf("%w: cid=%s after %d attempts: %v", ErrWASMFetchTimeout, wasmCID, wasmFetchMaxAttempts, lastErr)
}

// recoverWASMByRepin performs one bounded recovery for a cold read miss: re-pin
// the CID everywhere (so the local cluster peer re-fetches it from a peer that
// holds it), poll PinStatus until it is locally available (capped by
// wasmRecoveryWait), then attempt a single final fetch. Returns the bytes on
// success or an error the caller treats as "recovery failed".
func (r *Registry) recoverWASMByRepin(ctx context.Context, wasmCID string) ([]byte, error) {
	if _, err := r.ipfs.Pin(ctx, wasmCID, wasmCID+".wasm", wasmReplicationEverywhere); err != nil {
		return nil, fmt.Errorf("re-pin recovery for cid=%s failed: %w", wasmCID, err)
	}

	r.waitForLocalWASMBlock(ctx, wasmCID)

	data, err := r.fetchWASMOnce(ctx, wasmCID)
	if err != nil {
		return nil, fmt.Errorf("post-repin fetch for cid=%s failed: %w", wasmCID, err)
	}
	return data, nil
}

// waitForLocalWASMBlock polls PinStatus (capped by wasmRecoveryWait) until at
// least one peer reports the block pinned, giving the local cluster peer time to
// re-fetch it after a recovery re-pin. It returns when the block appears, the
// wait budget is spent, or ctx is cancelled — the caller still attempts a final
// fetch regardless, since the block may be local before the status converges.
func (r *Registry) waitForLocalWASMBlock(ctx context.Context, wasmCID string) {
	waitCtx, cancel := context.WithTimeout(ctx, wasmRecoveryWait)
	defer cancel()
	ticker := time.NewTicker(wasmFetchRetryBase)
	defer ticker.Stop()
	for {
		if status, err := r.ipfs.PinStatus(waitCtx, wasmCID); err == nil && status.PinnedPeers > 0 {
			return
		}
		select {
		case <-waitCtx.Done():
			return
		case <-ticker.C:
		}
	}
}

// fetchWASMOnce does a single IPFS fetch bounded by an independent deadline that
// is capped at the smaller of wasmFetchTimeout and whatever the caller ctx has
// left.
func (r *Registry) fetchWASMOnce(ctx context.Context, wasmCID string) ([]byte, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, wasmFetchTimeout)
	defer cancel()

	content, err := r.ipfs.GetStored(fetchCtx, wasmCID, r.ipfsAPIURL, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to get WASM from IPFS: %w", err)
	}
	defer content.Body.Close()

	data, err := io.ReadAll(content.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read WASM data: %w", err)
	}

	return data, nil
}

// RepinAllWASM re-pins the WASM of every version that can run again — the
// current one, a disabled one, and a superseded one a rollback can make
// current — on the cluster (replication=-1) so functions deployed before the
// pin-everywhere fix become durably pinned + GC-safe. Best-effort +
// idempotent; returns the count pinned.
//
// Incident 2026-06-24: scheduled IPFS GC reclaimed unpinned function WASM,
// deleting the bytecode of functions that were only ever pinned RF=3 (or whose
// pin silently failed). Run on gateway startup, this backfill guarantees all
// existing function WASM is pinned everywhere so GC can never delete it. New
// deploys are already covered by uploadWASM's pin-everywhere + hard-fail.
func (r *Registry) RepinAllWASM(ctx context.Context) (int, error) {
	var rows []wasmCIDRow
	query := `SELECT DISTINCT wasm_cid FROM functions WHERE status IN (?, ?, ?) AND wasm_cid IS NOT NULL AND wasm_cid != ''`
	if err := r.db.Query(ctx, &rows, query,
		string(FunctionStatusActive), string(FunctionStatusInactive), string(FunctionStatusSuperseded)); err != nil {
		return 0, fmt.Errorf("failed to list function WASM CIDs: %w", err)
	}

	cids := make([]string, 0, len(rows))
	for _, row := range rows {
		cids = append(cids, row.WASMCID)
	}
	return r.repinWASMCIDs(ctx, cids), nil
}

// wasmCIDRow is the scan target for RepinAllWASM's DISTINCT wasm_cid query.
type wasmCIDRow struct {
	WASMCID string `db:"wasm_cid"`
}

// repinWASMCIDs pins each CID on every cluster peer (replication=-1),
// best-effort: a CID whose block is gone (GC-deleted) can't be pinned, so it is
// logged and skipped while the surviving ones are still protected. Returns the
// count successfully pinned. Split out so the pin loop is unit-testable.
func (r *Registry) repinWASMCIDs(ctx context.Context, cids []string) int {
	pinned := 0
	for _, cid := range cids {
		if ctx.Err() != nil {
			return pinned
		}
		if _, err := r.ipfs.Pin(ctx, cid, cid+".wasm", wasmReplicationEverywhere); err != nil {
			r.logger.Warn("repin function WASM failed (block may be gone — needs re-deploy)",
				zap.String("cid", cid), zap.Error(err))
			continue
		}
		// Only count a CID as pinned once the async per-peer pin actually
		// converges — an unverified pin is what the #137 bug relied on.
		if err := r.verifyPinnedEverywhere(ctx, cid); err != nil {
			r.logger.Warn("repin function WASM not confirmed on all peers (block may be gone / not converged)",
				zap.String("cid", cid), zap.Error(err))
			continue
		}
		pinned++
	}
	return pinned
}

// GetEnvVars retrieves environment variables for a function.
func (r *Registry) GetEnvVars(ctx context.Context, functionID string) (map[string]string, error) {
	if env, ok := r.cachedEnv(functionID); ok {
		return env, nil
	}

	query := `SELECT key, value FROM function_env_vars WHERE function_id = ?`

	var rows []envVarRow
	if err := r.db.Query(ctx, &rows, query, functionID); err != nil {
		return nil, fmt.Errorf("failed to query env vars: %w", err)
	}

	envVars := make(map[string]string, len(rows))
	for _, row := range rows {
		envVars[row.Key] = row.Value
	}

	r.storeEnv(functionID, envVars)
	return envVars, nil
}

// GetByID retrieves a function by its ID.
func (r *Registry) GetByID(ctx context.Context, id string) (*Function, error) {
	query := `
		SELECT id, name, namespace, version, wasm_cid, wasm_sha256, source_cid,
			memory_limit_mb, timeout_seconds, is_public, is_internal,
			retry_count, retry_delay_seconds, dlq_topic,
			status, created_at, updated_at, created_by,
			ws_persistent, ws_idle_timeout_sec, ws_max_frame_bytes, ws_max_inflight_per_conn,
			raw_http_response, ws_auth,
			rate_limit_caller_requests, rate_limit_caller_window_sec,
			rate_limit_ip_requests, rate_limit_ip_window_sec
		FROM functions
		WHERE id = ?
	`

	var functions []functionRow
	if err := r.db.Query(ctx, &functions, query, id); err != nil {
		return nil, fmt.Errorf("failed to query function: %w", err)
	}

	if len(functions) == 0 {
		return nil, ErrFunctionNotFound
	}

	return r.rowToFunction(&functions[0]), nil
}

// ListVersions returns all versions of a function.
func (r *Registry) ListVersions(ctx context.Context, namespace, name string) ([]*Function, error) {
	query := `
		SELECT id, name, namespace, version, wasm_cid, wasm_sha256, source_cid,
			memory_limit_mb, timeout_seconds, is_public, is_internal,
			retry_count, retry_delay_seconds, dlq_topic,
			status, created_at, updated_at, created_by,
			ws_persistent, ws_idle_timeout_sec, ws_max_frame_bytes, ws_max_inflight_per_conn,
			raw_http_response, ws_auth,
			rate_limit_caller_requests, rate_limit_caller_window_sec,
			rate_limit_ip_requests, rate_limit_ip_window_sec
		FROM functions
		WHERE namespace = ? AND name = ?
		ORDER BY version DESC
	`

	var rows []functionRow
	if err := r.db.Query(ctx, &rows, query, namespace, name); err != nil {
		return nil, fmt.Errorf("failed to list versions: %w", err)
	}

	functions := make([]*Function, len(rows))
	for i, row := range rows {
		functions[i] = r.rowToFunction(&row)
	}

	return functions, nil
}

// ListAll returns every version row the namespace holds, whatever its status,
// by name and then newest version first: the owner's view of what it has
// deployed, including what `list` hides (disabled, superseded and deleted
// versions) until it is purged. A namespace holds at most
// MaxRetainedFunctionVersions rows per function.
func (r *Registry) ListAll(ctx context.Context, namespace string) ([]*Function, error) {
	query := `
		SELECT id, name, namespace, version, wasm_cid, wasm_sha256, source_cid,
			memory_limit_mb, timeout_seconds, is_public, is_internal,
			retry_count, retry_delay_seconds, dlq_topic,
			status, created_at, updated_at, created_by,
			ws_persistent, ws_idle_timeout_sec, ws_max_frame_bytes, ws_max_inflight_per_conn,
			raw_http_response, ws_auth,
			rate_limit_caller_requests, rate_limit_caller_window_sec,
			rate_limit_ip_requests, rate_limit_ip_window_sec
		FROM functions
		WHERE namespace = ?
		ORDER BY name, version DESC
	`

	var rows []functionRow
	if err := r.db.Query(ctx, &rows, query, strings.TrimSpace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list every function version: %w", err)
	}

	functions := make([]*Function, len(rows))
	for i, row := range rows {
		functions[i] = r.rowToFunction(&row)
	}
	return functions, nil
}

// Log records a function invocation and its logs to the database.
func (r *Registry) Log(ctx context.Context, inv *InvocationRecord) error {
	if inv == nil {
		return nil
	}

	// Insert invocation record
	invQuery := `
		INSERT INTO function_invocations (
			id, function_id, request_id, trigger_type, caller_wallet,
			input_size, output_size, started_at, completed_at,
			duration_ms, status, error_message, memory_used_mb,
			node_id, queue_ms, load_ms, db_calls, db_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.Exec(ctx, invQuery,
		inv.ID, inv.FunctionID, inv.RequestID, string(inv.TriggerType), inv.CallerWallet,
		inv.InputSize, inv.OutputSize, inv.StartedAt, inv.CompletedAt,
		inv.DurationMS, string(inv.Status), inv.ErrorMessage, inv.MemoryUsedMB,
		inv.NodeID, inv.QueueMS, inv.LoadMS, inv.DBCalls, inv.DBMS,
	)
	if err != nil {
		return fmt.Errorf("failed to insert invocation record: %w", err)
	}

	// Insert logs in batched multi-row INSERTs rather than one Exec per line.
	// Pre-fix this loop paid one cross-region Raft write PER log line (N+1):
	// a handler emitting 5 lines cost 6 sequential writes. Now a record's
	// lines collapse into ceil(N/maxLogRowsPerInsert) writes (bugboard feat-27).
	for _, chunk := range chunkLogEntries(inv.Logs, maxLogRowsPerInsert) {
		query, args := buildFunctionLogsInsert(inv.FunctionID, inv.ID, chunk)
		if _, err := r.db.Exec(ctx, query, args...); err != nil {
			r.logger.Warn("Failed to insert function logs batch", zap.Error(err))
			// Continue with remaining chunks — telemetry is best-effort.
		}
	}

	return nil
}

// maxLogRowsPerInsert caps how many function_logs rows go into a single
// multi-row INSERT statement. Keeps any one statement bounded (placeholder
// count, statement size) while still collapsing the per-line N+1 into a
// handful of writes for the common case.
const maxLogRowsPerInsert = 100

// chunkLogEntries splits entries into slices of at most size. Returns no
// chunks for an empty input.
func chunkLogEntries(entries []LogEntry, size int) [][]LogEntry {
	if len(entries) == 0 {
		return nil
	}
	var chunks [][]LogEntry
	for i := 0; i < len(entries); i += size {
		end := i + size
		if end > len(entries) {
			end = len(entries)
		}
		chunks = append(chunks, entries[i:end])
	}
	return chunks
}

// buildFunctionLogsInsert constructs a single multi-row INSERT for the given
// log entries: one VALUES tuple per entry, args flattened in column order
// (id, function_id, invocation_id, level, message, timestamp). Each row gets a
// fresh UUID id, matching the per-row behavior of the old loop.
func buildFunctionLogsInsert(functionID, invocationID string, entries []LogEntry) (string, []interface{}) {
	var sb strings.Builder
	sb.WriteString("INSERT INTO function_logs (id, function_id, invocation_id, level, message, timestamp) VALUES ")
	args := make([]interface{}, 0, len(entries)*6)
	for i, entry := range entries {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("(?, ?, ?, ?, ?, ?)")
		args = append(args, uuid.New().String(), functionID, invocationID, entry.Level, entry.Message, entry.Timestamp)
	}
	return sb.String(), args
}

// GetLogs retrieves logs for a function.
func (r *Registry) GetLogs(ctx context.Context, namespace, name string, limit int) ([]LogEntry, error) {
	if limit <= 0 {
		limit = 100
	}

	query := `
		SELECT l.level, l.message, l.timestamp
		FROM function_logs l
		JOIN functions f ON l.function_id = f.id
		WHERE f.namespace = ? AND f.name = ?
		ORDER BY l.timestamp DESC
		LIMIT ?
	`

	var results []struct {
		Level     string    `db:"level"`
		Message   string    `db:"message"`
		Timestamp time.Time `db:"timestamp"`
	}

	if err := r.db.Query(ctx, &results, query, namespace, name, limit); err != nil {
		return nil, fmt.Errorf("failed to query logs: %w", err)
	}

	logs := make([]LogEntry, len(results))
	for i, res := range results {
		logs[i] = LogEntry{
			Level:     res.Level,
			Message:   res.Message,
			Timestamp: res.Timestamp,
		}
	}

	return logs, nil
}

// GetInvocations returns invocation history for a function in reverse
// chronological order, with any associated WASM log entries nested per
// record. This is the right answer to "what happened when this function
// was invoked" — `orama function logs <name>` consumes this view.
//
// Always populated when the function has been invoked at least once.
// Two queries (invocations + nested WASM logs) batched into one map.
func (r *Registry) GetInvocations(ctx context.Context, namespace, name string, limit int) ([]Invocation, error) {
	if limit <= 0 {
		limit = 50
	}

	invQuery := `
		SELECT i.id, i.request_id, i.trigger_type, i.caller_wallet,
			i.input_size, i.output_size, i.started_at, i.completed_at,
			i.duration_ms, i.status, i.error_message, i.memory_used_mb,
			COALESCE(i.node_id, '') AS node_id, COALESCE(i.queue_ms, 0) AS queue_ms,
			COALESCE(i.load_ms, 0) AS load_ms, COALESCE(i.db_calls, 0) AS db_calls,
			COALESCE(i.db_ms, 0) AS db_ms
		FROM function_invocations i
		JOIN functions f ON i.function_id = f.id
		WHERE f.namespace = ? AND f.name = ?
		ORDER BY i.started_at DESC
		LIMIT ?
	`
	var rows []struct {
		ID           string    `db:"id"`
		RequestID    string    `db:"request_id"`
		TriggerType  string    `db:"trigger_type"`
		CallerWallet string    `db:"caller_wallet"`
		InputSize    int       `db:"input_size"`
		OutputSize   int       `db:"output_size"`
		StartedAt    time.Time `db:"started_at"`
		CompletedAt  time.Time `db:"completed_at"`
		DurationMS   int64     `db:"duration_ms"`
		Status       string    `db:"status"`
		ErrorMessage string    `db:"error_message"`
		MemoryUsedMB float64   `db:"memory_used_mb"`
		NodeID       string    `db:"node_id"`
		QueueMS      int64     `db:"queue_ms"`
		LoadMS       int64     `db:"load_ms"`
		DBCalls      int64     `db:"db_calls"`
		DBMS         int64     `db:"db_ms"`
	}
	if err := r.db.Query(ctx, &rows, invQuery, namespace, name, limit); err != nil {
		return nil, fmt.Errorf("failed to query invocations: %w", err)
	}
	if len(rows) == 0 {
		return []Invocation{}, nil
	}

	// Batched fetch of nested WASM logs.
	invIDs := make([]interface{}, len(rows))
	for i, r := range rows {
		invIDs[i] = r.ID
	}
	placeholders := strings.Repeat("?,", len(invIDs))
	placeholders = placeholders[:len(placeholders)-1]
	logsQuery := `
		SELECT invocation_id, level, message, timestamp
		FROM function_logs
		WHERE invocation_id IN (` + placeholders + `)
		ORDER BY timestamp ASC
	`
	var logRows []struct {
		InvocationID string    `db:"invocation_id"`
		Level        string    `db:"level"`
		Message      string    `db:"message"`
		Timestamp    time.Time `db:"timestamp"`
	}
	logsByInv := map[string][]LogEntry{}
	if err := r.db.Query(ctx, &logRows, logsQuery, invIDs...); err != nil {
		// Don't fail the whole call — invocation summary is still useful.
		r.logger.Warn("failed to fetch nested WASM logs; returning invocations without them",
			zap.Error(err))
	} else {
		for _, lr := range logRows {
			logsByInv[lr.InvocationID] = append(logsByInv[lr.InvocationID], LogEntry{
				Level:     lr.Level,
				Message:   lr.Message,
				Timestamp: lr.Timestamp,
			})
		}
	}

	out := make([]Invocation, len(rows))
	for i, row := range rows {
		out[i] = Invocation{
			ID:           row.ID,
			RequestID:    row.RequestID,
			TriggerType:  row.TriggerType,
			CallerWallet: row.CallerWallet,
			InputSize:    row.InputSize,
			OutputSize:   row.OutputSize,
			StartedAt:    row.StartedAt,
			CompletedAt:  row.CompletedAt,
			DurationMS:   row.DurationMS,
			Status:       row.Status,
			ErrorMessage: row.ErrorMessage,
			MemoryUsedMB: row.MemoryUsedMB,
			NodeID:       row.NodeID,
			QueueMS:      row.QueueMS,
			LoadMS:       row.LoadMS,
			DBCalls:      row.DBCalls,
			DBMS:         row.DBMS,
			WASMLogs:     logsByInv[row.ID],
		}
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Private helpers
// -----------------------------------------------------------------------------

// wasmReplicationEverywhere pins WASM binaries on EVERY IPFS-Cluster peer
// (IPFS-Cluster treats a replication factor of -1 as "pin everywhere"). WASM
// blobs are tiny metadata-class content that any namespace gateway node may need
// to load on invoke; pinning everywhere (vs RF=3) means every node already holds
// the block locally, eliminating the cold cross-node bitswap fetch that made
// rarely-invoked functions intermittently time out (bugboard #137).
const wasmReplicationEverywhere = -1

// uploadWASM uploads WASM bytecode to IPFS and pins it on every cluster peer.
// A function whose code is not durably retrievable is not deployed, so a pin
// failure is fatal here — we must not leave a "deployed but unfetchable" row
// that intermittently 15s-times-out on whichever node happens to be cold.
func (r *Registry) uploadWASM(ctx context.Context, wasmBytes []byte, name string) (string, error) {
	// Imported locally and pinned once: Add would pin everywhere too, and a
	// second pin of a CID the cluster is still pinning cancels and restarts it
	// on every peer.
	reader := bytes.NewReader(wasmBytes)
	resp, err := r.ipfs.AddLocal(ctx, reader, name+".wasm")
	if err != nil {
		return "", fmt.Errorf("failed to upload WASM to IPFS: %w", err)
	}

	if _, err := r.ipfs.Pin(ctx, resp.Cid, name+".wasm", wasmReplicationEverywhere); err != nil {
		return "", fmt.Errorf("failed to pin WASM cid=%s on cluster: %w", resp.Cid, err)
	}

	// The pin is async per-peer, so confirm it actually converged to "pinned"
	// on every peer before declaring the function deployed. A "deployed but not
	// actually pinned" function is the bugboard #137 bug — hard-fail instead.
	if err := r.verifyPinnedEverywhere(ctx, resp.Cid); err != nil {
		return "", err
	}

	return resp.Cid, nil
}

// wasmDigest is the lowercase hex SHA-256 of a function's WASM, the value
// `shasum -a 256 function.wasm` prints for the file the owner built.
func wasmDigest(wasmBytes []byte) string {
	sum := sha256.Sum256(wasmBytes)
	return hex.EncodeToString(sum[:])
}

// verifyPinnedEverywhere polls PinStatus until the CID reports "pinned" on every
// cluster peer or wasmPinVerifyTimeout elapses. On timeout it returns a wrapped
// error naming the cid, the last observed status, and the pinned/total peer
// counts so the caller can surface an actionable failure.
//
// Caveat: "every peer" is every peer IPFS-Cluster currently reports in the CID's
// peer_map. A peer that is down/unreachable at verify time may be omitted from
// the peer_map entirely (rather than reported as cluster_error), so the
// pinned==total check can pass while an absent peer holds nothing. The guarantee
// is therefore "pinned on every peer the cluster currently reports," not "every
// peer that will ever exist" — a down peer during a rolling upgrade is exactly
// when this gap matters. The startup RepinAllWASM backfill re-covers such peers
// once they rejoin.
func (r *Registry) verifyPinnedEverywhere(ctx context.Context, cid string) error {
	deadline := time.Now().Add(wasmPinVerifyTimeout)
	ticker := time.NewTicker(wasmPinVerifyInterval)
	defer ticker.Stop()

	var lastStatus string
	var lastPinned, lastTotal int
	for {
		status, err := r.ipfs.PinStatus(ctx, cid)
		if err == nil {
			lastStatus, lastPinned, lastTotal = status.Status, status.PinnedPeers, status.TotalPeers
			if status.Status == ipfs.PinStatusPinned {
				return nil
			}
		} else {
			lastStatus = "query-failed: " + err.Error()
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("WASM pin not confirmed on all cluster peers: cid=%s last_status=%q pinned=%d/%d after %s",
				cid, lastStatus, lastPinned, lastTotal, wasmPinVerifyTimeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("WASM pin verification cancelled: cid=%s last_status=%q pinned=%d/%d: %w",
				cid, lastStatus, lastPinned, lastTotal, ctx.Err())
		case <-ticker.C:
		}
	}
}

// getByNameInternal retrieves a function by name regardless of status.
func (r *Registry) getByNameInternal(ctx context.Context, namespace, name string) (*Function, error) {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)

	query := `
		SELECT id, name, namespace, version, wasm_cid, wasm_sha256, source_cid,
			memory_limit_mb, timeout_seconds, is_public, is_internal,
			retry_count, retry_delay_seconds, dlq_topic,
			status, created_at, updated_at, created_by,
			ws_persistent, ws_idle_timeout_sec, ws_max_frame_bytes, ws_max_inflight_per_conn,
			raw_http_response, ws_auth,
			rate_limit_caller_requests, rate_limit_caller_window_sec,
			rate_limit_ip_requests, rate_limit_ip_window_sec
		FROM functions
		WHERE namespace = ? AND name = ?
		ORDER BY version DESC
		LIMIT 1
	`

	var functions []functionRow
	if err := r.db.Query(ctx, &functions, query, namespace, name); err != nil {
		return nil, fmt.Errorf("failed to query function: %w", err)
	}

	if len(functions) == 0 {
		return nil, ErrFunctionNotFound
	}

	return r.rowToFunction(&functions[0]), nil
}

// saveEnvVars saves environment variables for a function.
func (r *Registry) saveEnvVars(ctx context.Context, functionID string, envVars map[string]string) error {
	// Clear existing env vars first
	deleteQuery := `DELETE FROM function_env_vars WHERE function_id = ?`
	if _, err := r.db.Exec(ctx, deleteQuery, functionID); err != nil {
		return fmt.Errorf("failed to clear existing env vars: %w", err)
	}

	if len(envVars) == 0 {
		return nil
	}

	for key, value := range envVars {
		id := uuid.New().String()
		query := `INSERT INTO function_env_vars (id, function_id, key, value, created_at) VALUES (?, ?, ?, ?, ?)`
		if _, err := r.db.Exec(ctx, query, id, functionID, key, value, time.Now()); err != nil {
			return fmt.Errorf("failed to save env var '%s': %w", key, err)
		}
	}

	return nil
}

// rowToFunction converts a database row to a Function struct.
func (r *Registry) rowToFunction(row *functionRow) *Function {
	return &Function{
		ID:                row.ID,
		Name:              row.Name,
		Namespace:         row.Namespace,
		Version:           row.Version,
		WASMCID:           row.WASMCID,
		WASMSHA256:        nullableString(row.WASMSHA256),
		SourceCID:         row.SourceCID.String,
		MemoryLimitMB:     row.MemoryLimitMB,
		TimeoutSeconds:    row.TimeoutSeconds,
		IsPublic:          row.IsPublic,
		IsInternal:        row.IsInternal,
		RetryCount:        row.RetryCount,
		RetryDelaySeconds: row.RetryDelaySeconds,
		DLQTopic:          row.DLQTopic.String,
		Status:            FunctionStatus(row.Status),
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
		CreatedBy:         row.CreatedBy,

		// WS persistent-instance fields (#240/#249 follow-up). Without
		// these the WS handler's `if fn.WSPersistent` branch never
		// fires and persistent functions silently run as per-frame
		// stateless. See functionRow doc above for full history.
		WSPersistent:         row.WSPersistent,
		WSIdleTimeoutSec:     row.WSIdleTimeoutSec,
		WSMaxFrameBytes:      row.WSMaxFrameBytes,
		WSMaxInflightPerConn: row.WSMaxInflightPerConn,

		// Raw-HTTP-response mode (bugboard #835). Without reading this back
		// the invoke handler's `if fn.RawHTTPResponse` engine branch never
		// fires and set_http_response is a no-op for every function.
		RawHTTPResponse: row.RawHTTPResponse,

		// How the function's WebSocket may be opened (feat-264). Without
		// reading it back a function declaring `ws_auth: capability`
		// would refuse every capability.
		WSAuth: row.WSAuth,

		// The function's own invoke limits (bug-740). Without reading them
		// back the limiter's per-function tier never runs, which is how it
		// stayed dead before this column existed.
		RateLimit: rateLimitFromColumns(row.RateLimitCallerRequests, row.RateLimitCallerWindowSec,
			row.RateLimitIPRequests, row.RateLimitIPWindowSec),
	}
}

// nullableString is a NULL column as nil, so the API answers null rather than
// an empty string.
func nullableString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

// -----------------------------------------------------------------------------
// Database row types (internal)
// -----------------------------------------------------------------------------

type functionRow struct {
	ID                string         `db:"id"`
	Name              string         `db:"name"`
	Namespace         string         `db:"namespace"`
	Version           int            `db:"version"`
	WASMCID           string         `db:"wasm_cid"`
	WASMSHA256        sql.NullString `db:"wasm_sha256"`
	SourceCID         sql.NullString `db:"source_cid"`
	MemoryLimitMB     int            `db:"memory_limit_mb"`
	TimeoutSeconds    int            `db:"timeout_seconds"`
	IsPublic          bool           `db:"is_public"`
	IsInternal        bool           `db:"is_internal"`
	RetryCount        int            `db:"retry_count"`
	RetryDelaySeconds int            `db:"retry_delay_seconds"`
	DLQTopic          sql.NullString `db:"dlq_topic"`
	Status            string         `db:"status"`
	CreatedAt         time.Time      `db:"created_at"`
	UpdatedAt         time.Time      `db:"updated_at"`
	CreatedBy         string         `db:"created_by"`

	// WS persistent-instance metadata (#240/#249 follow-up).
	//
	// Pre-fix history: these columns existed in the schema (migration
	// 011) and Register() at line 110+ wrote them, but every read path
	// (Get, List, GetByID, GetByNameInternal) omitted them from the
	// SELECT and functionRow had no fields for them. Result:
	// `fn.WSPersistent` was always the zero value (false) regardless
	// of what the DB said. Every WS function silently ran in
	// per-frame stateless mode — not the persistent mode the
	// `ws_persistent: true` config asks for.
	//
	// AnChat's rpc-router was the canary: it relies on per-connection
	// instance state (request_id ↔ reply correlation, persistent
	// subscription bookkeeping) that the stateless model destroys
	// every frame. Symptom: gateway-side function invocations succeed
	// (telemetry envelope `{request_id, status, duration_ms}` reaches
	// the client) but the function's own `ws_send` frames don't carry
	// the per-connection state the function expects. End-user impact
	// was every RPC timing out at 15 s.
	WSPersistent         bool `db:"ws_persistent"`
	WSIdleTimeoutSec     int  `db:"ws_idle_timeout_sec"`
	WSMaxFrameBytes      int  `db:"ws_max_frame_bytes"`
	WSMaxInflightPerConn int  `db:"ws_max_inflight_per_conn"`

	// Raw-HTTP-response mode (bugboard #835). Backed by migration
	// 029_raw_http_response.sql; defaults to false so existing functions
	// keep the JSON/Ack-wrapped behavior.
	RawHTTPResponse bool `db:"raw_http_response"`

	// WSAuth is how the function's WebSocket may be opened (feat-264).
	// Backed by migration 061; '' is a credential, as before.
	WSAuth string `db:"ws_auth"`

	// The function's own invoke limits (bug-740, migration 083). Zero is no
	// rule, which every function deployed before the columns has.
	RateLimitCallerRequests  int `db:"rate_limit_caller_requests"`
	RateLimitCallerWindowSec int `db:"rate_limit_caller_window_sec"`
	RateLimitIPRequests      int `db:"rate_limit_ip_requests"`
	RateLimitIPWindowSec     int `db:"rate_limit_ip_window_sec"`
}

type envVarRow struct {
	Key   string `db:"key"`
	Value string `db:"value"`
}
