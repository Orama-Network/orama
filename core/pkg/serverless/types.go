// Package serverless provides a WASM-based serverless function engine for the Orama Network.
// It enables users to deploy and execute Go functions (compiled to WASM) across all nodes,
// with support for HTTP/WebSocket triggers, cron jobs, database triggers, pub/sub triggers,
// one-time timers, retries with DLQ, and background jobs.
package serverless

import (
	"context"
	"io"
	"time"
)

// FunctionStatus represents the current state of a deployed function.
type FunctionStatus string

const (
	FunctionStatusActive   FunctionStatus = "active"
	FunctionStatusInactive FunctionStatus = "inactive"
	FunctionStatusError    FunctionStatus = "error"
	// FunctionStatusDeleted marks a deleted version. It is distinct from
	// inactive (a disabled function) so enabling can never revive a delete.
	FunctionStatusDeleted FunctionStatus = "deleted"
	// FunctionStatusSuperseded marks a version a later deploy or a rollback
	// replaced. It is kept as a rollback target and is never invoked: only the
	// current version of a function runs, so code its owner replaced cannot be
	// reached at name@N. Enabling and disabling leave it alone.
	FunctionStatusSuperseded FunctionStatus = "superseded"
)

// TriggerType identifies the type of event that triggered a function invocation.
type TriggerType string

const (
	TriggerTypeHTTP      TriggerType = "http"
	TriggerTypeWebSocket TriggerType = "websocket"
	TriggerTypeCron      TriggerType = "cron"
	TriggerTypeDatabase  TriggerType = "database"
	TriggerTypePubSub    TriggerType = "pubsub"
	TriggerTypeTimer     TriggerType = "timer"
	TriggerTypeJob       TriggerType = "job"
	// TriggerTypeInternal marks an invocation with no end-user caller. Treated
	// as a system trigger so the per-caller authorization check is skipped.
	//
	// Two producers:
	//  1. Gateway-initiated hooks — e.g. the auth claims-provider at JWT mint
	//     time (bugboard #548). The gateway is the trusted invoker.
	//  2. A nested function_invoke whose PARENT was system-triggered (bugboard
	//     #159), so a cron/pubsub handler can compose functions in its own
	//     namespace. See hostfunctions.nestedTriggerType.
	//
	// Because both map to the same value, `invocations.trigger_type` cannot
	// distinguish them after the fact.
	TriggerTypeInternal TriggerType = "internal"
)

// JobStatus represents the current state of a background job.
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
)

// InvocationStatus represents the result of a function invocation.
type InvocationStatus string

const (
	InvocationStatusSuccess InvocationStatus = "success"
	InvocationStatusError   InvocationStatus = "error"
	InvocationStatusTimeout InvocationStatus = "timeout"
)

// DBOperation represents the type of database operation that triggered a function.
type DBOperation string

const (
	DBOperationInsert DBOperation = "INSERT"
	DBOperationUpdate DBOperation = "UPDATE"
	DBOperationDelete DBOperation = "DELETE"
)

// -----------------------------------------------------------------------------
// Core Interfaces (following Interface Segregation Principle)
// -----------------------------------------------------------------------------

// FunctionRegistry manages function metadata and bytecode storage.
// Responsible for CRUD operations on function definitions.
type FunctionRegistry interface {
	// Register deploys a function as a new version and keeps the versions before it.
	// Returns the superseded (previously latest) version, or nil if it was a new registration.
	Register(ctx context.Context, fn *FunctionDefinition, wasmBytes []byte) (*Function, error)

	// Get retrieves a function by name and optional version.
	// If version is 0, returns the current version; otherwise exactly that
	// version if it is the current one. A version that was replaced is
	// ErrVersionSuperseded.
	Get(ctx context.Context, namespace, name string, version int) (*Function, error)

	// List returns the current version of every function in a namespace.
	List(ctx context.Context, namespace string) ([]*Function, error)

	// ListAll returns every version row the namespace holds, whatever its
	// status: current, disabled, superseded and deleted.
	ListAll(ctx context.Context, namespace string) ([]*Function, error)

	// Delete marks a function deleted. If version is 0, marks all versions.
	Delete(ctx context.Context, namespace, name string, version int) error

	// Purge removes a function's rows for good, with its environment,
	// triggers and history. If version is 0, removes every version.
	Purge(ctx context.Context, namespace, name string, version int) error

	// Rollback makes version the function's current version and supersedes
	// the one it replaces; the triggers move to it.
	Rollback(ctx context.Context, namespace, name string, version int) error

	// SetEnabled toggles a function's status between active and inactive
	// without redeploying. Plan 11.5 — lets operators pause a misbehaving
	// function during an incident response. Existing in-flight invocations
	// finish; new ones see the function as missing/inactive and the
	// invoker rejects them upstream. Returns ErrFunctionNotFound if the
	// name doesn't exist in the namespace.
	SetEnabled(ctx context.Context, namespace, name string, enabled bool) error

	// GetWASMBytes retrieves the compiled WASM bytecode for a function.
	GetWASMBytes(ctx context.Context, wasmCID string) ([]byte, error)

	// GetLogs returns WASM-emitted log entries (function_logs rows). Often
	// empty because most functions don't call log_info / log_error. Use
	// GetInvocations for the always-populated invocation-history view.
	GetLogs(ctx context.Context, namespace, name string, limit int) ([]LogEntry, error)

	// GetInvocations returns invocation history for a function in reverse
	// chronological order, with any associated WASM log entries nested
	// per record. Always populated when the function has been invoked.
	GetInvocations(ctx context.Context, namespace, name string, limit int) ([]Invocation, error)
}

