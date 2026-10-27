package serverless

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
	"go.uber.org/zap"

	"github.com/Orama-Network/orama/core/pkg/anonproxy"
	"github.com/Orama-Network/orama/core/pkg/httputil"
	"github.com/Orama-Network/orama/core/pkg/rqlite"
	"github.com/Orama-Network/orama/core/pkg/serverless/cache"
	"github.com/Orama-Network/orama/core/pkg/serverless/execution"
)

// persistentFriendlyProcExit is our override of WASI's `proc_exit`.
//
// Standard wazero proc_exit:
//
//	mod.CloseWithExitCode(ctx, exitCode)  // ← invalidates the module
//	panic(sys.NewExitError(exitCode))
//
// This breaks TinyGo command-mode (target=wasi) functions that we want
// to keep alive past `_start` for a persistent-instance lifecycle —
// `_start` ends with `proc_exit(0)`, which kills the module and makes
// the function's other exports (ws_open, ws_frame, ws_close,
// orama_alloc) uncallable.
//
// Override semantics:
//
//   - exitCode == 0: panic with ExitError(0) but DO NOT close the
//     module. This is TinyGo's "_start completed cleanly" signal; we
//     want the module to stay live so the persistent instance can
//     receive ws_open / ws_frame frames.
//   - exitCode != 0: preserve standard WASI behavior — close + panic.
//     A non-zero exit is a genuine application-signaled failure; we
//     want it to behave exactly as upstream WASI does.
//
// The panic is mandatory in both cases — wasm code following proc_exit
// is conventionally `unreachable` (LLVM emits this after exit calls),
// and not panicking would let it execute. The CALLER (our
// `InstantiatePersistent`) catches the ExitError and special-cases
// code 0 as success.
//
// Affects ALL functions (stateless + persistent) on this runtime, but
// safe for stateless because the stateless path closes its own module
// after each invocation regardless.
func persistentFriendlyProcExit(ctx context.Context, mod api.Module, exitCode uint32) {
	if exitCode != 0 {
		_ = mod.CloseWithExitCode(ctx, exitCode)
	}
	panic(sys.NewExitError(exitCode))
}

// Ensure Engine implements FunctionExecutor interface.
var _ FunctionExecutor = (*Engine)(nil)

// Engine is the core WASM execution engine using wazero.
// It manages compiled module caching and function execution.
type Engine struct {
	runtime      wazero.Runtime
	config       *Config
	registry     FunctionRegistry
	hostServices HostServices
	logger       *zap.Logger

	// Module cache
	moduleCache *cache.ModuleCache

	// Execution components
	executor  *execution.Executor
	lifecycle *execution.ModuleLifecycle

	// Invocation logger for metrics/debugging
	invocationLogger InvocationLogger

	// logQueue moves invocation telemetry writes OFF the reply critical path
	// (bugboard feat-27). Non-nil only when invocationLogger is set; logInvocation
	// enqueues into it instead of calling invocationLogger.Log synchronously.
	logQueue *invocationLogQueue

	// Rate limiter
	rateLimiter RateLimiter

	// reactorPool pre-warms one-shot instances for reactor-mode functions so
	// their ~550ms TinyGo _initialize cold-start runs ahead of the request
	// instead of on it (bugboard #898). Dormant unless a function is built
	// reactor-mode (exports handle + _initialize, no _start); command-mode
	// functions never touch it. Each warmed instance serves exactly one call
	// and is then closed (isolation identical to fresh-instance-per-call).
	reactorPool *reactorPool
}

// Reactor pre-warm pool sizing (bugboard #898).
const (
	reactorWarmTarget = 2  // ready instances kept per reactor module
	reactorMaxModules = 64 // distinct reactor module pools (LRU-evicted)
)

// InvocationLogger logs function invocations (optional).
type InvocationLogger interface {
	Log(ctx context.Context, inv *InvocationRecord) error
}

// InvocationRecord represents a logged invocation.
type InvocationRecord struct {
	ID           string           `json:"id"`
	FunctionID   string           `json:"function_id"`
	RequestID    string           `json:"request_id"`
	TriggerType  TriggerType      `json:"trigger_type"`
	CallerWallet string           `json:"caller_wallet,omitempty"`
	InputSize    int              `json:"input_size"`
	OutputSize   int              `json:"output_size"`
	StartedAt    time.Time        `json:"started_at"`
	CompletedAt  time.Time        `json:"completed_at"`
	DurationMS   int64            `json:"duration_ms"`
	Status       InvocationStatus `json:"status"`
	ErrorMessage string           `json:"error_message,omitempty"`
	MemoryUsedMB float64          `json:"memory_used_mb"`
	Logs         []LogEntry       `json:"logs,omitempty"`
}

// RateLimiter is the legacy single-bucket rate-limit interface, kept for
// backward compatibility with TokenBucketLimiter. New limiters should
// implement TieredRateLimiter as well — the engine prefers the richer path
// when available via type assertion.
type RateLimiter interface {
	Allow(ctx context.Context, key string) (bool, error)
}

// TieredRateLimiter is the rich interface that lets the engine pass
// per-(namespace, function, wallet, ip) context for layered enforcement.
// MultiTierLimiter implements both this and the legacy RateLimiter.
type TieredRateLimiter interface {
	AllowRequest(ctx context.Context, req RateLimitRequest) (Decision, error)
}

// EngineOption configures the Engine.
type EngineOption func(*Engine)

// WithInvocationLogger sets the invocation logger.
func WithInvocationLogger(logger InvocationLogger) EngineOption {
	return func(e *Engine) {
		e.invocationLogger = logger
	}
}

// WithRateLimiter sets the rate limiter.
func WithRateLimiter(limiter RateLimiter) EngineOption {
	return func(e *Engine) {
		e.rateLimiter = limiter
	}
}

// NewEngine creates a new WASM execution engine.
func NewEngine(cfg *Config, registry FunctionRegistry, hostServices HostServices, logger *zap.Logger, opts ...EngineOption) (*Engine, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	cfg.ApplyDefaults()

	// Create wazero runtime with compilation cache
	maxPages := uint32(cfg.MaxMemoryLimitMB * 16) // 1 MB = 16 WASM pages
	if maxPages == 0 {
		maxPages = 256 * 16
	}
	runtimeConfig := wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true).
		WithMemoryLimitPages(maxPages)

	runtime := wazero.NewRuntimeWithConfig(context.Background(), runtimeConfig)

	// Instantiate WASI with a CUSTOM `proc_exit` that does NOT close the
	// module on exit code 0 (#240/#249 follow-up #5).
	//
	// Background: TinyGo command-mode `_start` (target=wasi) runs the
	// runtime init, calls `main()`, then calls `proc_exit(0)`. Wazero's
	// stock proc_exit then calls `mod.CloseWithExitCode(0)` which
	// invalidates the module — subsequent calls to `ws_open`, `ws_frame`,
	// etc. return `ExitError(0)`. That breaks every TinyGo
	// command-mode persistent function (anchat's rpc-router being the
	// canary).
	//
	// Fix: override proc_exit. For exit code 0 (the "clean termination"
	// case TinyGo emits at the end of `_start`), we panic with
	// ExitError(0) but DO NOT close the module — letting the caller of
	// `_start` see the ExitError as a "_start completed" signal while
	// the module's exports stay live for ws_open/frame/close.
	//
	// For non-zero exit codes (genuine application-signaled errors), we
	// preserve standard WASI behavior: close the module AND panic. This
	// keeps `proc_exit(N != 0)` semantics intact.
	//
	// Override pattern documented in wazero v1.11+ at
	// imports/wasi_snapshot_preview1/wasi.go:111-127:
	//
	//   wasiBuilder := r.NewHostModuleBuilder(ModuleName)
	//   wasi_snapshot_preview1.NewFunctionExporter().ExportFunctions(wasiBuilder)
	//   // Subsequent calls to NewFunctionBuilder override built-in exports.
	//   wasiBuilder.NewFunctionBuilder().WithFunc(...).Export("proc_exit")
	//
	// This is the *only* way to bypass the close-on-exit behavior in
	// wazero — there's no per-instance flag and no global toggle.
	wasiBuilder := runtime.NewHostModuleBuilder(wasi_snapshot_preview1.ModuleName)
	wasi_snapshot_preview1.NewFunctionExporter().ExportFunctions(wasiBuilder)
	wasiBuilder.NewFunctionBuilder().
		WithFunc(persistentFriendlyProcExit).
		Export("proc_exit")
	if _, err := wasiBuilder.Instantiate(context.Background()); err != nil {
		panic("serverless: failed to instantiate WASI with custom proc_exit: " + err.Error())
	}

	engine := &Engine{
		runtime:      runtime,
		config:       cfg,
		registry:     registry,
		hostServices: hostServices,
		logger:       logger,
		moduleCache:  cache.NewModuleCache(cfg.ModuleCacheSize, logger),
		executor:     execution.NewExecutor(runtime, logger, cfg.MaxConcurrentExecutions),
		lifecycle:    execution.NewModuleLifecycle(runtime, logger),
	}

	// Reactor pre-warm pool (bugboard #898). The warmer reuses the engine's
	// compile cache + runtime; warmed instances are one-shot and isolated.
	engine.reactorPool = newReactorPool(engine.instantiateReactorInstance, reactorWarmTarget, reactorMaxModules, logger)

	// Apply options
	for _, opt := range opts {
		opt(engine)
	}

	// Start the async telemetry queue once we know whether a logger was wired
	// in. Invocation logging is now OFF the reply critical path (bugboard
	// feat-27): logInvocation enqueues, a single worker drains and writes with
	// its own context. Without a logger there's nothing to queue.
	if engine.invocationLogger != nil {
		engine.logQueue = newInvocationLogQueue(engine.invocationLogger, logger)
	}

	// Register host functions
	if err := engine.registerHostModule(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to register host module: %w", err)
	}

	return engine, nil
}

// slowInvokeThreshold returns the wall-clock duration above which Execute
// emits a structured "slow invocation" warning with per-phase breakdown.
// Sourced from config (SlowInvokeThresholdMs) so a cluster under
// investigation can lower it to surface the sub-second cold-start floor that
// the 5s default hides (bugboard #27). Defaults to 5s when unset.
func (e *Engine) slowInvokeThreshold() time.Duration {
	if e.config != nil && e.config.SlowInvokeThresholdMs > 0 {
		return time.Duration(e.config.SlowInvokeThresholdMs) * time.Millisecond
	}
	return defaultSlowInvokeThresholdMs * time.Millisecond
}

