package hostfunctions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Orama-Network/orama/core/pkg/rqlite"
	"github.com/Orama-Network/orama/core/pkg/serverless"
	"github.com/Orama-Network/orama/core/pkg/sqlguard"
)

// dbQueryBatchTimeout caps the rqlite round-trip for a single
// `oh.DBQueryBatch` host call. Tighter than the function's invocation
// timeout (typically 15-30s) so a stalled leader doesn't burn the entire
// budget on one batched read; the WASM function still has headroom to
// do downstream work after the read returns. 10s is generous for the
// 167ms-RTT cross-region devnet cluster (one round-trip ~340ms) while
// catching genuine leader stalls quickly.
const dbQueryBatchTimeout = 10 * time.Second

// DBQuery executes a SELECT query and returns JSON-encoded results.
func (h *HostFunctions) DBQuery(ctx context.Context, query string, args []interface{}) ([]byte, error) {
	defer serverless.RecordDBCall(ctx, time.Now())
	if err := h.checkDatabaseAccess(ctx, "db_query"); err != nil {
		return nil, err
	}
	if err := sqlguard.Check(query); err != nil {
		return nil, &serverless.HostFunctionError{Function: "db_query", Cause: err}
	}

	var results []map[string]interface{}
	if err := h.db.Query(ctx, &results, query, args...); err != nil {
		return nil, &serverless.HostFunctionError{Function: "db_query", Cause: err}
	}

	data, err := json.Marshal(results)
	if err != nil {
		return nil, &serverless.HostFunctionError{Function: "db_query", Cause: fmt.Errorf("failed to marshal results: %w", err)}
	}

	return data, nil
}

// DBExecute executes an INSERT/UPDATE/DELETE query and returns affected rows.
//
// IMPORTANT: this returns 0 for BOTH "0 rows affected" AND "SQL error"
// — callers can't distinguish. That ambiguity caused bug #218 (AnChat's
// migrate function silently dropped statements). For new code, prefer
// DBExecuteV2 which returns a typed envelope.
func (h *HostFunctions) DBExecute(ctx context.Context, query string, args []interface{}) (int64, error) {
	defer serverless.RecordDBCall(ctx, time.Now())
	if err := h.checkDatabaseAccess(ctx, "db_execute"); err != nil {
		return 0, err
	}
	if err := sqlguard.Check(query); err != nil {
		return 0, &serverless.HostFunctionError{Function: "db_execute", Cause: err}
	}

	result, err := h.db.Exec(ctx, query, args...)
	if err != nil {
		return 0, &serverless.HostFunctionError{Function: "db_execute", Cause: err}
	}

	affected, _ := result.RowsAffected()
	return affected, nil
}

// dbExecuteV2Result is the JSON wire shape returned by DBExecuteV2.
type dbExecuteV2Result struct {
	RowsAffected int64  `json:"rows_affected"`
	LastInsertID int64  `json:"last_insert_id,omitempty"`
	Error        string `json:"error,omitempty"`
	// Code classifies Error — one of the rqlite.BatchCode* constants, e.g.
	// CONSTRAINT_VIOLATION (never retry) or UNAVAILABLE (retry). Set whenever
	// Error is (bugboard #267).
	Code string `json:"code,omitempty"`
}

// DBExecuteV2 is the typed equivalent of DBExecute. Returns the same shape
// regardless of success/failure so callers can distinguish "0 rows affected"
// from "SQL error" — fixing the contract gap that caused bug #218.
//
// Returns a Go error only for host-side setup failures (no DB). SQL errors
// are encoded in the JSON envelope's "error" field.
func (h *HostFunctions) DBExecuteV2(ctx context.Context, query string, args []interface{}) ([]byte, error) {
	defer serverless.RecordDBCall(ctx, time.Now())
	if err := h.checkDatabaseAccess(ctx, "db_execute_v2"); err != nil {
		return nil, err
	}
	if err := sqlguard.Check(query); err != nil {
		return nil, &serverless.HostFunctionError{Function: "db_execute_v2", Cause: err}
	}

	out := dbExecuteV2Result{}
	result, err := h.db.Exec(ctx, query, args...)
	if err != nil {
		out.Error = err.Error()
		out.Code = rqlite.ClassifyBatchError(err)
		buf, mErr := json.Marshal(out)
		if mErr != nil {
			return nil, &serverless.HostFunctionError{Function: "db_execute_v2", Cause: mErr}
		}
		return buf, nil
	}
	if result != nil {
		out.RowsAffected, _ = result.RowsAffected()
		out.LastInsertID, _ = result.LastInsertId()
	}
	buf, mErr := json.Marshal(out)
	if mErr != nil {
		return nil, &serverless.HostFunctionError{Function: "db_execute_v2", Cause: mErr}
	}
	return buf, nil
}