// Invocation is the record of one function invocation as seen by
// `orama function logs`. Mirrors registry.Invocation; defined here at the
// public package boundary so callers don't need to import the inner package.
type Invocation struct {
	ID           string     `json:"id"`
	RequestID    string     `json:"request_id"`
	TriggerType  string     `json:"trigger_type"`
	CallerWallet string     `json:"caller_wallet,omitempty"`
	InputSize    int        `json:"input_size"`
	OutputSize   int        `json:"output_size"`
	StartedAt    time.Time  `json:"started_at"`
	CompletedAt  time.Time  `json:"completed_at"`
	DurationMS   int64      `json:"duration_ms"`
	Status       string     `json:"status"`
	ErrorMessage string     `json:"error_message,omitempty"`
	MemoryUsedMB float64    `json:"memory_used_mb,omitempty"`
	WASMLogs     []LogEntry `json:"wasm_logs,omitempty"`
}

// FunctionExecutor handles the actual execution of WASM functions.
type FunctionExecutor interface {
	// Execute runs a function with the given input and returns the output.
	Execute(ctx context.Context, fn *Function, input []byte, invCtx *InvocationContext) ([]byte, error)

	// Precompile compiles a WASM module and caches it for faster execution.
	Precompile(ctx context.Context, wasmCID string, wasmBytes []byte) error

	// Invalidate removes a compiled module from the cache.
	Invalidate(wasmCID string)
}

// SecretsManager handles secure storage and retrieval of secrets.
type SecretsManager interface {
	// Set stores an encrypted secret.
	Set(ctx context.Context, namespace, name, value string) error

	// Get retrieves a decrypted secret.
	Get(ctx context.Context, namespace, name string) (string, error)

	// List returns all secret names for a namespace (not values).
	List(ctx context.Context, namespace string) ([]string, error)

	// Delete removes a secret.
	Delete(ctx context.Context, namespace, name string) error
}

// TriggerManager manages function triggers (cron, database, pubsub, timer).
type TriggerManager interface {
	// AddCronTrigger adds a cron-based trigger to a function.
	AddCronTrigger(ctx context.Context, functionID, cronExpr string) error

	// AddDBTrigger adds a database trigger to a function.
	AddDBTrigger(ctx context.Context, functionID, tableName string, operation DBOperation, condition string) error

	// AddPubSubTrigger adds a pubsub trigger to a function.
	AddPubSubTrigger(ctx context.Context, functionID, topic string) error

	// ScheduleOnce schedules a one-time execution.
	ScheduleOnce(ctx context.Context, functionID string, runAt time.Time, payload []byte) (string, error)

	// RemoveTrigger removes a trigger by ID.
	RemoveTrigger(ctx context.Context, triggerID string) error
}

// JobManager manages background job execution.
type JobManager interface {
	// Enqueue adds a job to the queue for background execution.
	Enqueue(ctx context.Context, functionID string, payload []byte) (string, error)

	// GetStatus retrieves the current status of a job.
	GetStatus(ctx context.Context, jobID string) (*Job, error)

	// List returns jobs for a function.
	List(ctx context.Context, functionID string, limit int) ([]*Job, error)

	// Cancel attempts to cancel a pending or running job.
	Cancel(ctx context.Context, jobID string) error
}

// WebSocketManager manages WebSocket connections for function streaming.
type WebSocketManager interface {
	// Register registers a new WebSocket connection.
	Register(clientID string, conn WebSocketConn)

	// Unregister removes a WebSocket connection.
	Unregister(clientID string)

	// Send sends data to a specific client.
	Send(clientID string, data []byte) error

	// Broadcast sends data to all clients subscribed to a topic.
	Broadcast(topic string, data []byte) error

	// Subscribe adds a client to a topic.
	Subscribe(clientID, topic string)

	// Unsubscribe removes a client from a topic.
	Unsubscribe(clientID, topic string)
}

// WebSocketConn abstracts a WebSocket connection for testability.
//
// WriteControl and Close must be safe to call concurrently with the reader and
// the writer: they are how another goroutine ends the connection.
type WebSocketConn interface {
	WriteMessage(messageType int, data []byte) error
	WriteControl(messageType int, data []byte, deadline time.Time) error
	ReadMessage() (messageType int, p []byte, err error)
	Close() error
}

// -----------------------------------------------------------------------------
// Data Types
// -----------------------------------------------------------------------------