// Execute runs a function with the given input and returns the output.
//
// Emits per-phase timing telemetry when total duration exceeds
// slowInvokeThreshold — bugboard #24 diagnostic. Without this, slow
// invocations only surfaced as opaque "RPC timeout after 30s" at the
// WS handler, with no way to tell whether the sink was rate-limit
// checks, module compile, or WASM execution itself.
func (e *Engine) Execute(ctx context.Context, fn *Function, input []byte, invCtx *InvocationContext) ([]byte, error) {
	if fn == nil {
		return nil, &ValidationError{Field: "function", Message: "cannot be nil"}
	}

	invCtx = EnsureInvocationContext(invCtx, fn)
	startTime := time.Now()
	// Per-phase timestamps for the slow-invoke log (bugboard #24
	// diagnostic). Zero values mean the phase was never entered, which
	// itself is signal (e.g. ratelimitMs=0 with totalMs=30000 means we
	// blocked entirely in module-load or execution).
	var (
		ratelimitDoneAt time.Time
		moduleLoadedAt  time.Time
		executeDoneAt   time.Time
	)

	// Check rate limit. Prefer the tiered path when the limiter supports it
	// — that gives per-(ns, fn, wallet, ip) enforcement with retry-after.
	// Fall back to the legacy single-bucket interface otherwise.
	if e.rateLimiter != nil {
		if tl, ok := e.rateLimiter.(TieredRateLimiter); ok {
			req := RateLimitRequest{
				Namespace: invCtx.Namespace,
				Function:  invCtx.FunctionName,
				Wallet:    invCtx.CallerWallet,
				Key:       invCtx.CallerKey,
				IP:        invCtx.CallerIP,
				Override:  fn.RateLimit,
			}
			d, err := tl.AllowRequest(ctx, req)
			if err != nil {
				// A limiter that cannot judge the call does not let it past
				// a ceiling the namespace may have set.
				return nil, err
			}
			if !d.Allowed {
				return nil, &RateLimitedError{Scope: d.Scope, RetryAfter: d.RetryAfter}
			}
		} else {
			allowed, err := e.rateLimiter.Allow(ctx, "global")
			if err != nil {
				e.logger.Warn("Rate limiter error", zap.Error(err))
			} else if !allowed {
				return nil, ErrRateLimited
			}
		}
	}

	ratelimitDoneAt = time.Now()

	// Create timeout context
	execCtx, cancel := CreateTimeoutContext(ctx, fn, e.config.MaxTimeoutSeconds)
	defer cancel()

	// Attach a fresh per-invocation LogBuffer to the ctx that wazero
	// passes through to host-fn callbacks. host.LogInfo / host.LogError
	// extract this buffer and append to it instead of writing to the
	// HostFunctions singleton slice — which would cross-contaminate
	// concurrent invocations (bugboard #108: push-fanout's invocation
	// record was capturing rpc-router and message-push-handler log
	// lines because every WASM call shared one h.logs slice).
	logBuf := NewLogBuffer()
	execCtx = WithLogBuffer(execCtx, logBuf)

	// Attach this invocation's InvocationContext to execCtx so host
	// functions resolve identity/namespace from ctx instead of the
	// process-wide HostFunctions singleton. Closes the stateless race
	// that bugboard #348 surfaced via AnChat's message-push-handler:
	// two concurrent pubsub-triggered invocations would overwrite each
	// other's singleton invCtx, and the loser's push_send_v2 call would
	// read either a cross-tenant namespace (silent identity leak) or a
	// nil singleton ("no namespace in invocation context" error — the
	// observable empty-envelope symptom AnChat reported).
	//
	// This is the only source. There used to be a field on the shared
	// HostFunctions as well, set before each execution and cleared after;
	// the two invocations above are exactly what overwrote it.
	execCtx = WithInvocationContext(execCtx, invCtx)

	// Fresh per-invocation pubsub publish counter so the pubsub host
	// functions can cap how many messages one invocation floods onto the
	// shared gossipsub router (no WASM fuel metering exists; the rate limiter
	// gates invocation frequency, not per-invocation host-call volume).
	execCtx = WithPublishCounter(execCtx)
	// Likewise for the webrtc_* host calls, each of which writes the namespace's
	// database and calls every SFU.
	execCtx = WithWebRTCCounter(execCtx)
	// And for the gateway log lines its host calls write (bugboard #424).
	execCtx, logBudget := WithHostLogBudget(execCtx)
	// And an empty function_invoke_last_error slot (bugboard #263): it is new
	// for every invocation, so a reused reactor instance cannot report a
	// previous request's error.
	execCtx = WithInvokeErrorSlot(execCtx)
	defer logBudget.Summarize(e.logger, zap.String("namespace", invCtx.Namespace),
		zap.String("function", fn.Name), zap.String("request_id", invCtx.RequestID))

	// Raw-HTTP-response mode (bugboard #835). Only RawHTTPResponse functions
	// get a collector attached — set_http_response is a validated no-op for
	// every other function (no collector → host call returns an error). The
	// collector rides execCtx so concurrent invocations never cross-write,
	// matching the publish-counter / log-buffer per-call model.
	if fn.RawHTTPResponse {
		execCtx = WithRawHTTPCollector(execCtx)
	}

	// Get compiled module (from cache or compile)
	module, err := e.getOrCompileModule(execCtx, fn.WASMCID)
	if err != nil {
		e.logInvocation(ctx, fn, invCtx, logBuf, startTime, 0, InvocationStatusError, err)
		e.logSlowInvocation(invCtx, startTime, ratelimitDoneAt, moduleLoadedAt, executeDoneAt, 0, "module-load-failed", err)
		return nil, &ExecutionError{FunctionName: fn.Name, RequestID: invCtx.RequestID, Cause: err}
	}
	moduleLoadedAt = time.Now()

	// Attach a collector so ExecuteModule reports how long instantiate (TinyGo
	// _start cold-start) took, letting the slow-invoke diagnostic split the
	// execute phase into cold-start vs handler work (bugboard #27).
	execCtx, instTiming := execution.WithInstantiateTiming(execCtx)
	// Reactor-mode functions (bugboard #898) are served from the pre-warm pool
	// via handle() — they cannot run through ExecuteModule (no _start) and their
	// cold-start is paid ahead of the request. Per-invocation identity rides
	// execCtx. Command-mode functions are unchanged.
	var output []byte
	if moduleIsReactor(module) {
		output, err = e.invokeReactor(execCtx, fn.WASMCID, fn.Name, input, instTiming)
	} else {
		execCtx = execution.WithNamespace(execCtx, fn.Namespace)
		execCtx = execution.WithMemoryLimitMB(execCtx, e.memoryLimitMB(fn))
		output, err = e.executor.ExecuteModule(execCtx, module, fn.Name, input)
	}
	executeDoneAt = time.Now()
	if err != nil {
		status := InvocationStatusError
		if execCtx.Err() == context.DeadlineExceeded {
			status = InvocationStatusTimeout
			err = ErrTimeout
		}
		e.logInvocation(ctx, fn, invCtx, logBuf, startTime, len(output), status, err)
		e.logSlowInvocation(invCtx, startTime, ratelimitDoneAt, moduleLoadedAt, executeDoneAt, instTiming.InstantiateNs, string(status), err)
		return nil, &ExecutionError{FunctionName: fn.Name, RequestID: invCtx.RequestID, Cause: err}
	}

	// Surface any verbatim HTTP response the function set (bugboard #835)
	// onto invCtx so the Invoker → HTTP handler can replay it. Only
	// RawHTTPResponse functions have a collector attached; TakeRawHTTPResponse
	// returns (_, false) otherwise.
	if res, ok := TakeRawHTTPResponse(execCtx); ok {
		invCtx.RawHTTP = &res
	}

	e.logInvocation(ctx, fn, invCtx, logBuf, startTime, len(output), InvocationStatusSuccess, nil)
	e.logSlowInvocation(invCtx, startTime, ratelimitDoneAt, moduleLoadedAt, executeDoneAt, instTiming.InstantiateNs, "success", nil)
	return output, nil
}

// logSlowInvocation emits a structured warning when total wall-clock
// exceeds slowInvokeThreshold (bugboard #24 diagnostic). Per-phase
// timestamps let operators see WHICH layer was the sink — pre-fix the
// only signal was an opaque WS-handler "timeout after 30s" with no way
// to tell whether rate-limit, module-load, or WASM-execute consumed
// the budget.
//
// Zero-valued phase timestamps mean the phase was never reached, which
// is itself signal — e.g. moduleLoadedAt=zero + executeDoneAt=zero with
// large totalMs means we blocked in rate-limit OR module-load.
func (e *Engine) logSlowInvocation(invCtx *InvocationContext, startTime, ratelimitDoneAt, moduleLoadedAt, executeDoneAt time.Time, instantiateNs int64, status string, err error) {
	totalMs := time.Since(startTime).Milliseconds()
	if totalMs < e.slowInvokeThreshold().Milliseconds() {
		return
	}
	// Compute phase deltas. Use 0 for unreached phases so the log line
	// columns are stable.
	var ratelimitMs, moduleLoadMs, executeMs int64
	if !ratelimitDoneAt.IsZero() {
		ratelimitMs = ratelimitDoneAt.Sub(startTime).Milliseconds()
	}
	if !moduleLoadedAt.IsZero() && !ratelimitDoneAt.IsZero() {
		moduleLoadMs = moduleLoadedAt.Sub(ratelimitDoneAt).Milliseconds()
	}
	if !executeDoneAt.IsZero() && !moduleLoadedAt.IsZero() {
		executeMs = executeDoneAt.Sub(moduleLoadedAt).Milliseconds()
	}
	// Split execute into instantiate (TinyGo _start cold-start) vs run
	// (handler logic). A count=0 read with instantiate_ms ≈ execute_ms and
	// run_ms ≈ 0 is the bugboard #27 cold-start floor — the per-call fresh
	// instantiation, not the handler, is the sink.
	instantiateMs := instantiateNs / int64(time.Millisecond)
	runMs := executeMs - instantiateMs
	if runMs < 0 {
		runMs = 0
	}
	fields := []zap.Field{
		zap.String("namespace", invCtx.Namespace),
		zap.String("function", invCtx.FunctionName),
		zap.String("request_id", invCtx.RequestID),
		zap.String("trigger_type", string(invCtx.TriggerType)),
		zap.String("ws_client_id", invCtx.WSClientID),
		zap.Int64("total_ms", totalMs),
		zap.Int64("ratelimit_ms", ratelimitMs),
		zap.Int64("module_load_ms", moduleLoadMs),
		zap.Int64("execute_ms", executeMs),
		zap.Int64("instantiate_ms", instantiateMs),
		zap.Int64("run_ms", runMs),
		zap.String("invocation_status", status),
	}
	if err != nil {
		fields = append(fields, zap.Error(err))
	}
	e.logger.Warn("slow serverless invocation (bug-24 diagnostic)", fields...)
}

// Precompile compiles a WASM module and caches it for faster execution.
func (e *Engine) Precompile(ctx context.Context, wasmCID string, wasmBytes []byte) error {
	if wasmCID == "" {
		return &ValidationError{Field: "wasmCID", Message: "cannot be empty"}
	}
	if len(wasmBytes) == 0 {
		return &ValidationError{Field: "wasmBytes", Message: "cannot be empty"}
	}

	// Check if already cached
	if e.moduleCache.Has(wasmCID) {
		return nil
	}

	// Compile the module
	compiled, err := e.lifecycle.CompileModule(ctx, wasmCID, wasmBytes)
	if err != nil {
		return &DeployError{FunctionName: wasmCID, Cause: err}
	}

	// Enforce memory limits
	if err := e.checkMemoryLimits(compiled); err != nil {
		compiled.Close(ctx)
		return &DeployError{FunctionName: wasmCID, Cause: err}
	}

	// Cache the compiled module
	e.moduleCache.Set(wasmCID, compiled)

	return nil
}