// dbQueryV2Result is the JSON wire shape returned by DBQueryV2.
type dbQueryV2Result struct {
	Rows  []map[string]interface{} `json:"rows"`
	Error string                   `json:"error,omitempty"`
	// Code classifies Error — one of the rqlite.BatchCode* constants. Set
	// whenever Error is (bugboard #267).
	Code string `json:"code,omitempty"`
}

// DBQueryV2 is the typed equivalent of DBQuery. Distinguishes "empty
// result set" from "query failed" via the "error" field.
func (h *HostFunctions) DBQueryV2(ctx context.Context, query string, args []interface{}) ([]byte, error) {
	defer serverless.RecordDBCall(ctx, time.Now())
	if err := h.checkDatabaseAccess(ctx, "db_query_v2"); err != nil {
		return nil, err
	}
	if err := sqlguard.Check(query); err != nil {
		return nil, &serverless.HostFunctionError{Function: "db_query_v2", Cause: err}
	}

	out := dbQueryV2Result{Rows: []map[string]interface{}{}}
	if err := h.db.Query(ctx, &out.Rows, query, args...); err != nil {
		out.Error = err.Error()
		out.Code = rqlite.ClassifyBatchError(err)
		// Reset rows to non-nil empty on error so callers get a stable shape.
		out.Rows = []map[string]interface{}{}
	}
	buf, mErr := json.Marshal(out)
	if mErr != nil {
		return nil, &serverless.HostFunctionError{Function: "db_query_v2", Cause: mErr}
	}
	return buf, nil
}

// dbTransactionRequest is the WASM-side shape for db_transaction input.
type dbTransactionRequest struct {
	Ops []rqlite.BatchOp `json:"ops"`
}

// DBTransaction executes ops as a single atomic batch.
// Input is JSON: {"ops": [{"kind":"exec"|"query","sql":"...","args":[...]}, ...]}
// Output is JSON: BatchResult — caller checks `committed` to know if writes landed.
//
// Returns an error only for setup/validation problems. A rolled-back batch is
// communicated via committed=false in the returned JSON; that's not a Go error.
func (h *HostFunctions) DBTransaction(ctx context.Context, opsJSON []byte) ([]byte, error) {
	defer serverless.RecordDBCall(ctx, time.Now())
	if err := h.checkDatabaseAccess(ctx, "db_transaction"); err != nil {
		return nil, err
	}
	var req dbTransactionRequest
	if err := json.Unmarshal(opsJSON, &req); err != nil {
		return nil, &serverless.HostFunctionError{
			Function: "db_transaction",
			Cause:    fmt.Errorf("invalid json: %w", err),
		}
	}
	if len(req.Ops) == 0 {
		return nil, &serverless.HostFunctionError{
			Function: "db_transaction",
			Cause:    fmt.Errorf("ops required"),
		}
	}
	if len(req.Ops) > rqlite.MaxBatchOps {
		return nil, &serverless.HostFunctionError{
			Function: "db_transaction",
			Cause:    fmt.Errorf("too many ops: max %d", rqlite.MaxBatchOps),
		}
	}

	if err := checkGuestOps("db_transaction", req.Ops); err != nil {
		return nil, err
	}

	res, err := h.db.Batch(ctx, req.Ops)
	// Always return the structured result, even on rollback — caller wants the
	// per-op detail to know which op failed.
	if res == nil {
		// Unrecoverable setup failure (no native conn). Surface as Go error.
		return nil, &serverless.HostFunctionError{Function: "db_transaction", Cause: err}
	}
	// A rollback attributable to a statement is described by that op's Results
	// entry, and the caller reads `committed`. But a failure that reached no
	// statement leaves every entry zero-valued, so committed=false would be the
	// ONLY signal — indistinguishable from "op 0 failed" (bugboard #175).
	// Guarantee the envelope always carries a reason when nothing else does.
	if err != nil && res.Error == "" && !opResultsCarryError(res.Results) {
		res.Error = err.Error()
		res.Code = rqlite.ClassifyBatchError(err)
	}
	out, mErr := json.Marshal(res)
	if mErr != nil {
		return nil, &serverless.HostFunctionError{
			Function: "db_transaction",
			Cause:    fmt.Errorf("marshal result: %w", mErr),
		}
	}
	// Rollback errors are encoded in the JSON; do NOT propagate as Go error.
	// The caller inspects committed / failed_index / error instead.
	return out, nil
}