// FunctionDefinition contains the configuration for deploying a function.
type FunctionDefinition struct {
	Name           string `json:"name"`
	Namespace      string `json:"namespace"`
	Version        int    `json:"version,omitempty"`
	MemoryLimitMB  int    `json:"memory_limit_mb,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	IsPublic       bool   `json:"is_public,omitempty"`
	// IsInternal marks a function invokable ONLY by a system trigger or an
	// admin caller (bugboard #152). Default false → invokable as before.
	IsInternal        bool              `json:"is_internal,omitempty"`
	RetryCount        int               `json:"retry_count,omitempty"`
	RetryDelaySeconds int               `json:"retry_delay_seconds,omitempty"`
	DLQTopic          string            `json:"dlq_topic,omitempty"`
	EnvVars           map[string]string `json:"env_vars,omitempty"`
	CronExpressions   []string          `json:"cron_expressions,omitempty"`
	DBTriggers        []DBTriggerConfig `json:"db_triggers,omitempty"`
	PubSubTopics      []string          `json:"pubsub_topics,omitempty"`

	// Persistent WebSocket settings — see plan 06_PERSISTENT_WS_FUNCTIONS.md
	// When WSPersistent is true, the function exports ws_open/ws_frame/ws_close
	// instead of using the default per-frame stateless model.
	WSPersistent         bool `json:"ws_persistent,omitempty"`
	WSIdleTimeoutSec     int  `json:"ws_idle_timeout_sec,omitempty"`      // 0 = no idle timeout
	WSMaxFrameBytes      int  `json:"ws_max_frame_bytes,omitempty"`       // 0 = use default 256 KB
	WSMaxInflightPerConn int  `json:"ws_max_inflight_per_conn,omitempty"` // 0 = use default 64

	// RawHTTPResponse enables raw-HTTP-response mode (bugboard #835): the
	// function may call set_http_response to emit a verbatim status/headers/
	// body instead of the JSON/Ack-wrapped output. See pkg/serverless/raw_http.go.
	RawHTTPResponse bool `json:"raw_http_response,omitempty"`

	// WSAuth is how the function's WebSocket may be opened: "" for a caller's
	// credential, as every function has been, or WSAuthCapability to also
	// accept a capability minted through the function (feat-264).
	WSAuth string `json:"ws_auth,omitempty"`

	// RateLimit is the function's own invoke limits (function.yaml
	// rate_limit), nil for none. See function_rate_limit.go.
	RateLimit *FunctionRateLimit `json:"rate_limit,omitempty"`
}

// DBTriggerConfig defines a database trigger configuration.
type DBTriggerConfig struct {
	Table     string      `json:"table"`
	Operation DBOperation `json:"operation"`
	Condition string      `json:"condition,omitempty"`
}

// Function represents a deployed serverless function.
type Function struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Version   int    `json:"version"`
	WASMCID   string `json:"wasm_cid"`
	// WASMSHA256 is the lowercase hex SHA-256 of the WASM as the gateway
	// received it, before sealing: unlike WASMCID, the same bytes always give
	// the same value. nil (JSON null) for a version deployed before the
	// column existed (migration 093).
	WASMSHA256     *string `json:"wasm_sha256"`
	SourceCID      string  `json:"source_cid,omitempty"`
	MemoryLimitMB  int     `json:"memory_limit_mb"`
	TimeoutSeconds int     `json:"timeout_seconds"`
	IsPublic       bool    `json:"is_public"`
	// IsInternal marks a function invokable ONLY by a system trigger or an
	// admin caller (bugboard #152). Enforced in canInvokeFn; default false
	// so every existing function stays invokable exactly as before.
	IsInternal        bool           `json:"is_internal"`
	RetryCount        int            `json:"retry_count"`
	RetryDelaySeconds int            `json:"retry_delay_seconds"`
	DLQTopic          string         `json:"dlq_topic,omitempty"`
	Status            FunctionStatus `json:"status"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	CreatedBy         string         `json:"created_by"`

	// Persistent WebSocket settings — see plan 06_PERSISTENT_WS_FUNCTIONS.md
	WSPersistent         bool `json:"ws_persistent,omitempty"`
	WSIdleTimeoutSec     int  `json:"ws_idle_timeout_sec,omitempty"`
	WSMaxFrameBytes      int  `json:"ws_max_frame_bytes,omitempty"`
	WSMaxInflightPerConn int  `json:"ws_max_inflight_per_conn,omitempty"`

	// RawHTTPResponse — bugboard #835. When true, the function may emit a
	// verbatim HTTP response via set_http_response instead of the
	// JSON/Ack-wrapped output. See pkg/serverless/raw_http.go.
	RawHTTPResponse bool `json:"raw_http_response,omitempty"`

	// WSAuth is how the function's WebSocket may be opened: "" for a caller's
	// credential, as every function has been, or WSAuthCapability to also
	// accept a capability minted through the function (feat-264).
	WSAuth string `json:"ws_auth,omitempty"`

	// RateLimit is the function's own invoke limits (function.yaml
	// rate_limit), nil for none. See function_rate_limit.go.
	RateLimit *FunctionRateLimit `json:"rate_limit,omitempty"`
}