// Invalidate removes a compiled module from the cache.
func (e *Engine) Invalidate(wasmCID string) {
	e.moduleCache.Delete(context.Background(), wasmCID)
	// Drop any pre-warmed reactor instances for the old module so a redeploy
	// never serves stale code (bugboard #898).
	if e.reactorPool != nil {
		e.reactorPool.Invalidate(wasmCID)
	}
}

// Close shuts down the engine and releases resources.
func (e *Engine) Close(ctx context.Context) error {
	// Flush any pending invocation telemetry first (best-effort, bounded —
	// see invocationLogQueue.Close). Losing a few records at shutdown is
	// acceptable; blocking the process on telemetry is not.
	if e.logQueue != nil {
		e.logQueue.Close()
	}

	// Drain the reactor pre-warm pool (bugboard #898) before closing modules.
	if e.reactorPool != nil {
		e.reactorPool.Close()
	}

	// Close all cached modules
	e.moduleCache.Clear(ctx)

	// Close the runtime
	return e.runtime.Close(ctx)
}

// GetCacheStats returns cache statistics.
func (e *Engine) GetCacheStats() (size int, capacity int) {
	return e.moduleCache.GetStats()
}

// -----------------------------------------------------------------------------
// Private methods
// -----------------------------------------------------------------------------

// memoryLimitMB returns the memory limit enforced on one invocation of fn: the
// function's own limit, or the configured default when it has none stored.
func (e *Engine) memoryLimitMB(fn *Function) int {
	if fn.MemoryLimitMB > 0 {
		return fn.MemoryLimitMB
	}
	return e.config.DefaultMemoryLimitMB
}

// checkMemoryLimits validates that a compiled module's memory declarations
// don't exceed the configured maximum. Each WASM memory page is 64KB.
func (e *Engine) checkMemoryLimits(compiled wazero.CompiledModule) error {
	maxPages := uint32(e.config.MaxMemoryLimitMB * 16) // 1 MB = 16 pages (64KB each)
	for _, mem := range compiled.ExportedMemories() {
		if max, hasMax := mem.Max(); hasMax && max > maxPages {
			return fmt.Errorf("module declares %d MB max memory, exceeds limit of %d MB",
				max/16, e.config.MaxMemoryLimitMB)
		}
	}
	return nil
}

// getOrCompileModule retrieves a compiled module from cache or compiles it.
// InstantiatePersistent creates a long-lived module instance for a
// persistent WebSocket function. Unlike the per-frame stateless model,
// this instance:
//
//   - is NOT closed after a single call
//   - has its WASI _start hook DISABLED (the function's main() must be
//     empty; the lifecycle exports ws_open/ws_frame/ws_close are called
//     explicitly by the caller)
//   - retains memory across frames
//
// Caller is responsible for calling Close() on the returned api.Module
// (typically wrapped in persistent.Instance which handles this).
func (e *Engine) InstantiatePersistent(ctx context.Context, fn *Function, invCtx *InvocationContext) (api.Module, error) {
	compiled, err := e.getOrCompileModule(ctx, fn.WASMCID)
	if err != nil {
		return nil, fmt.Errorf("InstantiatePersistent: compile: %w", err)
	}

	// Persistent WS uses per-call invCtx propagation through ctx —
	// see pkg/serverless/invocation_context.go for the cross-tenant
	// race rationale. The persistent.Instance wrapper attaches invCtx
	// to every WASM-host call's ctx via WithInvocationContext, so we
	// do NOT touch the HostFunctions singleton here. Two simultaneous
	// persistent connections from different users now keep their
	// caller identity isolated.

	// Persistent-instance runtime-init policy. TinyGo emits one of two
	// start hooks depending on the build target:
	//
	//   - wasi-reactor target → exports `_initialize` only
	//   - wasi (command) target → exports `_start` only
	//
	// Both hooks run the runtime's initAll (heap, GC, package init).
	// `_start` additionally calls `main()` — fine when main is an
	// empty stub (which is the convention for persistent WS functions
	// since the gateway drives lifecycle via ws_open / ws_frame /
	// ws_close, NOT main()).
	//
	// Without one of them being called, TinyGo's runtime stays in an
	// uninitialized state and the very first export call traps via
	// `wasmExportCheckRun` — managed-memory operations (allocs,
	// hashmap ops) panic immediately.
	//
	// History of this code path (bugs #240/#249 follow-ups):
	//   - Original code: `WithStartFunctions()` with NO args
	//     (explicitly disable both). Intent was to skip main(); side
	//     effect was breaking TinyGo init. Persistent WS dead since
	//     plan #06 landed.
	//   - First fix: call `_initialize` manually. Worked for
	//     wasi-reactor builds. Still broken for wasi (command) builds
	//     like AnChat's rpc-router which only exports `_start`.
	//   - This fix: try `_initialize` first; fall back to `_start`
	//     if reactor hook isn't exported. Bounded by a 5s timeout so
	//     a runaway main() can't hang instantiation forever.
	//
	// AnChat's wasm-objdump output that pinned this:
	//   Export[15]:
	//    - func[127] <_start>                          → "_start"
	//    - func[414] <main.oramaAlloc#wasmexport>      → "orama_alloc"
	//    - func[416] <main.wsOpen#wasmexport>          → "ws_open"
	//    ...
	//   (no `_initialize`)
	//
	// We still pass `WithStartFunctions()` (no args) so wazero doesn't
	// auto-call `_start` during InstantiateModule — we want full
	// control over which hook runs and to bound it with our own
	// timeout.
	moduleConfig := wazero.NewModuleConfig().
		WithName(fn.Name + "-" + invCtx.WSClientID).
		WithStartFunctions().
		WithStdin(emptyReader{}).
		WithStdout(discardWriter{}).
		WithStderr(discardWriter{}).
		WithArgs(fn.Name).
		// Bugboard #27 — wazero defaults to fake/sentinel clocks (deterministic
		// fixtures for unit testing). TinyGo wasm calls WASI clock_time_get
		// from time.Now() and gets a frozen ~2022-01-01T00:00:00.001Z back
		// for every reading, silently poisoning any serverless function that
		// embeds timestamps (receipts, audit rows, cursor cmp logic). Opt
		// into real clocks via the documented wazero hook — same effect as
		// the runtime would get on a normal Go process.
		WithSysWalltime().
		WithSysNanotime().
		// Bugboard #120 — same class as #27. Without WithRandSource, wazero's
		// default RNG is deterministic (zero seed), so TinyGo crypto/rand.Read
		// returns identical bytes on every fresh instance — constant codes /
		// nonces / tokens. Wire in the host CSPRNG. Same fix at
		// execution/executor.go for the stateless path.
		WithRandSource(cryptorand.Reader)

	instance, err := e.runtime.InstantiateModule(ctx, compiled, moduleConfig)
	if err != nil {
		return nil, fmt.Errorf("InstantiatePersistent: instantiate: %w", err)
	}

	// Bootstrap the wasm runtime. Try reactor hook first (no main()),
	// then command hook (assumes main() is an empty stub per
	// persistent-function convention). Bounded by a short timeout so
	// a buggy main() can't hang every connection.
	//
	// Wrap initCtx with invCtx so any host functions called from a TinyGo
	// init() (e.g. early GetEnv / GetSecret reads) see this connection's
	// caller identity, not whatever happens to be on the singleton.
	const initTimeout = 5 * time.Second
	initCtx, initCancel := context.WithTimeout(WithInvocationContext(ctx, invCtx), initTimeout)
	defer initCancel()

	var initName string
	var initFn api.Function
	if hook := instance.ExportedFunction("_initialize"); hook != nil {
		initName, initFn = "_initialize", hook
	} else if hook := instance.ExportedFunction("_start"); hook != nil {
		initName, initFn = "_start", hook
	}
	if initFn != nil {
		_, callErr := initFn.Call(initCtx)
		if callErr != nil {
			// ExitError(0) is the "command-mode _start completed cleanly"
			// signal from TinyGo (target=wasi). Our custom proc_exit
			// override (persistentFriendlyProcExit, registered at engine
			// setup) keeps the module alive in this case — it just
			// panics ExitError(0) without calling CloseWithExitCode.
			// So the bootstrap is actually successful and the module's
			// exports remain callable.
			//
			// Anything else is a real failure: ExitError(N != 0) means
			// the function's main() returned non-zero (or proc_exit was
			// called explicitly with non-zero), or the runtime trapped
			// during init. Close + propagate.
			var exitErr *sys.ExitError
			if errors.As(callErr, &exitErr) && exitErr.ExitCode() == 0 {
				e.logger.Debug("persistent instance bootstrapped via _start (command-mode normal exit)",
					zap.String("function", fn.Name),
					zap.String("client_id", invCtx.WSClientID),
					zap.String("init_hook", initName))
			} else {
				_ = instance.Close(ctx)
				return nil, fmt.Errorf("InstantiatePersistent: %s: %w", initName, callErr)
			}
		} else {
			// _initialize-style clean return (no panic). wasi-reactor
			// modules built with TinyGo `//go:wasmexport` go this path.
			e.logger.Debug("persistent instance bootstrapped",
				zap.String("function", fn.Name),
				zap.String("client_id", invCtx.WSClientID),
				zap.String("init_hook", initName))
		}
	} else {
		// Neither hook exported. The module may still work if it has
		// no managed-memory operations — but that's rare in TinyGo.
		// Log a warning so a function author who hits this can
		// diagnose without filing a ticket.
		e.logger.Warn("persistent module exports no _initialize or _start; runtime may be uninitialized",
			zap.String("function", fn.Name),
			zap.String("client_id", invCtx.WSClientID))
	}

	return instance, nil
}

// instantiateReactorInstance warms a one-shot reactor instance for the
// reactor-mode function identified by wasmCID: it instantiates the compiled
// module and runs the TinyGo _initialize cold-start so the returned instance
// can serve a single `handle` call immediately. The reactor pool calls this in
// the background (bugboard #898); name must be unique within the runtime.
//
// REACTOR CONTRACT: _initialize must be identity-independent. Warming happens
// before any request, so there is no caller identity in scope — a reactor
// function does all per-caller work in handle() (which DOES receive the
// invocation context via ctx), never in package init().
func (e *Engine) instantiateReactorInstance(ctx context.Context, wasmCID, name string) (api.Module, error) {
	compiled, err := e.getOrCompileModule(ctx, wasmCID)
	if err != nil {
		return nil, fmt.Errorf("reactor warm: compile: %w", err)
	}
	moduleConfig := wazero.NewModuleConfig().
		WithName(name).
		WithStartFunctions(). // suppress auto-start; we run _initialize explicitly
		WithStdin(emptyReader{}).
		WithStdout(discardWriter{}).
		WithStderr(discardWriter{}).
		WithArgs(name).
		WithSysWalltime(). // real clocks (bugboard #27)
		WithSysNanotime().
		WithRandSource(cryptorand.Reader) // real CSPRNG (bugboard #120)

	instance, err := e.runtime.InstantiateModule(ctx, compiled, moduleConfig)
	if err != nil {
		return nil, fmt.Errorf("reactor warm: instantiate: %w", err)
	}
	if hook := instance.ExportedFunction("_initialize"); hook != nil {
		// Pin warm-time host calls to a neutral, identity-free invocation context
		// so a guest init() can never observe a concurrent invocation's singleton
		// identity (bugboard #898 hardening). Per-caller work belongs in handle(),
		// which receives the real caller via ctx.
		warmCtx := WithInvocationContext(ctx, &InvocationContext{})
		initCtx, cancel := context.WithTimeout(warmCtx, 5*time.Second)
		defer cancel()
		if _, callErr := hook.Call(initCtx); callErr != nil {
			_ = instance.Close(ctx)
			return nil, fmt.Errorf("reactor warm: _initialize: %w", callErr)
		}
	}
	return instance, nil
}