// checkGuestOps applies the guest SQL guard to every op of a batched host
// call, before anything runs. One helper for every batched call: the guard
// was once missing from one of them (bugboard #425).
func checkGuestOps(fn string, ops []rqlite.BatchOp) error {
	for i, op := range ops {
		if err := sqlguard.Check(op.SQL); err != nil {
			return &serverless.HostFunctionError{Function: fn, Cause: fmt.Errorf("op %d: %w", i, err)}
		}
	}
	return nil
}

// opResultsCarryError reports whether any op already explains the failure, so
// the batch-level reason is only added when nothing else describes it.
func opResultsCarryError(results []rqlite.OpResult) bool {
	for _, r := range results {
		if r.Error != "" {
			return true
		}
	}
	return false
}

// dbQueryBatchRequest is the WASM-side shape for db_query_batch input.
// Each op MUST be Kind=BatchOpQuery; mixing exec is rejected at the
// rqlite layer.
type dbQueryBatchRequest struct {
	Ops []rqlite.BatchOp `json:"ops"`
	// Consistency is the optional rqlite read level for this batch.
	// "" / "weak" (default): leader-routed; misses a write only in the moment
	// of a leadership change. "none": fast LOCAL read (~1ms, no leader hop) —
	// ONLY safe for reads that don't need read-your-own-writes freshness (see
	// rqlite.ReadConsistency / bug #235; feat-6). "linearizable": observes
	// every write committed before the read began, at the cost of one more
	// leader-to-quorum round trip; it fails rather than degrade (feat-802).
	Consistency string `json:"consistency,omitempty"`
	// Freshness (#1021) is an optional Go duration string ("2s", "500ms"). When
	// set with consistency="none", the local read is REJECTED by rqlite if this
	// node is staler than the bound, instead of silently returning stale rows.
	// Empty (default) preserves prior behavior. Setting it with any consistency
	// other than "none" is a boundary error.
	Freshness string `json:"freshness,omitempty"`
	// FreshnessStrict (#1021) maps to rqlite's freshness_strict: also reject the
	// read if this node has no leader (not just when stale). Ignored unless
	// Freshness is set.
	FreshnessStrict bool `json:"freshness_strict,omitempty"`
}

// freshClient is the optional capability a Client exposes when it can serve a
// none-read with an explicit freshness bound (#1021). The production
// *rqlite.client implements it; bare test mocks don't. Kept OFF the
// rqlite.Client interface alongside batchQueryConsistencyClient so the
// freshness path doesn't churn every mock.
type freshClient interface {
	BatchQueryFresh(ctx context.Context, ops []rqlite.BatchOp, freshness time.Duration, strict bool) ([]rqlite.OpResult, error)
}

// batchQueryConsistencyClient is the optional capability a Client exposes when
// it can serve reads at an explicit consistency level. The production
// *rqlite.client implements it; bare test mocks don't. Kept OFF the
// rqlite.Client interface so the none-read path doesn't churn every mock.
type batchQueryConsistencyClient interface {
	BatchQueryConsistency(ctx context.Context, ops []rqlite.BatchOp, rc rqlite.ReadConsistency) ([]rqlite.OpResult, error)
}