// InvocationContext provides context for a function invocation.
type InvocationContext struct {
	RequestID    string `json:"request_id"`
	FunctionID   string `json:"function_id"`
	FunctionName string `json:"function_name"`
	Namespace    string `json:"namespace"`
	CallerWallet string `json:"caller_wallet,omitempty"`
	// CallerIP is the source IP of the request, populated by HTTP/WS handlers.
	// Used by the multi-tier rate limiter as a fallback bucket for anonymous
	// (no-wallet) callers.
	CallerIP string `json:"caller_ip,omitempty"`
	// CallerKey names the credential of a caller with no wallet; see
	// InvokeRequest.CallerKey. Carried into nested invocations so a function's
	// own calls are charged to the caller that started them.
	CallerKey string `json:"caller_key,omitempty"`
	// CallerIsAdmin marks the caller as holding the admin (control-plane)
	// grant (bugboard #152). Propagated into child WASM→WASM invocations so
	// an internal→internal call works while external→internal stays blocked.
	CallerIsAdmin bool `json:"caller_is_admin,omitempty"`
	// CallerHasInvoke mirrors InvokeRequest.CallerHasInvoke so a nested
	// function_invoke can pass on the grant the caller actually holds. It was
	// not carried, so a caller with the invoke grant but no admin bit reached
	// a private function directly and was refused by the same function's own
	// nested call.
	CallerHasInvoke bool `json:"caller_has_invoke,omitempty"`
	// SystemOriginated marks an invocation the gateway itself started; see
	// InvokeRequest.SystemOriginated. A nested call made from inside such an
	// invocation carries it on, so the chain keeps the authority the trigger
	// had without the trigger type having to stand in for it.
	SystemOriginated bool              `json:"-"`
	TriggerType      TriggerType       `json:"trigger_type"`
	WSClientID       string            `json:"ws_client_id,omitempty"`
	EnvVars          map[string]string `json:"env_vars,omitempty"`
	// CallerClaims holds custom JWT claims set on the caller's token (beyond
	// the standard sub/namespace fields). Read via host fn `get_caller_claim`.
	// Populated by auth handlers from JWTClaims.Custom; empty for non-JWT auth.
	CallerClaims map[string]string `json:"caller_claims,omitempty"`

	// CallerJWTSubject is the `sub` claim of the Bearer JWT, if any.
	// EXPLICITLY captured from the JWT independent of the API-key-vs-JWT
	// wallet-resolution heuristic — so functions that must bind on the
	// JWT-signed identity (signup flows) can do so reliably even when the
	// caller also presents an API key. Empty string when the request was
	// not JWT-authenticated. Bug #215.
	CallerJWTSubject string `json:"caller_jwt_subject,omitempty"`

	// CallerDeviceID is the device the caller's session is bound to: the
	// RFC 7638 thumbprint of a key the device proved it holds when the session
	// was issued, carried as the token's `did`. Empty for a session bound to
	// the account alone, an API key, or no credential. Read via host fn
	// `get_caller_device_id`; like the subject, it is set only by the gateway.
	CallerDeviceID string `json:"caller_device_id,omitempty"`

	// CallerAttestation is what the caller's device attestation proved
	// (feat-941), or nil when the caller's token carries none. Read via host
	// fn `get_caller_attestation`; set only by the gateway, from the verified
	// token's `att` claim.
	CallerAttestation *CallerAttestation `json:"caller_attestation,omitempty"`

	// CallerCapability is what the capability the caller's socket was opened
	// with grants, or nil for a caller who came in on a credential. Read via
	// host fn `get_caller_capability`. A nested function_invoke does not
	// carry it: a capability opens the function that minted it, no other.
	CallerCapability *CapabilityGrant `json:"caller_capability,omitempty"`

	// TriggerDepth is the recursion-depth bucket for trigger-driven
	// invocations. 0 means a top-level (HTTP/WS/cron) invocation; each
	// PubSub-trigger-driven invocation increments it. The host-fn
	// wildcard-publish path (`oh.PubSubPublish` → DispatchLocalPublish)
	// reads this and refuses to fire wildcards once depth ≥
	// maxTriggerDepth, preventing local-only recursion loops a function
	// could create by publishing topics that match its own wildcard
	// trigger (bugboard #93 follow-up).
	TriggerDepth int `json:"trigger_depth,omitempty"`

	// RawHTTP carries a verbatim HTTP response set by a RawHTTPResponse
	// function (bugboard #835). The engine populates this from the
	// per-invocation collector after Execute returns; the Invoker surfaces
	// it on InvokeResponse so the HTTP handler can replay it. nil/unset for
	// normal functions and functions that didn't call set_http_response.
	// Not serialized — internal plumbing only.
	RawHTTP *RawHTTPResult `json:"-"`
}

// InvocationResult represents the result of a function invocation.
type InvocationResult struct {
	RequestID  string           `json:"request_id"`
	Output     []byte           `json:"output,omitempty"`
	Status     InvocationStatus `json:"status"`
	Error      string           `json:"error,omitempty"`
	DurationMS int64            `json:"duration_ms"`
	Logs       []LogEntry       `json:"logs,omitempty"`
}