// moduleIsReactor reports whether a compiled module was built reactor-mode
// (TinyGo -buildmode=c-shared): it exports the `handle` entrypoint plus the WASI
// reactor `_initialize` hook and has NO command-mode `_start`. Such a module
// cannot run via ExecuteModule (which expects _start + stdout output) — it must
// be driven by calling handle(). Command-mode functions export `_start` and
// return false, so they keep using ExecuteModule unchanged.
func moduleIsReactor(compiled wazero.CompiledModule) bool {
	exports := compiled.ExportedFunctions()
	_, hasHandle := exports["handle"]
	_, hasInit := exports["_initialize"]
	_, hasStart := exports["_start"]
	return hasHandle && hasInit && !hasStart
}

// invokeReactor runs a reactor-mode function (bugboard #898): it serves the call
// from a pre-warmed pool instance when one is ready (skipping the ~550ms
// cold-start), otherwise instantiates one synchronously (cold fallback). Either
// way the instance serves THIS one call and is then closed — never reused, so
// isolation is identical to fresh-instance-per-call. Per-invocation identity
// rides ctx (WithInvocationContext), so a generic warmed instance resolves the
// correct caller in its host calls.
func (e *Engine) invokeReactor(ctx context.Context, wasmCID, fnName string, input []byte, instTiming *execution.InstantiateTiming) ([]byte, error) {
	instance, warm := e.reactorPool.Acquire(wasmCID)
	if !warm {
		// Pool miss: pay the cold-start synchronously and attribute it to the
		// instantiate phase so the bugboard #27 slow-invoke diagnostic stays
		// accurate (a warm hit pays ~0 instantiate on the request path).
		coldStart := time.Now()
		var err error
		instance, err = e.reactorPool.InstantiateSync(ctx, wasmCID)
		if instTiming != nil {
			instTiming.InstantiateNs = time.Since(coldStart).Nanoseconds()
		}
		if err != nil {
			return nil, fmt.Errorf("reactor cold instantiate %s: %w", fnName, err)
		}
	}
	defer instance.Close(context.Background())
	return e.executor.CallHandleFunction(ctx, instance, input)
}

// emptyReader satisfies io.Reader for persistent WASM stdin.
type emptyReader struct{}

func (emptyReader) Read(p []byte) (int, error) { return 0, nil }

// discardWriter satisfies io.Writer for persistent WASM stdout/stderr.
// Unlike io.Discard which has special handling, this is a typed value
// suitable for the wazero ModuleConfig API.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func (e *Engine) getOrCompileModule(ctx context.Context, wasmCID string) (wazero.CompiledModule, error) {
	return e.moduleCache.GetOrCompute(ctx, wasmCID, func() (wazero.CompiledModule, error) {
		// Fetch WASM bytes from registry
		wasmBytes, err := e.registry.GetWASMBytes(ctx, wasmCID)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch WASM: %w", err)
		}

		// Compile the module
		compiled, err := e.lifecycle.CompileModule(ctx, wasmCID, wasmBytes)
		if err != nil {
			return nil, ErrCompilationFailed
		}

		// Enforce memory limits
		if err := e.checkMemoryLimits(compiled); err != nil {
			compiled.Close(ctx)
			return nil, err
		}

		return compiled, nil
	})
}

// logInvocation records an invocation's telemetry.
//
// IMPORTANT behavior note (bugboard feat-27): the record is now ENQUEUED for
// asynchronous writing — it is NOT written on the reply path. A single worker
// goroutine drains the queue and writes with its own context, so a
// function_invocations row may lag the response by up to the queue drain time.
// That lag is acceptable for telemetry and is worth it: it removes ~500ms-3s
// of cross-region Raft write latency from every serverless RPC round-trip.
// `ctx` is therefore unused for the write (the request ctx dies when Execute
// returns); it is retained only to keep the call-site signature stable.
//
// `logBuf` is the per-invocation LogBuffer attached to ctx at Execute
// start (bugboard #108 fix). When non-nil, the record's Logs field is
// populated from the buffer's snapshot — invocation-local, no
// cross-contamination. When nil (legacy callers that haven't been
// updated), falls back to the HostFunctions singleton via the
// GetLogs() interface check — same behavior as pre-#108.
func (e *Engine) logInvocation(ctx context.Context, fn *Function, invCtx *InvocationContext, logBuf *LogBuffer, startTime time.Time, outputSize int, status InvocationStatus, err error) {
	_ = ctx // request context is intentionally not used for the async write
	if e.logQueue == nil || !e.config.LogInvocations {
		return
	}

	completedAt := time.Now()
	record := &InvocationRecord{
		ID:           uuid.New().String(),
		FunctionID:   fn.ID,
		RequestID:    invCtx.RequestID,
		TriggerType:  invCtx.TriggerType,
		CallerWallet: invCtx.CallerWallet,
		OutputSize:   outputSize,
		StartedAt:    startTime,
		CompletedAt:  completedAt,
		DurationMS:   completedAt.Sub(startTime).Milliseconds(),
		Status:       status,
	}

	if err != nil {
		record.ErrorMessage = err.Error()
	}

	// The invocation's own log buffer, and nothing else. There used to be a
	// fallback to a slice shared by every invocation on the gateway, which is
	// how one invocation's log lines ended up in another's record (bugboard
	// #108). Every Execute path attaches a buffer.
	if logBuf != nil {
		record.Logs = logBuf.Snapshot()
	}

	// Enqueue is non-blocking: a full queue drops the record (counted) rather
	// than stalling the reply path. See invocationLogQueue.enqueue.
	e.logQueue.enqueue(record)
}