// resolveBatchQuery runs the batched read at the requested consistency and
// optional freshness bound. Empty or "weak" → the default leader-routed read.
// "none" → a fast local read via the consistency-capable client (degrading to
// weak only when the client can't serve an explicit level — weak is always
// correct). When freshness is set (#1021) the none-read is routed to
// BatchQueryFresh so rqlite rejects it if this node is staler than the bound.
// "linearizable" → the consistency-capable client's linearizable read, and an
// error when the client has none: no weaker level gives its guarantee
// (feat-802). All invalid combinations are rejected here at the boundary
// rather than silently downgraded.
func (h *HostFunctions) resolveBatchQuery(ctx context.Context, ops []rqlite.BatchOp, consistency, freshness string, strict bool) ([]rqlite.OpResult, error) {
	// Parse + validate freshness at the boundary before any DB work.
	freshDur, err := parseFreshness(consistency, freshness)
	if err != nil {
		return nil, err
	}
	switch consistency {
	case "", string(rqlite.ReadConsistencyWeak):
		return h.db.BatchQuery(ctx, ops)
	case string(rqlite.ReadConsistencyNone):
		if freshDur > 0 {
			if ext, ok := h.db.(freshClient); ok {
				return ext.BatchQueryFresh(ctx, ops, freshDur, strict)
			}
			// A freshness bound was requested but the client can't enforce it.
			// Degrading to a non-freshness read would silently drop the
			// staleness guarantee the caller asked for — reject instead.
			return nil, fmt.Errorf("freshness requested but client cannot enforce it (no freshness capability)")
		}
		if ext, ok := h.db.(batchQueryConsistencyClient); ok {
			return ext.BatchQueryConsistency(ctx, ops, rqlite.ReadConsistencyNone)
		}
		return h.db.BatchQuery(ctx, ops)
	case string(rqlite.ReadConsistencyLinearizable):
		ext, ok := h.db.(batchQueryConsistencyClient)
		if !ok {
			return nil, fmt.Errorf("consistency \"linearizable\" requested but this gateway's database client cannot serve it")
		}
		return ext.BatchQueryConsistency(ctx, ops, rqlite.ReadConsistencyLinearizable)
	default:
		return nil, fmt.Errorf("invalid consistency %q (allowed: \"none\", \"weak\", \"linearizable\")", consistency)
	}
}

const (
	// minFreshness is the smallest freshness bound accepted. Sub-millisecond
	// bounds are meaningless for a cross-node read, and time.Duration.String()
	// renders them with a non-ASCII "µs" unit that must never reach the request
	// target — reject them at the boundary.
	minFreshness = time.Millisecond
	// maxFreshness caps the bound; anything larger is effectively "no bound", so
	// the caller should omit freshness instead of passing a huge value.
	maxFreshness = 24 * time.Hour
)

// parseFreshness validates and parses the optional freshness bound at the host
// boundary. Empty freshness → 0 (no bound, unchanged behavior). A non-empty
// freshness is only valid with consistency="none" and must fall within
// [minFreshness, maxFreshness]. Returns an actionable error otherwise.
func parseFreshness(consistency, freshness string) (time.Duration, error) {
	if freshness == "" {
		return 0, nil
	}
	if consistency != string(rqlite.ReadConsistencyNone) {
		return 0, fmt.Errorf("freshness requires consistency=\"none\"")
	}
	d, err := time.ParseDuration(freshness)
	if err != nil {
		return 0, fmt.Errorf("invalid freshness %q: %w", freshness, err)
	}
	if d < minFreshness {
		return 0, fmt.Errorf("invalid freshness %q: minimum is %s", freshness, minFreshness)
	}
	if d > maxFreshness {
		return 0, fmt.Errorf("invalid freshness %q: maximum is %s", freshness, maxFreshness)
	}
	return d, nil
}

// dbQueryBatchResult is the JSON wire shape returned to WASM callers.
// `Results` is one entry per input op, in the same order. Per-op errors
// are surfaced in `error`; transport/validation errors come back as a
// Go error from the host fn.
//
// StaleRejected (#1021) is true ONLY when a freshness-bound none-read was
// rejected because this node was staler than the bound. It is a structured,
// WASM-visible signal — distinct from an opaque host error — so the caller can
// fall back to a leader-routed read. When true, Results is empty and
// StaleDetail carries rqlite's reason (credential-free).
type dbQueryBatchResult struct {
	Results       []rqlite.OpResult `json:"results"`
	StaleRejected bool              `json:"stale_rejected,omitempty"`
	StaleDetail   string            `json:"stale_detail,omitempty"`
	// Error / Code carry a batch-level failure (bugboard #175). Populated by
	// the ABI wrapper's rejection envelope; empty on the success path.
	Error string `json:"error,omitempty"`
	Code  string `json:"code,omitempty"`
}