// LogEntry represents a log message from a function.
type LogEntry struct {
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

// Job represents a background job.
type Job struct {
	ID          string     `json:"id"`
	FunctionID  string     `json:"function_id"`
	Payload     []byte     `json:"payload,omitempty"`
	Status      JobStatus  `json:"status"`
	Progress    int        `json:"progress"`
	Result      []byte     `json:"result,omitempty"`
	Error       string     `json:"error,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// CronTrigger represents a cron-based trigger.
type CronTrigger struct {
	ID             string     `json:"id"`
	FunctionID     string     `json:"function_id"`
	CronExpression string     `json:"cron_expression"`
	NextRunAt      *time.Time `json:"next_run_at,omitempty"`
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	Enabled        bool       `json:"enabled"`
}

// DBTrigger represents a database trigger.
type DBTrigger struct {
	ID         string      `json:"id"`
	FunctionID string      `json:"function_id"`
	TableName  string      `json:"table_name"`
	Operation  DBOperation `json:"operation"`
	Condition  string      `json:"condition,omitempty"`
	Enabled    bool        `json:"enabled"`
}

// PubSubTrigger represents a pubsub trigger.
//
// Topic may be an exact topic name or a SQLite GLOB pattern (e.g.
// "presence:*"). See pkg/serverless/triggers/pattern.go for matching rules.
//
// AggregationWindowMs > 0 enables event buffering: the dispatcher accumulates
// events for at most that many milliseconds (or until AggregationMaxBatchSize
// events have been collected, whichever comes first), then invokes the
// function once with a batched payload of type BatchedPubSubEvent.
type PubSubTrigger struct {
	ID                      string `json:"id"`
	FunctionID              string `json:"function_id"`
	Topic                   string `json:"topic"`
	Enabled                 bool   `json:"enabled"`
	AggregationWindowMs     int    `json:"aggregation_window_ms,omitempty"`
	AggregationMaxBatchSize int    `json:"aggregation_max_batch_size,omitempty"`
}

// Timer represents a one-time scheduled execution.
type Timer struct {
	ID         string    `json:"id"`
	FunctionID string    `json:"function_id"`
	RunAt      time.Time `json:"run_at"`
	Payload    []byte    `json:"payload,omitempty"`
	Status     JobStatus `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

// DBChangeEvent is passed to functions triggered by database changes.
type DBChangeEvent struct {
	Table     string                 `json:"table"`
	Operation DBOperation            `json:"operation"`
	Row       map[string]interface{} `json:"row"`
	OldRow    map[string]interface{} `json:"old_row,omitempty"`
}

// -----------------------------------------------------------------------------
// Host Function Types (passed to WASM functions)
// -----------------------------------------------------------------------------

// HostServices provides access to Orama services from within WASM functions.
// This interface is implemented by the host and exposed to WASM modules.
type HostServices interface {
	// Database operations
	DBQuery(ctx context.Context, query string, args []interface{}) ([]byte, error)
	DBExecute(ctx context.Context, query string, args []interface{}) (int64, error)

	// DBExecuteV2 is the typed equivalent of DBExecute that returns BOTH the
	// rows-affected count AND a JSON envelope. The legacy DBExecute returns
	// only uint32(rows) — collapsing real errors into "0 rows affected"
	// (bug #218). New code should call DBExecuteV2 to detect real failures.
	//
	// Output JSON shape:
	//   {"rows_affected": 1, "last_insert_id": 42, "error": ""}     // success
	//   {"rows_affected": 0, "last_insert_id": 0,  "error": "..."}  // failure
	//
	// Returns a Go error only on host-side validation failures (no DB,
	// bad JSON args). SQL execution errors are encoded in the JSON.
	DBExecuteV2(ctx context.Context, query string, args []interface{}) ([]byte, error)

	// DBQueryV2 is the typed equivalent of DBQuery — returns a JSON envelope
	// distinguishing "empty result" from "query failed".
	//
	// Output JSON shape:
	//   {"rows": [...], "error": ""}     // success (rows may be [])
	//   {"rows": [],     "error": "..."}  // failure
	DBQueryV2(ctx context.Context, query string, args []interface{}) ([]byte, error)

	// Cache operations
	CacheGet(ctx context.Context, key string) ([]byte, error)
	CacheSet(ctx context.Context, key string, value []byte, ttlSeconds int64) error
	CacheDelete(ctx context.Context, key string) error
	CacheIncr(ctx context.Context, key string) (int64, error)
	CacheIncrBy(ctx context.Context, key string, delta int64) (int64, error)
	// CacheIncrByTTL is CacheIncrBy that gives the counter an expiry of
	// ttlSeconds when the call creates it; an existing counter keeps its own.
	CacheIncrByTTL(ctx context.Context, key string, delta, ttlSeconds int64) (int64, error)

	// Storage operations
	StoragePut(ctx context.Context, data []byte) (string, error)
	StorageGet(ctx context.Context, cid string) ([]byte, error)

	// PubSub operations
	PubSubPublish(ctx context.Context, topic string, data []byte) error
	PubSubPublishBatch(ctx context.Context, msgsJSON []byte) error

	// Push notifications. Sends to all of `userID`'s registered devices in
	// the function's namespace. `msgJSON` is the JSON-encoded PushSendArgs
	// shape (see hostfunctions.PushSend). Returns nil if push is not
	// configured (silent no-op) so functions can be portable across
	// namespaces with/without push enabled.
	PushSend(ctx context.Context, userID string, msgJSON []byte) error

	// DBTransaction executes a batch of SQL statements atomically via the
	// native RQLite transaction endpoint. opsJSON is the JSON-encoded
	// {"ops": [{"kind":"exec"|"query","sql":"...","args":[...]}]} shape.
	// Returns the JSON-encoded BatchResult; the boolean inside the result
	// (committed) tells the caller whether the writes landed.
	//
	// Returns an error only on setup/validation failures (no DB, bad JSON,
	// too many ops). A rollback is reported via committed=false in the
	// returned JSON, NOT as a Go error.
	DBTransaction(ctx context.Context, opsJSON []byte) ([]byte, error)

	// DBQueryBatch runs N SELECT statements in ONE round-trip to the leader
	// (via RQLite's /db/query bulk endpoint). All queries see the same
	// committed snapshot. opsJSON shape: {"ops":[{"sql":"...","args":[...]}, ...]}.
	// Returns JSON {"results":[{"rows":[...], "error":""}, ...]} with one
	// entry per input op, in the same order. Per-query errors are surfaced
	// in the per-op `error` field; the call only returns a Go error on
	// transport/validation failures.
	//
	// Use this for read-heavy functions that gather state from many tables
	// before doing work — e.g. anchat's message-create reads auth +
	// participants + devices (7-10 SELECTs) before writing. Empirically on
	// devnet's cross-region cluster: 10 sequential DBQuery = ~3.5s; one
	// DBQueryBatch with 10 statements = ~340ms. See bugboard #270.
	DBQueryBatch(ctx context.Context, opsJSON []byte) ([]byte, error)

	// PushSendV2 dispatches a push notification with PER-DEVICE result
	// reporting. Returns JSON-encoded push.SendDetailedResult:
	//
	//   {
	//     "ok": false,
	//     "devices_attempted": 2,
	//     "devices_succeeded": 1,
	//     "results": [
	//       {"device_id":"ios-A", "provider":"apns", "success":true},
	//       {"device_id":"ios-B", "provider":"apns", "success":false,
	//        "http_status":410, "reason":"Unregistered",
	//        "message":"...", "unregistered":true}
	//     ]
	//   }
	//
	// Unlike the legacy PushSend (which returns success/fail and discards
	// every provider's HTTP status), this lets WASM callers auto-clean
	// stale tokens, retry transient failures, and surface real reasons.
	// Bugboard #348.
	//
	// Returns a Go error only on setup failures (no manager, invalid JSON,
	// no namespace in invocation context). A per-device failure goes into
	// the JSON `results[]` array, NOT as a Go error — callers parse the
	// envelope. Same shape as DBTransaction's "structured per-op result".
	PushSendV2(ctx context.Context, userID string, msgJSON []byte) ([]byte, error)

	// PushSendTopic delivers to the device registered under a rotating push
	// topic (FEAT-265) in the function's namespace, and returns the same JSON
	// envelope as PushSendV2. topicID is the lowercase hex SHA-256 of the
	// device's topic secret. An unknown or expired topic is reported in the
	// envelope (reason "TopicNotFound"), not as a Go error.
	PushSendTopic(ctx context.Context, topicID string, msgJSON []byte) ([]byte, error)

	// TurnCredentials mints per-namespace TURN HMAC credentials for the
	// caller's namespace (derived from invocation context — caller
	// cannot spoof). Returns a JSON envelope matching the HTTP endpoint
	// at /v1/webrtc/turn/credentials:
	//
	//   {
	//     "configured": true,
	//     "username": "<unix-ts>:<namespace>",
	//     "password": "<hmac>",
	//     "ttl": 600,
	//     "uris": ["turn:...", "turns:...:443"],
	//     "namespace": "<ns>"
	//   }
	//
	// When TURN isn't configured on this gateway (TURNSecret empty),
	// returns {configured:false, namespace:<ns>} as a structured envelope
	// — NOT a Go error. This matches PushSend's silent-noop semantics so
	// functions stay portable across deployments with/without TURN.
	//
	// Bugboard feat-9 — removes the round-trip through HTTP for WASM
	// functions that need to inject TURN credentials into a peer's
	// RTCConfiguration without going back out to the gateway.
	TurnCredentials(ctx context.Context) ([]byte, error)

	// ExecAndPublish runs ops atomically (like DBTransaction) and, ONLY
	// if the batch commits, publishes data to the named topic with any
	// occurrence of the literal string "{{seq}}" replaced by the assigned
	// per-namespace sequence number.
	//
	// Subscribers can use the seq to detect cross-node replication-lag
	// gaps ("I expected seq N+1, got N+3, must have missed two").
	//
	// Returns the JSON-encoded result with extra fields: seq, published,
	// publish_error (in addition to the embedded BatchResult shape).
	// Rollback or publish failure is reported in the JSON, NOT as Go error.
	ExecAndPublish(ctx context.Context, opsJSON []byte, topic string, dataTemplate []byte) ([]byte, error)

	// WSPubSubBridge wires a WebSocket client directly to a PubSub topic
	// in the function's namespace. The gateway then auto-forwards every
	// matching libp2p message to that client's WS without invoking this
	// function per event. Idempotent.
	//
	// The function's namespace must match the client's namespace (set at
	// WS upgrade time) — namespaces are server-trusted; functions cannot
	// bridge clients in another namespace's topic.
	WSPubSubBridge(ctx context.Context, clientID, topic string) error

	// WSPubSubUnbridge removes a previously-established bridge. Idempotent.
	// Auto-cleaned on WS disconnect, so functions don't have to call this
	// in OnClose unless they want to dynamically unsubscribe.
	WSPubSubUnbridge(ctx context.Context, clientID, topic string) error

	// SetHTTPResponse records a verbatim HTTP response (status, headers, body)
	// for a RawHTTPResponse function (bugboard #835). The HTTP invoke handler
	// replays it byte-for-byte instead of the JSON/Ack-wrapped output, so a
	// function can transparently proxy an upstream RPC. Returns an error when
	// the function is NOT deployed with raw_http_response, or when the status /
	// header count / body size fail validation. headers may be nil.
	SetHTTPResponse(ctx context.Context, status int, headers map[string]string, body []byte) error

	// EphemeralStateSet records WS-subscribe-tracked ephemeral state owned by
	// the current invocation's WS client (bugboard #710) and publishes a "set"
	// event on the topic so subscribers observe it. The state auto-clears (with
	// a synthetic "clear" event) when the owning WS client disconnects, and
	// also expires after ttlMs (clamped to a max; <=0 uses a default). Returns
	// an error when there is no WS client in context, on empty topic/key, on an
	// oversized payload, or when the client's per-connection key cap is hit.
	EphemeralStateSet(ctx context.Context, topic, key string, payload []byte, ttlMs int64) error

	// EphemeralStateClear removes ephemeral state the current WS client owns
	// and publishes a "clear" event. Idempotent: clearing a missing or
	// non-owned key is a no-op. Errors only on no-WS-client / empty topic-key.
	EphemeralStateClear(ctx context.Context, topic, key string) error

	// EphemeralStateList returns the live entries on a topic in the current
	// invocation's namespace as a JSON envelope:
	//   {"entries":[{"key":..,"client_id":..,"payload":<base64>,"expires_in_ms":..}, …]}
	// The reconnect catch-up read (bugboard #710 acceptance): unlike
	// Set/Clear it does NOT require a WS client in context — any function
	// invocation may read. Errors on empty topic or no invocation context.
	EphemeralStateList(ctx context.Context, topic string) ([]byte, error)

	// WebSocket operations (only valid in WS context)
	WSSend(ctx context.Context, clientID string, data []byte) error
	WSBroadcast(ctx context.Context, topic string, data []byte) error

	// FunctionInvoke synchronously invokes another function in the same
	// namespace from inside a function (e.g. a persistent rpc-router
	// dispatching client RPCs to per-op handlers). The caller's wallet,
	// JWT claims, and WS client ID are inherited so the invoked function
	// sees the same authenticated identity as the outer call.
	//
	// `name` is the target function name; `payload` is the raw input bytes
	// to feed the function (typically JSON). Returns the function's output
	// bytes on success. Errors (not found, unauthorized, runtime) are
	// returned as Go errors and the caller should surface them as
	// rpc_error to the client.
	FunctionInvoke(ctx context.Context, name string, payload []byte) ([]byte, error)

	// FunctionInvokeAsync invokes another function in the same namespace
	// CONCURRENTLY and returns immediately — it does NOT wait for or return
	// the target's output. The target runs in the engine's execution pool
	// inheriting the caller's identity (wallet, JWT claims, WS client ID),
	// and is expected to deliver any result to the client itself via ws_send
	// (it has the same WS client ID).
	//
	// This is the non-blocking counterpart to FunctionInvoke, for a
	// persistent dispatcher (rpc-router) that must not freeze its single
	// stateful instance for the full duration of a slow target invocation.
	// Returns an error only when the invocation could not be ACCEPTED (no
	// invoker wired, no invocation context, or in-flight cap reached) — not
	// for failures inside the target, which surface via the target's own
	// logging/ws_send.
	FunctionInvokeAsync(ctx context.Context, name string, payload []byte) error

	// HTTP operations
	HTTPFetch(ctx context.Context, method, url string, headers map[string]string, body []byte) ([]byte, error)

	// AnonFetch is HTTPFetch routed through the node's Tor client
	// (SOCKS5) so the external endpoint sees a Tor exit IP, not the
	// gateway's. Feat-11 — server-side analog of the
	// client-side proxy, for serverless functions fronting third-party
	// APIs (e.g. wallet RPC) that shouldn't expose a gateway↔upstream
	// metadata trail. NO silent fallback to direct: returns a typed
	// error envelope when Tor routing is unavailable. Exported to WASM
	// as anon_fetch and as the deprecated alias anyone_fetch.
	AnonFetch(ctx context.Context, method, url string, headers map[string]string, body []byte) ([]byte, error)

	// Context operations
	GetEnv(ctx context.Context, key string) (string, error)
	GetSecret(ctx context.Context, name string) (string, error)
	GetRequestID(ctx context.Context) string
	GetCallerWallet(ctx context.Context) string
	// GetWSClientID returns the WebSocket client ID when the function was
	// invoked via a WS connection, or empty string otherwise.
	GetWSClientID(ctx context.Context) string
	// GetCallerClaim returns a custom JWT claim's value, or empty if missing
	// or the request was not JWT-authenticated.
	GetCallerClaim(ctx context.Context, name string) string
	// GetCallerJWTSubject returns the JWT `sub` claim independent of the
	// API-key-vs-JWT wallet-resolution heuristic. Empty when the request
	// was not JWT-authenticated. Use this when a function must bind on
	// the JWT-signed identity (e.g. signup-time wallet ownership checks)
	// and the caller may ALSO present an API key. Bug #215.
	GetCallerJWTSubject(ctx context.Context) string
	// GetCallerDeviceID returns the device the caller's session is bound to,
	// or empty when it is bound to none.
	GetCallerDeviceID(ctx context.Context) string
	// GetCallerAttestation returns what the caller's device attestation
	// proved, as JSON, or empty when the caller's token carries none.
	GetCallerAttestation(ctx context.Context) string
	// GetCallerAttestationVerdict returns the gateway's verdict on the
	// caller under the namespace's attestation policy as it is now, as JSON
	// (AttestationVerdict), or empty outside a gateway.
	GetCallerAttestationVerdict(ctx context.Context) string
	// GetCallerCapability returns what the caller's capability grants, as
	// JSON, or empty when the caller came in on a credential.
	GetCallerCapability(ctx context.Context) string
	// MintCapability issues a capability for the calling function's
	// WebSocket and returns it as JSON.
	MintCapability(ctx context.Context, resource string, ttl time.Duration) (string, error)
	// RevokeCapability refuses one capability of the calling namespace,
	// named by its token.
	RevokeCapability(ctx context.Context, token string) error
	// MintStorageFetchCaps issues count fetch capabilities to read cid of the
	// calling namespace and returns them as JSON
	// {"namespace","cid","caps":[{"id","token","expires_at"}]}.
	MintStorageFetchCaps(ctx context.Context, cid string, count int, ttl time.Duration) (string, error)

	// WebRTCAdmit admits user to a WebRTC room of the calling namespace for ttl,
	// from device (any device when empty), and returns the admission as JSON
	// {"room","user_id","device_id","expires_at"}.
	WebRTCAdmit(ctx context.Context, room, user, device string, ttl time.Duration) (string, error)
	// WebRTCKick revokes user's admissions to room and closes their connection.
	WebRTCKick(ctx context.Context, room, user string) error
	// WebRTCKickDevice revokes the admission of one device of user to room and
	// closes that device's connection; the user's other devices stay.
	WebRTCKickDevice(ctx context.Context, room, user, device string) error
	// WebRTCMute stops (or resumes) the forwarding of user's audio in room.
	WebRTCMute(ctx context.Context, room, user string, muted bool) error
	// WebRTCMuteVideo stops (or resumes) the forwarding of user's video
	// (camera and screen) in room, apart from their audio.
	WebRTCMuteVideo(ctx context.Context, room, user string, muted bool) error
	// WebRTCAdmission returns what the namespace has on record for device of
	// user in room, as JSON {"room","user_id","device_id","admitted","revoked",
	// "expired","muted","video_muted","expires_at"}.
	WebRTCAdmission(ctx context.Context, room, user, device string) (string, error)

	// Job operations
	EnqueueBackground(ctx context.Context, functionName string, payload []byte) (string, error)
	ScheduleOnce(ctx context.Context, functionName string, runAt time.Time, payload []byte) (string, error)

	// Logging
	LogInfo(ctx context.Context, message string)
	LogError(ctx context.Context, message string)
}

// -----------------------------------------------------------------------------
// Deployment Types
// -----------------------------------------------------------------------------

// DeployRequest represents a request to deploy a function.
type DeployRequest struct {
	Definition *FunctionDefinition `json:"definition"`
	Source     io.Reader           `json:"-"`       // Go source code or WASM bytes
	IsWASM     bool                `json:"is_wasm"` // True if Source contains WASM bytes
}

// DeployResult represents the result of a deployment.
type DeployResult struct {
	Function *Function `json:"function"`
	WASMCID  string    `json:"wasm_cid"`
	Triggers []string  `json:"triggers,omitempty"`
}

// WebRTCController is what the WebRTC host calls act through: the namespace's
// admissions and the SFUs that hold its rooms. Every call names the namespace
// the calling function runs in; a function never names another.
type WebRTCController interface {
	Admit(ctx context.Context, namespace, room, user, device string, ttl time.Duration) (time.Time, error)
	Kick(ctx context.Context, namespace, room, user string) error
	KickDevice(ctx context.Context, namespace, room, user, device string) error
	Mute(ctx context.Context, namespace, room, user string, muted bool) error
	MuteVideo(ctx context.Context, namespace, room, user string, muted bool) error
	AdmissionOf(ctx context.Context, namespace, room, user, device string) (WebRTCAdmission, error)
}

// WebRTCAdmission is what a namespace has on record for one device of a user
// in one room, judged as a join from that device is when the namespace
// requires admission.
type WebRTCAdmission struct {
	// Admitted is set when a join from the device would be let in.
	Admitted bool
	// Revoked and Expired say why it would not; neither is set when the
	// namespace never admitted the device (or the user from every device).
	Revoked, Expired bool
	// Muted and VideoMuted are the user's forced mutes in the room.
	Muted, VideoMuted bool
	// ExpiresAt is the unix second the admission ends, 0 when not admitted.
	ExpiresAt int64
}

// CallerAttestation is what a caller's device attestation proved (feat-941):
// the platform, the app and the build it attested, and when. The gateway sets
// it from the token's signed `att` claim; nothing in a request body or header
// reaches it.
type CallerAttestation struct {
	// Platform is "ios" or "android".
	Platform string `json:"platform"`
	// AppID is "TEAMID.bundle.id" on iOS, the package name on Android.
	AppID string `json:"app_id"`
	// AppVersion is the build the platform signed (CFBundleVersion on iOS 27
	// and later, versionCode on Android); "" when it signed none.
	AppVersion string `json:"app_version"`
	// AttestedAt is when the device last attested, unix seconds.
	AttestedAt int64 `json:"attested_at"`
	// Development marks an iOS key of the App Attest development environment.
	Development bool `json:"development,omitempty"`
	// GrapheneOS marks an Android device booting GrapheneOS.
	GrapheneOS bool `json:"grapheneos,omitempty"`
	// SigningCertDigests are the lowercase-hex SHA-256 digests of the
	// certificates the attested Android app is signed with.
	SigningCertDigests []string `json:"signing_cert_digests,omitempty"`
	// OSPatchLevel is the Android OS security patch level the device
	// attested (YYYYMM), 0 when it attested none.
	OSPatchLevel int `json:"os_patch_level,omitempty"`
}

// AttestationVerdict is the gateway's verdict on a caller under its
// namespace's attestation policy as the policy is when the function asks
// (feat-965), in every mode: monitor refuses nothing at the gateway, but a
// function reads here what enforcing would decide. Read via host fn
// `get_caller_attestation_verdict`.
type AttestationVerdict struct {
	// Mode is the namespace's attestation mode: off, monitor or enforce;
	// empty when the verdict is VerdictUnavailable.
	Mode string `json:"mode,omitempty"`
	// Verdict is one of the Verdict* values.
	Verdict string `json:"verdict"`
	// Reason names the check that refused an invalid attestation or an old
	// build ("signing_cert", "package", "boot_key", "app_version", …); empty
	// otherwise.
	Reason string `json:"reason,omitempty"`
	// Message says why, in words; empty when the caller is admitted.
	Message string `json:"message,omitempty"`
}

// The verdicts of AttestationVerdict.
const (
	// VerdictAdmitted: the policy admits the caller now.
	VerdictAdmitted = "admitted"
	// VerdictAttestationRequired: the caller has not attested, or must
	// attest again — its Android attestation is past the re-attestation
	// interval, or does not show what the policy now asks.
	VerdictAttestationRequired = "attestation_required"
	// VerdictAppUpdateRequired: the build is older than the policy admits.
	VerdictAppUpdateRequired = "app_update_required"
	// VerdictAttestationInvalid: the attestation is of something the policy
	// does not admit (Reason says which check).
	VerdictAttestationInvalid = "attestation_invalid"
	// VerdictNotConfigured: the namespace's mode is off; there is no app to
	// judge against, and no caller is admitted as attested.
	VerdictNotConfigured = "not_configured"
	// VerdictUnavailable: the gateway cannot read the namespace's policy
	// right now. Retry; it says nothing about the caller.
	VerdictUnavailable = "unavailable"
)