// registerHostModule registers the Orama host functions with the wazero runtime.
//
// We expose the SAME export set under three module names:
//
//   - "env"    — canonical. Matches the WASI / TinyGo convention. The
//     official SDK examples and docs use this name.
//   - "host"   — long-standing alias kept for backward compatibility.
//   - "orama"  — alias added 2026-05-06 after multiple apps intuited the
//     brand name as the import target and hit cryptic
//     "module[orama] not instantiated" errors. Cheap insurance:
//     a few KB of runtime metadata per alias, zero behavioral
//     cost. Apps SHOULD prefer `env` going forward; `orama` is
//     supported indefinitely to avoid breaking deployed code.
//
// All three names resolve to identical function tables — a WASM module
// can mix imports across the three with no consequence.
func (e *Engine) registerHostModule(ctx context.Context) error {
	for _, moduleName := range []string{"env", "host", "orama"} {
		_, err := e.runtime.NewHostModuleBuilder(moduleName).
			NewFunctionBuilder().WithFunc(e.hGetCallerWallet).Export("get_caller_wallet").
			NewFunctionBuilder().WithFunc(e.hGetCallerJWTSubject).Export("get_caller_jwt_subject").
			NewFunctionBuilder().WithFunc(e.hGetCallerDeviceID).Export("get_caller_device_id").
			NewFunctionBuilder().WithFunc(e.hGetCallerAttestation).Export("get_caller_attestation").
			NewFunctionBuilder().WithFunc(e.hGetCallerAttestationVerdict).Export("get_caller_attestation_verdict").
			NewFunctionBuilder().WithFunc(e.hGetCallerCapability).Export("get_caller_capability").
			NewFunctionBuilder().WithFunc(e.hCapabilityMint).Export("capability_mint").
			NewFunctionBuilder().WithFunc(e.hCapabilityRevoke).Export("capability_revoke").
			NewFunctionBuilder().WithFunc(e.hStorageFetchCapMint).Export("storage_fetch_cap_mint").
			NewFunctionBuilder().WithFunc(e.hGetWSClientID).Export("get_ws_client_id").
			NewFunctionBuilder().WithFunc(e.hGetCallerClaim).Export("get_caller_claim").
			NewFunctionBuilder().WithFunc(e.hGetRequestID).Export("get_request_id").
			NewFunctionBuilder().WithFunc(e.hGetEnv).Export("get_env").
			NewFunctionBuilder().WithFunc(e.hGetSecret).Export("get_secret").
			NewFunctionBuilder().WithFunc(e.hDBQuery).Export("db_query").
			NewFunctionBuilder().WithFunc(e.hDBQueryV2).Export("db_query_v2").
			NewFunctionBuilder().WithFunc(e.hDBExecute).Export("db_execute").
			NewFunctionBuilder().WithFunc(e.hDBExecuteV2).Export("db_execute_v2").
			NewFunctionBuilder().WithFunc(e.hDBTransaction).Export("db_transaction").
			NewFunctionBuilder().WithFunc(e.hDBQueryBatch).Export("db_query_batch").
			NewFunctionBuilder().WithFunc(e.hExecAndPublish).Export("exec_and_publish").
			NewFunctionBuilder().WithFunc(e.hCacheGet).Export("cache_get").
			NewFunctionBuilder().WithFunc(e.hCacheSet).Export("cache_set").
			NewFunctionBuilder().WithFunc(e.hCacheDelete).Export("cache_delete").
			NewFunctionBuilder().WithFunc(e.hCacheIncr).Export("cache_incr").
			NewFunctionBuilder().WithFunc(e.hCacheIncrBy).Export("cache_incr_by").
			NewFunctionBuilder().WithFunc(e.hCacheIncrTTL).Export("cache_incr_ttl").
			NewFunctionBuilder().WithFunc(e.hHTTPFetch).Export("http_fetch").
			NewFunctionBuilder().WithFunc(e.hAnonFetch).Export(AnonFetchExport).
			NewFunctionBuilder().WithFunc(e.hAnonFetch).Export(AnyoneFetchDeprecatedExport).
			NewFunctionBuilder().WithFunc(e.hSetHTTPResponse).Export("set_http_response").
			NewFunctionBuilder().WithFunc(e.hPubSubPublish).Export("pubsub_publish").
			NewFunctionBuilder().WithFunc(e.hPubSubPublishBatch).Export("pubsub_publish_batch").
			NewFunctionBuilder().WithFunc(e.hPushSend).Export("push_send").
			NewFunctionBuilder().WithFunc(e.hPushSendV2).Export("push_send_v2").
			NewFunctionBuilder().WithFunc(e.hPushSendTopic).Export("push_send_topic").
			NewFunctionBuilder().WithFunc(e.hTurnCredentials).Export("turn_credentials").
			NewFunctionBuilder().WithFunc(e.hWebRTCAdmit).Export("webrtc_admit").
			NewFunctionBuilder().WithFunc(e.hWebRTCKick).Export("webrtc_kick").
			NewFunctionBuilder().WithFunc(e.hWebRTCKickDevice).Export("webrtc_kick_device").
			NewFunctionBuilder().WithFunc(e.hWebRTCMute).Export("webrtc_mute").
			NewFunctionBuilder().WithFunc(e.hWebRTCMuteVideo).Export("webrtc_mute_video").
			NewFunctionBuilder().WithFunc(e.hWebRTCAdmission).Export("webrtc_admission").
			NewFunctionBuilder().WithFunc(e.hWSPubSubBridge).Export("ws_pubsub_bridge").
			NewFunctionBuilder().WithFunc(e.hWSPubSubUnbridge).Export("ws_pubsub_unbridge").
			NewFunctionBuilder().WithFunc(e.hWSSend).Export("ws_send").
			NewFunctionBuilder().WithFunc(e.hWSBroadcast).Export("ws_broadcast").
			NewFunctionBuilder().WithFunc(e.hEphemeralStateSet).Export("ephemeral_state_set").
			NewFunctionBuilder().WithFunc(e.hEphemeralStateClear).Export("ephemeral_state_clear").
			NewFunctionBuilder().WithFunc(e.hEphemeralStateList).Export("ephemeral_state_list").
			NewFunctionBuilder().WithFunc(e.hFunctionInvoke).Export("function_invoke").
			NewFunctionBuilder().WithFunc(e.hFunctionInvokeAsync).Export("function_invoke_async").
			NewFunctionBuilder().WithFunc(e.hFunctionInvokeLastError).Export("function_invoke_last_error").
			NewFunctionBuilder().WithFunc(e.hLogInfo).Export("log_info").
			NewFunctionBuilder().WithFunc(e.hLogError).Export("log_error").
			Instantiate(ctx)
		if err != nil {
			return err
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// Host function implementations (delegate to executor for memory operations)
// -----------------------------------------------------------------------------

func (e *Engine) hGetCallerWallet(ctx context.Context, mod api.Module) uint64 {
	wallet := e.hostServices.GetCallerWallet(ctx)
	return e.executor.WriteToGuest(ctx, mod, []byte(wallet))
}

func (e *Engine) hGetRequestID(ctx context.Context, mod api.Module) uint64 {
	rid := e.hostServices.GetRequestID(ctx)
	return e.executor.WriteToGuest(ctx, mod, []byte(rid))
}

// hGetWSClientID returns the current invocation's WebSocket client ID, or
// empty string if the function wasn't invoked via WS.
func (e *Engine) hGetWSClientID(ctx context.Context, mod api.Module) uint64 {
	cid := e.hostServices.GetWSClientID(ctx)
	return e.executor.WriteToGuest(ctx, mod, []byte(cid))
}

// hGetCallerJWTSubject returns the JWT `sub` claim explicitly. Empty
// string if the request was not JWT-authenticated. See bug #215.
func (e *Engine) hGetCallerJWTSubject(ctx context.Context, mod api.Module) uint64 {
	sub := e.hostServices.GetCallerJWTSubject(ctx)
	return e.executor.WriteToGuest(ctx, mod, []byte(sub))
}

// hGetCallerDeviceID returns the device the caller's session is bound to.
// Empty string when it is bound to none.
func (e *Engine) hGetCallerDeviceID(ctx context.Context, mod api.Module) uint64 {
	did := e.hostServices.GetCallerDeviceID(ctx)
	return e.executor.WriteToGuest(ctx, mod, []byte(did))
}

// hGetCallerAttestation returns what the caller's device attestation proved,
// as JSON, or an empty string when the caller's token carries none.
func (e *Engine) hGetCallerAttestation(ctx context.Context, mod api.Module) uint64 {
	att := e.hostServices.GetCallerAttestation(ctx)
	return e.executor.WriteToGuest(ctx, mod, []byte(att))
}

// hGetCallerAttestationVerdict returns the gateway's verdict on the caller under
// the namespace's attestation policy as it is now, as JSON (feat-965).
func (e *Engine) hGetCallerAttestationVerdict(ctx context.Context, mod api.Module) uint64 {
	verdict := e.hostServices.GetCallerAttestationVerdict(ctx)
	return e.executor.WriteToGuest(ctx, mod, []byte(verdict))
}

// hGetCallerClaim reads a claim name from guest memory, looks it up on the
// caller's JWT custom claims, and writes the value (or empty string) back.
func (e *Engine) hGetCallerClaim(ctx context.Context, mod api.Module, namePtr, nameLen uint32) uint64 {
	name, ok := e.executor.ReadFromGuest(mod, namePtr, nameLen)
	if !ok {
		return 0
	}
	val := e.hostServices.GetCallerClaim(ctx, string(name))
	return e.executor.WriteToGuest(ctx, mod, []byte(val))
}

func (e *Engine) hGetEnv(ctx context.Context, mod api.Module, keyPtr, keyLen uint32) uint64 {
	key, ok := e.executor.ReadFromGuest(mod, keyPtr, keyLen)
	if !ok {
		return 0
	}
	val, _ := e.hostServices.GetEnv(ctx, string(key))
	return e.executor.WriteToGuest(ctx, mod, []byte(val))
}

func (e *Engine) hGetSecret(ctx context.Context, mod api.Module, namePtr, nameLen uint32) uint64 {
	name, ok := e.executor.ReadFromGuest(mod, namePtr, nameLen)
	if !ok {
		return 0
	}
	val, err := e.hostServices.GetSecret(ctx, string(name))
	if err != nil {
		return 0
	}
	return e.executor.WriteToGuest(ctx, mod, []byte(val))
}

func (e *Engine) hDBQuery(ctx context.Context, mod api.Module, queryPtr, queryLen, argsPtr, argsLen uint32) uint64 {
	query, ok := e.executor.ReadFromGuest(mod, queryPtr, queryLen)
	if !ok {
		return 0
	}

	var args []interface{}
	if argsLen > 0 {
		if err := e.executor.UnmarshalJSONFromGuest(mod, argsPtr, argsLen, &args); err != nil {
			HostLogger(ctx, e.logger).Error("failed to unmarshal db_query arguments", zap.Error(err))
			return 0
		}
	}

	results, err := e.hostServices.DBQuery(ctx, string(query), args)
	if err != nil {
		HostLogger(ctx, e.logger).Error("host function db_query failed", zap.Error(err), zap.String("query", string(query)))
		return 0
	}
	return e.executor.WriteToGuest(ctx, mod, results)
}

func (e *Engine) hDBExecute(ctx context.Context, mod api.Module, queryPtr, queryLen, argsPtr, argsLen uint32) uint32 {
	query, ok := e.executor.ReadFromGuest(mod, queryPtr, queryLen)
	if !ok {
		return 0
	}

	var args []interface{}
	if argsLen > 0 {
		if err := e.executor.UnmarshalJSONFromGuest(mod, argsPtr, argsLen, &args); err != nil {
			HostLogger(ctx, e.logger).Error("failed to unmarshal db_execute arguments", zap.Error(err))
			return 0
		}
	}

	affected, err := e.hostServices.DBExecute(ctx, string(query), args)
	if err != nil {
		HostLogger(ctx, e.logger).Error("host function db_execute failed", zap.Error(err), zap.String("query", string(query)))
		return 0
	}
	return uint32(affected)
}

func (e *Engine) hCacheGet(ctx context.Context, mod api.Module, keyPtr, keyLen uint32) uint64 {
	key, ok := e.executor.ReadFromGuest(mod, keyPtr, keyLen)
	if !ok {
		return 0
	}
	val, err := e.hostServices.CacheGet(ctx, string(key))
	if err != nil {
		// A miss is the normal answer and returns empty. Anything else is a
		// failure the guest cannot tell from a miss, so it is logged.
		if !errors.Is(err, ErrCacheMiss) {
			HostLogger(ctx, e.logger).Error("host function cache_get failed", zap.Error(err), zap.String("key", logSafeKey(key)))
		}
		return 0
	}
	return e.executor.WriteToGuest(ctx, mod, val)
}

func (e *Engine) hCacheSet(ctx context.Context, mod api.Module, keyPtr, keyLen, valPtr, valLen uint32, ttl int64) {
	key, ok := e.executor.ReadFromGuest(mod, keyPtr, keyLen)
	if !ok {
		return
	}
	val, ok := e.executor.ReadFromGuest(mod, valPtr, valLen)
	if !ok {
		return
	}
	// cache_set has no return value in the guest ABI, so a failure can only
	// be surfaced in the gateway log.
	if err := e.hostServices.CacheSet(ctx, string(key), val, ttl); err != nil {
		fields := []zap.Field{zap.Error(err), zap.String("key", logSafeKey(key)), zap.Int64("ttl_seconds", ttl)}
		if errors.Is(err, ErrInvalidCacheTTL) {
			HostLogger(ctx, e.logger).Warn("host function cache_set refused its ttl", fields...)
			return
		}
		HostLogger(ctx, e.logger).Error("host function cache_set failed", fields...)
	}
}

// maxLoggedKeyBytes bounds a guest-chosen cache key in a log line. A key can
// be as large as guest memory, and one failing call per log line would let a
// guest flood the gateway log.
const maxLoggedKeyBytes = 64

func logSafeKey(key []byte) string {
	if len(key) <= maxLoggedKeyBytes {
		return string(key)
	}
	return fmt.Sprintf("%s…(%d bytes)", strings.ToValidUTF8(string(key[:maxLoggedKeyBytes]), ""), len(key))
}

// hCacheDelete returns 1 when the key is gone afterwards (deleted, or was
// never there) and 0 when the delete could not be performed.
func (e *Engine) hCacheDelete(ctx context.Context, mod api.Module, keyPtr, keyLen uint32) uint32 {
	key, ok := e.executor.ReadFromGuest(mod, keyPtr, keyLen)
	if !ok {
		return 0
	}
	if err := e.hostServices.CacheDelete(ctx, string(key)); err != nil {
		HostLogger(ctx, e.logger).Error("host function cache_delete failed", zap.Error(err), zap.String("key", logSafeKey(key)))
		return 0
	}
	return 1
}

func (e *Engine) hCacheIncr(ctx context.Context, mod api.Module, keyPtr, keyLen uint32) int64 {
	key, ok := e.executor.ReadFromGuest(mod, keyPtr, keyLen)
	if !ok {
		return 0
	}
	val, err := e.hostServices.CacheIncr(ctx, string(key))
	if err != nil {
		HostLogger(ctx, e.logger).Error("host function cache_incr failed", zap.Error(err), zap.String("key", logSafeKey(key)))
		return 0
	}
	return val
}

func (e *Engine) hCacheIncrBy(ctx context.Context, mod api.Module, keyPtr, keyLen uint32, delta int64) int64 {
	key, ok := e.executor.ReadFromGuest(mod, keyPtr, keyLen)
	if !ok {
		return 0
	}
	val, err := e.hostServices.CacheIncrBy(ctx, string(key), delta)
	if err != nil {
		HostLogger(ctx, e.logger).Error("host function cache_incr_by failed", zap.Error(err), zap.String("key", logSafeKey(key)), zap.Int64("delta", delta))
		return 0
	}
	return val
}

// hCacheIncrTTL is cache_incr_by for a counter that must expire: the counter
// expires ttl seconds after the call that creates it (bugboard #741). It is a
// new export rather than a parameter on cache_incr_by because a deployed
// module's import signature is fixed.
func (e *Engine) hCacheIncrTTL(ctx context.Context, mod api.Module, keyPtr, keyLen uint32, delta, ttl int64) int64 {
	key, ok := e.executor.ReadFromGuest(mod, keyPtr, keyLen)
	if !ok {
		return 0
	}
	val, err := e.hostServices.CacheIncrByTTL(ctx, string(key), delta, ttl)
	if err != nil {
		fields := []zap.Field{zap.Error(err), zap.String("key", logSafeKey(key)), zap.Int64("delta", delta), zap.Int64("ttl_seconds", ttl)}
		if errors.Is(err, ErrInvalidCacheTTL) {
			HostLogger(ctx, e.logger).Warn("host function cache_incr_ttl refused its ttl", fields...)
			return 0
		}
		HostLogger(ctx, e.logger).Error("host function cache_incr_ttl failed", fields...)
		return 0
	}
	return val
}

func (e *Engine) hHTTPFetch(ctx context.Context, mod api.Module, methodPtr, methodLen, urlPtr, urlLen, headersPtr, headersLen, bodyPtr, bodyLen uint32) uint64 {
	method, ok := e.executor.ReadFromGuest(mod, methodPtr, methodLen)
	if !ok {
		return 0
	}
	u, ok := e.executor.ReadFromGuest(mod, urlPtr, urlLen)
	if !ok {
		return 0
	}

	var headers map[string]string
	if headersLen > 0 {
		if err := e.executor.UnmarshalJSONFromGuest(mod, headersPtr, headersLen, &headers); err != nil {
			HostLogger(ctx, e.logger).Error("failed to unmarshal http_fetch headers", zap.Error(err))
			return 0
		}
	}

	body, ok := e.executor.ReadFromGuest(mod, bodyPtr, bodyLen)
	if !ok {
		return 0
	}

	resp, err := e.hostServices.HTTPFetch(ctx, string(method), string(u), headers, body)
	if err != nil {
		// The client's error quotes the whole URL, and a credential or the
		// user's address may be in its path or query: only the origin is named.
		HostLogger(ctx, e.logger).Error("host function http_fetch failed",
			zap.String("error", httputil.FailureReason(err)), zap.String("origin", httputil.Origin(string(u))))
		return 0
	}
	return e.executor.WriteToGuest(ctx, mod, resp)
}

// hSetHTTPResponse is the WASM-callable wrapper for SetHTTPResponse —
// bugboard #835 raw-HTTP-response mode.
//
// ABI: set_http_response(status i32, headersJSONPtr, headersJSONLen,
// bodyPtr, bodyLen uint32) -> uint32. headersJSON (when non-empty) is a JSON
// object of string→string. Returns 1 on success, 0 on failure (function not
// deployed with raw_http_response, bad status, oversized headers/body, or a
// guest-memory read error).
func (e *Engine) hSetHTTPResponse(ctx context.Context, mod api.Module,
	status, headersPtr, headersLen, bodyPtr, bodyLen uint32) uint32 {
	var headers map[string]string
	if headersLen > 0 {
		if err := e.executor.UnmarshalJSONFromGuest(mod, headersPtr, headersLen, &headers); err != nil {
			HostLogger(ctx, e.logger).Warn("set_http_response: failed to unmarshal headers", zap.Error(err))
			return 0
		}
	}

	var body []byte
	if bodyLen > 0 {
		b, ok := e.executor.ReadFromGuest(mod, bodyPtr, bodyLen)
		if !ok {
			return 0
		}
		body = b
	}

	if err := e.hostServices.SetHTTPResponse(ctx, int(status), headers, body); err != nil {
		HostLogger(ctx, e.logger).Warn("host function set_http_response failed", zap.Error(err))
		return 0
	}
	return 1
}

// AnonFetchExport is the host export for outbound HTTP through the node's Tor
// client.
const AnonFetchExport = "anon_fetch"

// AnyoneFetchDeprecatedExport is the name anon_fetch had while the anonymity
// backend was the Anyone network. It is exported as an alias of the same
// implementation — now routed through Tor — because deployed functions import
// it (AnChat's rpc-solana and rpc-evm import orama.anyone_fetch), and a module
// whose import is missing fails to instantiate. New code imports anon_fetch.
const AnyoneFetchDeprecatedExport = "anyone_fetch"

// hAnonFetch is the WASM-callable wrapper for AnonFetch — feat-11, exported
// as both anon_fetch and the deprecated anyone_fetch.
// Identical ABI to hHTTPFetch (method, url, headers JSON, body), routes
// through the Tor SOCKS5 proxy. Returns packed (ptr<<32 | len) to the
// JSON response envelope, or 0 on a setup error (the typed
// proxy-unavailable / transport-error cases come back inside the
// envelope with status 0, NOT as a 0 return).
func (e *Engine) hAnonFetch(ctx context.Context, mod api.Module, methodPtr, methodLen, urlPtr, urlLen, headersPtr, headersLen, bodyPtr, bodyLen uint32) uint64 {
	method, ok := e.executor.ReadFromGuest(mod, methodPtr, methodLen)
	if !ok {
		return 0
	}
	u, ok := e.executor.ReadFromGuest(mod, urlPtr, urlLen)
	if !ok {
		return 0
	}

	var headers map[string]string
	if headersLen > 0 {
		if err := e.executor.UnmarshalJSONFromGuest(mod, headersPtr, headersLen, &headers); err != nil {
			HostLogger(ctx, e.logger).Error("failed to unmarshal anon_fetch headers", zap.Error(err))
			return 0
		}
	}

	body, ok := e.executor.ReadFromGuest(mod, bodyPtr, bodyLen)
	if !ok {
		return 0
	}

	resp, err := e.hostServices.AnonFetch(ctx, string(method), string(u), headers, body)
	if err != nil {
		// An anon_fetch caller chose Tor so the node could not say where the
		// function went: neither the URL nor the error text, which names the
		// destination, is logged.
		HostLogger(ctx, e.logger).Error("host function anon_fetch failed", zap.String("error_class", anonproxy.ErrorClass(err)))
		return 0
	}
	return e.executor.WriteToGuest(ctx, mod, resp)
}

func (e *Engine) hPubSubPublish(ctx context.Context, mod api.Module, topicPtr, topicLen, dataPtr, dataLen uint32) uint32 {
	topic, ok := e.executor.ReadFromGuest(mod, topicPtr, topicLen)
	if !ok {
		return 0
	}

	data, ok := e.executor.ReadFromGuest(mod, dataPtr, dataLen)
	if !ok {
		return 0
	}

	err := e.hostServices.PubSubPublish(ctx, string(topic), data)
	if err != nil {
		HostLogger(ctx, e.logger).Error("host function pubsub_publish failed", zap.Error(err), zap.String("topic", string(topic)))
		return 0
	}
	return 1 // Success
}

// hPubSubPublishBatch is the WASM-callable wrapper for PubSubPublishBatch.
// Input: pointer/length of a JSON array of {topic, data_base64}.
// Returns 1 on success, 0 on error.
func (e *Engine) hPubSubPublishBatch(ctx context.Context, mod api.Module, msgsPtr, msgsLen uint32) uint32 {
	msgsJSON, ok := e.executor.ReadFromGuest(mod, msgsPtr, msgsLen)
	if !ok {
		return 0
	}
	if err := e.hostServices.PubSubPublishBatch(ctx, msgsJSON); err != nil {
		HostLogger(ctx, e.logger).Error("host function pubsub_publish_batch failed", zap.Error(err))
		return 0
	}
	return 1
}

// hDBExecuteV2 is the WASM-callable wrapper for DBExecuteV2 (bug #218).
// Inputs: pointer/length of SQL + JSON args. Returns a packed uint64
// (ptr<<32 | len) pointing to the JSON envelope in guest memory, or 0 on
// host-side failure. The JSON envelope's "error" field carries SQL errors.
func (e *Engine) hDBExecuteV2(ctx context.Context, mod api.Module, queryPtr, queryLen, argsPtr, argsLen uint32) uint64 {
	query, ok := e.executor.ReadFromGuest(mod, queryPtr, queryLen)
	if !ok {
		return 0
	}
	var args []interface{}
	if argsLen > 0 {
		if err := e.executor.UnmarshalJSONFromGuest(mod, argsPtr, argsLen, &args); err != nil {
			HostLogger(ctx, e.logger).Warn("db_execute_v2: failed to unmarshal args", zap.Error(err))
			return 0
		}
	}
	out, err := e.hostServices.DBExecuteV2(ctx, string(query), args)
	if err != nil {
		HostLogger(ctx, e.logger).Warn("host function db_execute_v2 failed", zap.Error(err))
		return 0
	}
	return e.executor.WriteToGuest(ctx, mod, out)
}

// hDBQueryV2 is the WASM-callable wrapper for DBQueryV2 (bug #218).
func (e *Engine) hDBQueryV2(ctx context.Context, mod api.Module, queryPtr, queryLen, argsPtr, argsLen uint32) uint64 {
	query, ok := e.executor.ReadFromGuest(mod, queryPtr, queryLen)
	if !ok {
		return 0
	}
	var args []interface{}
	if argsLen > 0 {
		if err := e.executor.UnmarshalJSONFromGuest(mod, argsPtr, argsLen, &args); err != nil {
			HostLogger(ctx, e.logger).Warn("db_query_v2: failed to unmarshal args", zap.Error(err))
			return 0
		}
	}
	out, err := e.hostServices.DBQueryV2(ctx, string(query), args)
	if err != nil {
		HostLogger(ctx, e.logger).Warn("host function db_query_v2 failed", zap.Error(err))
		return 0
	}
	return e.executor.WriteToGuest(ctx, mod, out)
}

// hDBTransaction is the WASM-callable wrapper for DBTransaction.
// Input: pointer/length of opsJSON ({"ops":[{kind,sql,args},...]}).
// Returns a packed uint64 (ptr<<32 | len) pointing to JSON BatchResult in
// guest memory. A host-level failure carries `error` and `code` on that same
// envelope (bugboard #175); 0 is reserved for the case where the envelope
// itself could not be written into guest memory.
//
// Note the result JSON's `committed` field tells the caller whether the
// writes landed — a return of non-zero does NOT imply commit.
func (e *Engine) hDBTransaction(ctx context.Context, mod api.Module, opsPtr, opsLen uint32) uint64 {
	opsJSON, ok := e.executor.ReadFromGuest(mod, opsPtr, opsLen)
	if !ok {
		return 0
	}
	out, err := e.hostServices.DBTransaction(ctx, opsJSON)
	if err != nil {
		// Return a structured rejection instead of 0 (bugboard #175).
		//
		// Returning 0 gave the guest an empty buffer with no reason, which
		// surfaced to callers as "host returned empty envelope". A real
		// namespace outage was diagnosed that way: a group write exceeding
		// MaxBatchOps failed deterministically, and neither the function nor
		// its operators could see why — the reason existed only in the
		// gateway's own journal, which tenants cannot read.
		HostLogger(ctx, e.logger).Warn("host function db_transaction failed", zap.Error(err))
		return e.writeBatchRejection(ctx, mod, "db_transaction", err)
	}
	return e.executor.WriteToGuest(ctx, mod, out)
}

// batchRejectionPayload builds the JSON envelope a batched database host
// function returns when it fails at the host level (bugboard #175).
//
// One shape serves all three (db_transaction, db_query_batch,
// exec_and_publish): rqlite.BatchResult decodes cleanly into each of their
// success structs, since encoding/json ignores fields a caller does not
// declare. `committed` is false and `results` is an empty array rather than
// null, so a guest that iterates results without a nil check still works.
func batchRejectionPayload(cause error) ([]byte, error) {
	return json.Marshal(rqlite.BatchResult{
		Results:   []rqlite.OpResult{},
		Committed: false,
		Error:     cause.Error(),
		Code:      rqlite.ClassifyBatchError(cause),
	})
}

// writeBatchRejection writes that envelope into guest memory, so a WASM caller
// reading the normal result sees committed=false plus a classified reason
// instead of an empty buffer.
//
// Returning nothing was the whole of bugboard #175: "host returned empty
// envelope" cannot be told apart from a deadline, an oversized payload, a
// rejected Raft entry or a node that died mid-commit, and the reason existed
// only in the gateway journal, which tenants cannot read. Falls back to 0 only
// when the envelope itself cannot be written, which is genuinely no signal.
func (e *Engine) writeBatchRejection(ctx context.Context, mod api.Module, hostFn string, cause error) uint64 {
	payload, mErr := batchRejectionPayload(cause)
	if mErr != nil {
		HostLogger(ctx, e.logger).Error("failed to marshal host function rejection envelope",
			zap.String("host_fn", hostFn), zap.Error(mErr))
		return 0
	}
	return e.executor.WriteToGuest(ctx, mod, payload)
}

// hDBQueryBatch is the WASM-callable wrapper for DBQueryBatch.
// Input: pointer/length of opsJSON ({"ops":[{"sql":"...","args":[...]}, ...]}).
// Returns a packed uint64 (ptr<<32 | len) pointing to JSON result in guest
// memory. A host-level failure returns an envelope carrying `error` and `code`
// (bugboard #175), never an empty buffer; 0 is reserved for the case where the
// envelope itself could not be written into guest memory.
//
// Per-query errors are surfaced inside the JSON result (one entry per op
// has its own `error` field).
func (e *Engine) hDBQueryBatch(ctx context.Context, mod api.Module, opsPtr, opsLen uint32) uint64 {
	opsJSON, ok := e.executor.ReadFromGuest(mod, opsPtr, opsLen)
	if !ok {
		return 0
	}
	out, err := e.hostServices.DBQueryBatch(ctx, opsJSON)
	if err != nil {
		// Structured rejection rather than an empty buffer (bugboard #175).
		HostLogger(ctx, e.logger).Warn("host function db_query_batch failed", zap.Error(err))
		return e.writeBatchRejection(ctx, mod, "db_query_batch", err)
	}
	return e.executor.WriteToGuest(ctx, mod, out)
}

// hExecAndPublish is the WASM-callable wrapper for ExecAndPublish.
// Inputs:
//
//	opsPtr/opsLen     — JSON {"ops":[{kind,sql,args},...]}
//	topicPtr/topicLen — UTF-8 PubSub topic for the wake-up
//	dataPtr/dataLen   — wake-up payload bytes; "{{seq}}" will be substituted
//
// Returns a packed uint64 (ptr<<32 | len) pointing to the JSON result in
// guest memory. The result JSON has fields committed/seq/published/
// publish_error that the caller inspects; a host-level failure carries `error`
// and `code` instead (bugboard #175). 0 is reserved for the case where the
// envelope itself could not be written into guest memory.
func (e *Engine) hExecAndPublish(ctx context.Context, mod api.Module,
	opsPtr, opsLen, topicPtr, topicLen, dataPtr, dataLen uint32) uint64 {

	opsJSON, ok := e.executor.ReadFromGuest(mod, opsPtr, opsLen)
	if !ok {
		return 0
	}
	topic, ok := e.executor.ReadFromGuest(mod, topicPtr, topicLen)
	if !ok {
		return 0
	}
	data, ok := e.executor.ReadFromGuest(mod, dataPtr, dataLen)
	if !ok {
		return 0
	}
	out, err := e.hostServices.ExecAndPublish(ctx, opsJSON, string(topic), data)
	if err != nil {
		// Structured rejection rather than an empty buffer (bugboard #175).
		HostLogger(ctx, e.logger).Warn("host function exec_and_publish failed",
			zap.String("topic", string(topic)),
			zap.Error(err))
		return e.writeBatchRejection(ctx, mod, "exec_and_publish", err)
	}
	return e.executor.WriteToGuest(ctx, mod, out)
}

// hWSPubSubBridge is the WASM-callable wrapper for WSPubSubBridge.
// Inputs: clientID + topic strings. Returns 1 on success, 0 on error.
func (e *Engine) hWSPubSubBridge(ctx context.Context, mod api.Module,
	cidPtr, cidLen, topicPtr, topicLen uint32) uint32 {
	cid, ok := e.executor.ReadFromGuest(mod, cidPtr, cidLen)
	if !ok {
		return 0
	}
	topic, ok := e.executor.ReadFromGuest(mod, topicPtr, topicLen)
	if !ok {
		return 0
	}
	if err := e.hostServices.WSPubSubBridge(ctx, string(cid), string(topic)); err != nil {
		HostLogger(ctx, e.logger).Warn("ws_pubsub_bridge failed",
			zap.String("client_id", string(cid)),
			zap.String("topic", string(topic)),
			zap.Error(err))
		return 0
	}
	return 1
}

// hWSPubSubUnbridge is the WASM-callable wrapper for WSPubSubUnbridge.
func (e *Engine) hWSPubSubUnbridge(ctx context.Context, mod api.Module,
	cidPtr, cidLen, topicPtr, topicLen uint32) uint32 {
	cid, ok := e.executor.ReadFromGuest(mod, cidPtr, cidLen)
	if !ok {
		return 0
	}
	topic, ok := e.executor.ReadFromGuest(mod, topicPtr, topicLen)
	if !ok {
		return 0
	}
	if err := e.hostServices.WSPubSubUnbridge(ctx, string(cid), string(topic)); err != nil {
		HostLogger(ctx, e.logger).Warn("ws_pubsub_unbridge failed",
			zap.String("client_id", string(cid)),
			zap.String("topic", string(topic)),
			zap.Error(err))
		return 0
	}
	return 1
}

// hFunctionInvoke is the WASM-callable wrapper for FunctionInvoke. Used by
// the rpc-router persistent function (and any future dispatcher) to run
// another function in the same namespace synchronously and forward its
// output back to its caller.
//
// Inputs:
//
//	namePtr/nameLen       — UTF-8 target function name
//	payloadPtr/payloadLen — raw input bytes for the target function
//
// Returns a packed uint64 (ptr<<32 | len) pointing to the target's output
// bytes in guest memory, or 0 on error. The caller is expected to JSON-
// decode the output (target functions ack with JSON envelopes).
func (e *Engine) hFunctionInvoke(ctx context.Context, mod api.Module,
	namePtr, nameLen, payloadPtr, payloadLen uint32) uint64 {
	name, payload, ok := e.readInvokeArgs(ctx, mod, namePtr, nameLen, payloadPtr, payloadLen)
	if !ok {
		return 0
	}
	out, err := e.hostServices.FunctionInvoke(ctx, string(name), payload)
	RecordInvokeOutcome(ctx, err)
	if err != nil {
		// The return value can only say "no result" (0); the guest reads why
		// from function_invoke_last_error (bugboard #263). The host log keeps
		// an unauthorized nested invoke apart as well: it is a permanent
		// misconfiguration that will silently no-op forever, not something a
		// retry fixes. AnChat's cron reconciler reported SUCCESS while settling
		// nothing for weeks because this was one undifferentiated Warn.
		if IsUnauthorized(err) {
			HostLogger(ctx, e.logger).Error("function_invoke unauthorized",
				zap.String("name", string(name)),
				zap.Error(err))
		} else {
			HostLogger(ctx, e.logger).Warn("function_invoke failed",
				zap.String("name", string(name)),
				zap.Error(err))
		}
		return 0
	}
	packed := e.executor.WriteToGuest(ctx, mod, out)
	if packed == 0 && len(out) > 0 {
		RecordInvokeOutcome(ctx, errors.New("the target's output could not be written into guest memory"))
	}
	return packed
}

// readInvokeArgs reads function_invoke's name and payload from guest memory,
// recording a failed read for function_invoke_last_error.
func (e *Engine) readInvokeArgs(ctx context.Context, mod api.Module, namePtr, nameLen, payloadPtr, payloadLen uint32) ([]byte, []byte, bool) {
	name, ok := e.executor.ReadFromGuest(mod, namePtr, nameLen)
	if !ok {
		RecordInvokeArgumentError(ctx, "the function name could not be read from guest memory")
		return nil, nil, false
	}
	payload, ok := e.executor.ReadFromGuest(mod, payloadPtr, payloadLen)
	if !ok {
		RecordInvokeArgumentError(ctx, "the payload could not be read from guest memory")
		return nil, nil, false
	}
	return name, payload, true
}

// hFunctionInvokeLastError returns why the invocation's last function_invoke
// or function_invoke_async call failed, as JSON {"error","code"} (codes:
// InvokeCode*), packed ptr<<32|len; 0 when that call succeeded or none was
// made in this invocation (bugboard #263). The slot is the invocation's own
// (WithInvokeErrorSlot), so it never reports another invocation's call.
// function_invoke returns 0 both for "no output" and for a failure; a guest
// that needs to know which calls this after a 0.
func (e *Engine) hFunctionInvokeLastError(ctx context.Context, mod api.Module) uint64 {
	out := LastInvokeErrorJSON(ctx)
	if out == nil {
		return 0
	}
	return e.executor.WriteToGuest(ctx, mod, out)
}

// hFunctionInvokeAsync is the WASM-callable wrapper for FunctionInvokeAsync.
// Fire-and-forget: it dispatches the target function to run concurrently and
// returns immediately so the caller's frame loop isn't blocked on the target's
// I/O. The target inherits the caller's identity (incl. WS client ID) and is
// expected to deliver its own result to the client via ws_send.
//
// Inputs mirror hFunctionInvoke (name + payload pointers). Returns 1 when the
// invocation was ACCEPTED (queued), 0 on a read failure or backpressure
// rejection — the guest can fall back to a synchronous function_invoke or
// surface "busy" to the client.
func (e *Engine) hFunctionInvokeAsync(ctx context.Context, mod api.Module,
	namePtr, nameLen, payloadPtr, payloadLen uint32) uint32 {
	name, payload, ok := e.readInvokeArgs(ctx, mod, namePtr, nameLen, payloadPtr, payloadLen)
	if !ok {
		return 0
	}
	// Only acceptance is recorded: the target runs after this call returns,
	// so its own failure reaches the host log, not the guest.
	err := e.hostServices.FunctionInvokeAsync(ctx, string(name), payload)
	RecordInvokeOutcome(ctx, err)
	if err != nil {
		HostLogger(ctx, e.logger).Warn("function_invoke_async rejected",
			zap.String("name", string(name)),
			zap.Error(err))
		return 0
	}
	return 1
}

// hWSSend is the WASM-callable wrapper for WSSend.
// Inputs: clientID + raw frame bytes. clientID may be empty — in that case
// the host falls back to the current invocation's WS client (if any).
// Returns 1 on success, 0 on error.
func (e *Engine) hWSSend(ctx context.Context, mod api.Module,
	cidPtr, cidLen, dataPtr, dataLen uint32) uint32 {
	cid, ok := e.executor.ReadFromGuest(mod, cidPtr, cidLen)
	if !ok {
		return 0
	}
	data, ok := e.executor.ReadFromGuest(mod, dataPtr, dataLen)
	if !ok {
		return 0
	}
	if err := e.hostServices.WSSend(ctx, string(cid), data); err != nil {
		HostLogger(ctx, e.logger).Warn("ws_send failed",
			zap.String("client_id", string(cid)),
			zap.Error(err))
		return 0
	}
	return 1
}

// hWSBroadcast is the WASM-callable wrapper for WSBroadcast.
// Inputs: topic + raw frame bytes. Sends `data` to every WS client currently
// subscribed to `topic` in the function's namespace. Returns 1 on success,
// 0 on error.
func (e *Engine) hWSBroadcast(ctx context.Context, mod api.Module,
	topicPtr, topicLen, dataPtr, dataLen uint32) uint32 {
	topic, ok := e.executor.ReadFromGuest(mod, topicPtr, topicLen)
	if !ok {
		return 0
	}
	data, ok := e.executor.ReadFromGuest(mod, dataPtr, dataLen)
	if !ok {
		return 0
	}
	if err := e.hostServices.WSBroadcast(ctx, string(topic), data); err != nil {
		HostLogger(ctx, e.logger).Warn("ws_broadcast failed",
			zap.String("topic", string(topic)),
			zap.Error(err))
		return 0
	}
	return 1
}

// hEphemeralStateSet is the WASM-callable wrapper for EphemeralStateSet —
// bugboard #710 WS-subscribe-tracked ephemeral state.
//
// ABI: ephemeral_state_set(topicPtr, topicLen, keyPtr, keyLen, payloadPtr,
// payloadLen uint32, ttlMs int64) -> uint32. Returns 1 on success, 0 on
// failure (no WS client in context, empty topic/key, oversized payload,
// per-client key cap, or a guest-memory read error).
func (e *Engine) hEphemeralStateSet(ctx context.Context, mod api.Module,
	topicPtr, topicLen, keyPtr, keyLen, payloadPtr, payloadLen uint32, ttlMs int64) uint32 {
	topic, ok := e.executor.ReadFromGuest(mod, topicPtr, topicLen)
	if !ok {
		return 0
	}
	key, ok := e.executor.ReadFromGuest(mod, keyPtr, keyLen)
	if !ok {
		return 0
	}
	var payload []byte
	if payloadLen > 0 {
		p, ok := e.executor.ReadFromGuest(mod, payloadPtr, payloadLen)
		if !ok {
			return 0
		}
		payload = p
	}
	if err := e.hostServices.EphemeralStateSet(ctx, string(topic), string(key), payload, ttlMs); err != nil {
		HostLogger(ctx, e.logger).Warn("host function ephemeral_state_set failed",
			zap.String("topic", string(topic)),
			zap.String("key", string(key)),
			zap.Error(err))
		return 0
	}
	return 1
}

// hEphemeralStateClear is the WASM-callable wrapper for EphemeralStateClear.
//
// ABI: ephemeral_state_clear(topicPtr, topicLen, keyPtr, keyLen uint32) ->
// uint32. Returns 1 on success (including idempotent clears of a missing key),
// 0 on failure (no WS client in context, empty topic/key, or a guest-memory
// read error).
func (e *Engine) hEphemeralStateClear(ctx context.Context, mod api.Module,
	topicPtr, topicLen, keyPtr, keyLen uint32) uint32 {
	topic, ok := e.executor.ReadFromGuest(mod, topicPtr, topicLen)
	if !ok {
		return 0
	}
	key, ok := e.executor.ReadFromGuest(mod, keyPtr, keyLen)
	if !ok {
		return 0
	}
	if err := e.hostServices.EphemeralStateClear(ctx, string(topic), string(key)); err != nil {
		HostLogger(ctx, e.logger).Warn("host function ephemeral_state_clear failed",
			zap.String("topic", string(topic)),
			zap.String("key", string(key)),
			zap.Error(err))
		return 0
	}
	return 1
}

// hEphemeralStateList is the WASM-callable wrapper for EphemeralStateList —
// the bugboard #710 reconnect catch-up read.
//
// ABI: ephemeral_state_list(topicPtr, topicLen uint32) -> uint64 packed
// (ptr<<32 | len) pointing to a JSON envelope in guest memory:
//
//	{"entries":[{"key":..,"client_id":..,"payload":<base64>,"expires_in_ms":..}, …]}
//
// Returns 0 on failure (empty topic, no invocation context, ephemeral state
// unavailable, or a guest-memory error). Unlike set/clear, no WS client is
// required — the read is namespace-scoped via the invocation context.
func (e *Engine) hEphemeralStateList(ctx context.Context, mod api.Module,
	topicPtr, topicLen uint32) uint64 {
	topic, ok := e.executor.ReadFromGuest(mod, topicPtr, topicLen)
	if !ok {
		return 0
	}
	out, err := e.hostServices.EphemeralStateList(ctx, string(topic))
	if err != nil {
		HostLogger(ctx, e.logger).Warn("host function ephemeral_state_list failed",
			zap.String("topic", string(topic)),
			zap.Error(err))
		return 0
	}
	return e.executor.WriteToGuest(ctx, mod, out)
}

// hPushSend is the WASM-callable wrapper for PushSend.
// Inputs:
//
//	userIDPtr/userIDLen — UTF-8 user ID to push to (within the function's
//	                      own namespace; the namespace is server-side trusted)
//	msgPtr/msgLen       — JSON payload matching hostfunctions.PushSendArgs
//
// Returns 1 on success, 0 on error.
func (e *Engine) hPushSend(ctx context.Context, mod api.Module,
	userIDPtr, userIDLen, msgPtr, msgLen uint32) uint32 {
	userID, ok := e.executor.ReadFromGuest(mod, userIDPtr, userIDLen)
	if !ok {
		return 0
	}
	msgJSON, ok := e.executor.ReadFromGuest(mod, msgPtr, msgLen)
	if !ok {
		return 0
	}
	if err := e.hostServices.PushSend(ctx, string(userID), msgJSON); err != nil {
		HostLogger(ctx, e.logger).Error("host function push_send failed",
			zap.String("user_id", string(userID)),
			zap.Error(err))
		return 0
	}
	return 1
}

// hPushSendV2 is the WASM-callable wrapper for PushSendV2 — the
// rich-result push host function. Returns a packed uint64
// (ptr<<32 | len) pointing to a JSON envelope in guest memory, or 0
// on setup/validation error.
//
// The JSON envelope is push.SendDetailedResult: top-level Ok bool,
// per-device Results with HTTP status / reason / unregistered flag.
// Callers MUST parse it — a non-zero return does NOT mean every
// device succeeded (read result.ok or iterate results[]).
//
// Bugboard #348: replaces the binary success/fail of PushSend with
// the full per-device truth so WASM callers can react granularly.
func (e *Engine) hPushSendV2(ctx context.Context, mod api.Module,
	userIDPtr, userIDLen, msgPtr, msgLen uint32) uint64 {
	userID, ok := e.executor.ReadFromGuest(mod, userIDPtr, userIDLen)
	if !ok {
		return 0
	}
	msgJSON, ok := e.executor.ReadFromGuest(mod, msgPtr, msgLen)
	if !ok {
		return 0
	}
	out, err := e.hostServices.PushSendV2(ctx, string(userID), msgJSON)
	if err != nil {
		HostLogger(ctx, e.logger).Warn("host function push_send_v2 failed",
			zap.String("user_id", string(userID)),
			zap.Error(err))
		return 0
	}
	return e.executor.WriteToGuest(ctx, mod, out)
}

// hPushSendTopic is the WASM-callable wrapper for PushSendTopic (FEAT-265):
// push to the device registered under a rotating topic id. Same result
// convention as push_send_v2 — a packed uint64 (ptr<<32 | len) of the JSON
// envelope, or 0 on a setup/validation error. The topic id is not logged.
func (e *Engine) hPushSendTopic(ctx context.Context, mod api.Module,
	topicIDPtr, topicIDLen, msgPtr, msgLen uint32) uint64 {
	topicID, ok := e.executor.ReadFromGuest(mod, topicIDPtr, topicIDLen)
	if !ok {
		return 0
	}
	msgJSON, ok := e.executor.ReadFromGuest(mod, msgPtr, msgLen)
	if !ok {
		return 0
	}
	out, err := e.hostServices.PushSendTopic(ctx, string(topicID), msgJSON)
	if err != nil {
		HostLogger(ctx, e.logger).Warn("host function push_send_topic failed", zap.Error(err))
		return 0
	}
	return e.executor.WriteToGuest(ctx, mod, out)
}

// hTurnCredentials is the WASM-callable wrapper for TurnCredentials —
// feat-9. Takes no args (namespace derived from invocation context),
// returns packed uint64 (ptr<<32 | len) pointing to a JSON envelope in
// guest memory, or 0 on setup error.
//
// The envelope shape is documented at turn.go:turnCredentialsEnvelope.
// Callers MUST parse it — a non-zero return doesn't imply TURN is
// configured (check envelope.configured before using credentials).
func (e *Engine) hTurnCredentials(ctx context.Context, mod api.Module) uint64 {
	out, err := e.hostServices.TurnCredentials(ctx)
	if err != nil {
		HostLogger(ctx, e.logger).Warn("host function turn_credentials failed",
			zap.Error(err))
		return 0
	}
	return e.executor.WriteToGuest(ctx, mod, out)
}

// maxLogMessageBytes caps a single oh.LogInfo/LogError message. A guest could
// otherwise pass its entire linear memory as one "log line", ballooning the
// per-invocation buffer (and the async invocation-log queue holding it).
// Truncation, not rejection — telemetry is best-effort.
const maxLogMessageBytes = 16 * 1024

func (e *Engine) hLogInfo(ctx context.Context, mod api.Module, ptr, size uint32) {
	if size > maxLogMessageBytes {
		size = maxLogMessageBytes
	}
	msg, ok := e.executor.ReadFromGuest(mod, ptr, size)
	if ok {
		e.hostServices.LogInfo(ctx, string(msg))
	}
}

func (e *Engine) hLogError(ctx context.Context, mod api.Module, ptr, size uint32) {
	if size > maxLogMessageBytes {
		size = maxLogMessageBytes
	}
	msg, ok := e.executor.ReadFromGuest(mod, ptr, size)
	if ok {
		e.hostServices.LogError(ctx, string(msg))
	}
}