// DBQueryBatch runs N SELECTs in one round-trip via RQLite's /db/query
// bulk endpoint. Designed for read-heavy functions that gather state
// from multiple tables before doing work (e.g. anchat's message-create
// reads auth + participants + devices = 7-10 SELECTs).
//
// Wire shapes:
//
//	in:  {"ops": [{"sql":"...","args":[...]}, ...]}
//	out: {"results": [{"kind":"query","rows":[...],"error":""}, ...]}
//
// Per-query errors are reported in the per-op `error` field; the host
// fn only returns a Go error on setup/validation/transport failures.
// Kind is auto-set to "query" on input — exec ops are rejected, since
// mixing kinds in a query batch is meaningless and would silently
// drop the writes (see bugboard #270).
//
// Empirical baseline on devnet's cross-region cluster (167ms RTT to
// leader): 10 sequential DBQuery host calls = ~3.5s; one DBQueryBatch
// with 10 statements = ~340ms. 10× speedup.
func (h *HostFunctions) DBQueryBatch(ctx context.Context, opsJSON []byte) ([]byte, error) {
	defer serverless.RecordDBCall(ctx, time.Now())
	if err := h.checkDatabaseAccess(ctx, "db_query_batch"); err != nil {
		return nil, err
	}
	var req dbQueryBatchRequest
	if err := json.Unmarshal(opsJSON, &req); err != nil {
		return nil, &serverless.HostFunctionError{
			Function: "db_query_batch",
			Cause:    fmt.Errorf("invalid json: %w", err),
		}
	}
	if len(req.Ops) == 0 {
		return nil, &serverless.HostFunctionError{
			Function: "db_query_batch",
			Cause:    fmt.Errorf("ops required"),
		}
	}
	if len(req.Ops) > rqlite.MaxBatchOps {
		return nil, &serverless.HostFunctionError{
			Function: "db_query_batch",
			Cause:    fmt.Errorf("too many ops: max %d", rqlite.MaxBatchOps),
		}
	}
	// Force kind=query for every op. Callers can omit the field; this
	// makes the wire format more ergonomic AND prevents accidental exec
	// ops from being silently dropped by the rqlite-side validator.
	for i := range req.Ops {
		req.Ops[i].Kind = rqlite.BatchOpQuery
	}

	if err := checkGuestOps("db_query_batch", req.Ops); err != nil {
		return nil, err
	}

	// Explicit batch-level deadline. The caller's ctx already carries the
	// function's invocation timeout (typically 15-30s), but we want a
	// tighter cap on the rqlite round-trip itself so a stalled leader
	// doesn't burn the entire invocation budget on one batched query.
	// Leaves headroom for downstream WASM work after the read returns.
	batchCtx, cancel := context.WithTimeout(ctx, dbQueryBatchTimeout)
	defer cancel()

	results, err := h.resolveBatchQuery(batchCtx, req.Ops, req.Consistency, req.Freshness, req.FreshnessStrict)
	if err != nil {
		// A freshness rejection is a recoverable, WASM-visible signal — return a
		// structured envelope (stale_rejected=true) so the caller can fall back
		// to a leader-routed read, instead of an opaque host error.
		if errors.Is(err, rqlite.ErrFreshnessViolation) {
			out, mErr := json.Marshal(dbQueryBatchResult{
				Results:       []rqlite.OpResult{},
				StaleRejected: true,
				StaleDetail:   err.Error(),
			})
			if mErr != nil {
				return nil, &serverless.HostFunctionError{
					Function: "db_query_batch",
					Cause:    fmt.Errorf("marshal stale-rejected result: %w", mErr),
				}
			}
			return out, nil
		}
		return nil, &serverless.HostFunctionError{Function: "db_query_batch", Cause: err}
	}

	out, mErr := json.Marshal(dbQueryBatchResult{Results: results})
	if mErr != nil {
		return nil, &serverless.HostFunctionError{
			Function: "db_query_batch",
			Cause:    fmt.Errorf("marshal result: %w", mErr),
		}
	}
	return out, nil
}

// execAndPublishResult is the JSON wire shape returned to WASM callers.
type execAndPublishResult struct {
	Results      []rqlite.OpResult `json:"results"`
	Committed    bool              `json:"committed"`
	FailedIndex  int               `json:"failed_index,omitempty"`
	Seq          int64             `json:"seq,omitempty"`
	Published    bool              `json:"published,omitempty"`
	PublishError string            `json:"publish_error,omitempty"`
	// Error / Code carry a batch-level failure that belongs to no single op —
	// a transport fault, a lost leader, an expired deadline (bugboard #175).
	// Without them a batch that never reached a statement returned
	// committed=false with nothing else set, which reads as "op 0 failed".
	Error string `json:"error,omitempty"`
	Code  string `json:"code,omitempty"`
}

// ExecAndPublish runs ops atomically (with a seq increment in the same batch)
// and, if committed, publishes data with `{{seq}}` substituted for the
// assigned per-namespace sequence number.
//
// Failure modes (each communicated in the JSON, not as Go error):
//   - Rollback: committed=false, failed_index points to the failing user op
//   - Publish failed but commit succeeded: committed=true, published=false,
//     publish_error is set. Writes are durable; caller can retry the publish.
//   - Both succeeded: committed=true, published=true.
//
// Returns a Go error only on setup failures (no DB, bad JSON, no namespace).
func (h *HostFunctions) ExecAndPublish(
	ctx context.Context, opsJSON []byte, topic string, dataTemplate []byte,
) ([]byte, error) {
	defer serverless.RecordDBCall(ctx, time.Now())
	if err := h.checkDatabaseAccess(ctx, "exec_and_publish"); err != nil {
		return nil, err
	}
	if h.pubsub == nil {
		return nil, &serverless.HostFunctionError{
			Function: "exec_and_publish",
			Cause:    fmt.Errorf("pubsub not available"),
		}
	}
	if topic == "" {
		return nil, &serverless.HostFunctionError{
			Function: "exec_and_publish",
			Cause:    fmt.Errorf("topic required"),
		}
	}

	// The namespace is the invocation's — server-trusted, and present:
	// checkDatabaseAccess refused the call otherwise.
	ns := h.currentInvocationContext(ctx).Namespace

	var req dbTransactionRequest
	if err := json.Unmarshal(opsJSON, &req); err != nil {
		return nil, &serverless.HostFunctionError{
			Function: "exec_and_publish",
			Cause:    fmt.Errorf("invalid ops json: %w", err),
		}
	}
	if len(req.Ops) > rqlite.MaxBatchOps {
		return nil, &serverless.HostFunctionError{
			Function: "exec_and_publish",
			Cause:    fmt.Errorf("too many ops: max %d", rqlite.MaxBatchOps),
		}
	}
	// The same guard every other database host function applies, before any
	// side effect: without it this was a way around sqlguard to the platform's
	// auth tables (bugboard #425).
	if err := checkGuestOps("exec_and_publish", req.Ops); err != nil {
		return nil, err
	}

	// exec_and_publish reaches the same shared gossipsub publish path as
	// pubsub_publish, so it must charge the same per-invocation publish budget
	// (it publishes exactly one wake-up message on commit). Checked before the
	// write so an over-budget call has no side effects.
	if n := serverless.AddPublishCount(ctx, 1); n > maxPublishesPerInvocation {
		return nil, &serverless.HostFunctionError{
			Function: "exec_and_publish",
			Cause:    fmt.Errorf("publish budget exceeded (max %d per invocation)", maxPublishesPerInvocation),
		}
	}

	batchRes, seq, batchErr := h.db.BatchWithSeq(ctx, ns, req.Ops)
	out := execAndPublishResult{Results: []rqlite.OpResult{}}
	if batchRes != nil {
		out.Results = batchRes.Results
		out.Committed = batchRes.Committed
		out.FailedIndex = batchRes.FailedIndex
		out.Error = batchRes.Error
		out.Code = batchRes.Code
	}

	// On rollback or pre-publish error, return without publishing.
	if batchErr != nil || !out.Committed {
		// A rollback attributable to a statement is already described by that
		// op's Results entry, so the Go error adds nothing there. But a failure
		// that reached no statement at all — transport, lost leader, deadline —
		// leaves every Results entry zero-valued, and reporting only
		// committed=false told the caller nothing about why (bugboard #175).
		// Record the reason whenever nothing else carries it.
		if batchErr != nil && out.Error == "" {
			out.Error = batchErr.Error()
			out.Code = rqlite.ClassifyBatchError(batchErr)
		}
		buf, mErr := json.Marshal(out)
		if mErr != nil {
			return nil, &serverless.HostFunctionError{Function: "exec_and_publish", Cause: mErr}
		}
		return buf, nil
	}

	out.Seq = seq

	// Substitute {{seq}} in the data template, then publish.
	finalData := bytes.ReplaceAll(
		dataTemplate,
		[]byte("{{seq}}"),
		[]byte(strconv.FormatInt(seq, 10)),
	)
	if err := h.pubsub.Publish(ctx, topic, finalData); err != nil {
		out.PublishError = err.Error()
	} else {
		out.Published = true
	}

	buf, mErr := json.Marshal(out)
	if mErr != nil {
		return nil, &serverless.HostFunctionError{Function: "exec_and_publish", Cause: mErr}
	}
	return buf, nil
}
