package persistent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Orama-Network/orama/core/pkg/serverless"
	"github.com/tetratelabs/wazero/api"
	"go.uber.org/zap"
)

// WSOpenInput is the JSON payload passed to the WASM module's ws_open export.
// The WASM module unmarshals this from its input buffer.
type WSOpenInput struct {
	ClientID  string            `json:"client_id"`
	Wallet    string            `json:"wallet,omitempty"`
	Namespace string            `json:"namespace"`
	Headers   map[string]string `json:"headers,omitempty"` // filtered subset
}

// CloseReason explains why a connection is shutting down. Passed to ws_close.
type CloseReason string

const (
	CloseReasonClientDisconnect CloseReason = "client_disconnect"
	CloseReasonServerShutdown   CloseReason = "server_shutdown"
	CloseReasonIdleTimeout      CloseReason = "idle_timeout"
	CloseReasonHandlerError     CloseReason = "handler_error"
	CloseReasonRejected         CloseReason = "rejected_by_open"
)

// SendFunc writes a frame to the underlying WebSocket. Returns an error if
// the connection is closed. The instance forwards ws_send hostfunc calls
// to this function.
type SendFunc func(data []byte) error

// guestFunc is the callable surface of an exported WASM function.
// api.Function satisfies it.
type guestFunc interface {
	Call(ctx context.Context, params ...uint64) ([]uint64, error)
}

// guestMemory is the part of a module's linear memory the instance writes to.
// api.Memory satisfies it.
type guestMemory interface {
	Write(offset uint32, v []byte) bool
}

// guestModule is the instance's ownership handle on the wazero module.
// api.Module satisfies it.
type guestModule interface {
	Close(ctx context.Context) error
}

// Instance owns one WASM module bound to one WebSocket connection.
//
// Lifecycle:
//
//  1. NewInstance — wraps an already-instantiated module
//  2. Open(input) — calls ws_open; non-zero return = reject
//  3. Run(ctx) — drains the inbound channel, calling ws_frame per message;
//     blocks until ctx cancelled or instance closed
//  4. Submit(frame) — enqueues a frame for Run to pick up; returns error if full
//  5. Close(reason) — calls ws_close; closes the wazero instance; idempotent
type Instance struct {
	clientID     string
	functionName string
	namespace    string

	// The wazero handles are held through the narrowest interfaces this type
	// actually uses. api.Module and api.Memory are sealed (wazeroOnly), so
	// depending on them directly makes the instance lifecycle untestable
	// without a full runtime — and the lifecycle is exactly where the
	// process-killing concurrency bug lived.
	module  guestModule // wazero instance, owned by this struct
	openFn  guestFunc   // exported ws_open
	frameFn guestFunc   // exported ws_frame
	closeFn guestFunc   // exported ws_close
	allocFn guestFunc   // orama_alloc / malloc — for input bytes
	memory  guestMemory

	// Per-instance invocation context. Bound at NewInstance time and
	// attached to every WASM-host call's ctx via
	// hostfunctions.WithInvocationContext. This is what makes persistent
	// WS function_invoke / GetCallerJWTSubject / GetSecret race-free
	// across concurrent connections — each instance carries its own
	// caller identity in the ctx, never reading the HostFunctions
	// singleton field. See pkg/serverless/hostfunctions/invocation_context.go.
	//
	// MUTABLE: bug #321 added mid-session re-auth — the WS handler can
	// swap invCtx via UpdateInvocationContext when the client rotates
	// its JWT. invCtxMu guards reads/writes; withInvCtx() takes RLock.
	invCtx   *serverless.InvocationContext
	invCtxMu sync.RWMutex

	inbound chan []byte
	logger  *zap.Logger

	// Per-frame timeout. Bounded by the function's TimeoutSeconds.
	frameTimeout time.Duration

	// callMu serializes EVERY entry into the wazero module — ws_open, ws_frame,
	// ws_close and the final module.Close.
	//
	// wazero instances are not goroutine-safe: the wazevo engine keeps a single
	// manually-managed guest stack per instance and executes guest code on the
	// calling goroutine. Two goroutines inside one instance corrupt that
	// bookkeeping, and the failure is not a returned error — it is a Go runtime
	// fatal (`split stack overflow`, `unknown caller pc`, `bad recovery`) that
	// no recover() can catch, so it kills the whole gateway process and every
	// other WebSocket on the node.
	//
	// The frame loop and Close ran concurrently: teardown cancelled Run's
	// context and then called ws_close immediately, without waiting for an
	// in-flight frame to unwind. Frame execution is bounded by frameTimeout
	// (seconds), so the window was wide open — and it is widest exactly when
	// frames are slow, which is also when clients give up and disconnect.
	callMu sync.Mutex

	// runDone is closed when Run returns, so Close can wait for the frame loop
	// to finish before tearing the module down. runStarted reports whether Run
	// was ever launched — Close must not wait on a loop that will never run
	// (e.g. an instance rejected by ws_open).
	runDone    chan struct{}
	runOnce    sync.Once
	runStarted atomic.Bool

	// Closed exactly once.
	closeOnce sync.Once
	closed    atomic.Bool
}

// signalRunDone marks the frame loop as finished. Idempotent.
func (i *Instance) signalRunDone() {
	i.runOnce.Do(func() { close(i.runDone) })
}

// Config holds knobs for a persistent instance. Zero values use sensible
// defaults; the gateway populates these from the function's metadata.
type Config struct {
	ClientID          string
	FunctionName      string
	Namespace         string
	FrameTimeoutSec   int // 0 = 30s default
	MaxInflightFrames int // 0 = 64 default

	// InvocationContext is attached to every WASM-host call's ctx so the
	// instance's caller identity (JWT subject, wallet, claims, ws client
	// ID) is race-free across concurrent persistent WS connections.
	//
	// REQUIRED. NewInstance returns an error if nil — without it, host
	// functions would fall back to the shared HostFunctions singleton
	// field and re-open the cross-tenant identity leak this whole
	// machinery exists to fix (see pkg/serverless/invocation_context.go).
	InvocationContext *serverless.InvocationContext
}

// NewInstance wraps an already-instantiated wazero module as a persistent
// instance. Returns an error if any of the required exports
// (ws_open, ws_frame, ws_close) are missing.
//
// The caller retains ownership of the module's lifecycle outside of Close —
// that is, when Close is invoked here, the wazero instance is closed.
func NewInstance(module api.Module, cfg Config, logger *zap.Logger) (*Instance, error) {
	// Reject nil invCtx loud and early. A persistent instance without
	// per-call invCtx propagation falls back to the singleton field on
	// every host call, which races across concurrent connections — the
	// exact bug this design exists to prevent. Caller MUST populate.
	if cfg.InvocationContext == nil {
		return nil, fmt.Errorf("persistent: Config.InvocationContext is required (nil would re-open the cross-tenant identity-leak race; see pkg/serverless/invocation_context.go)")
	}

	openFn := module.ExportedFunction("ws_open")
	if openFn == nil {
		return nil, fmt.Errorf("persistent: module missing ws_open export")
	}
	frameFn := module.ExportedFunction("ws_frame")
	if frameFn == nil {
		return nil, fmt.Errorf("persistent: module missing ws_frame export")
	}
	closeFn := module.ExportedFunction("ws_close")
	if closeFn == nil {
		return nil, fmt.Errorf("persistent: module missing ws_close export")
	}
	allocFn := module.ExportedFunction("orama_alloc")
	if allocFn == nil {
		allocFn = module.ExportedFunction("malloc")
	}
	if allocFn == nil {
		return nil, fmt.Errorf("persistent: module missing orama_alloc/malloc export (required to pass payload bytes)")
	}
	memory := module.Memory()
	if memory == nil {
		return nil, fmt.Errorf("persistent: module exports no memory")
	}

	frameTimeout := time.Duration(cfg.FrameTimeoutSec) * time.Second
	if frameTimeout <= 0 {
		frameTimeout = 30 * time.Second
	}
	maxInflight := cfg.MaxInflightFrames
	if maxInflight <= 0 {
		maxInflight = 64
	}

	return &Instance{
		clientID:     cfg.ClientID,
		functionName: cfg.FunctionName,
		namespace:    cfg.Namespace,
		module:       module,
		openFn:       openFn,
		frameFn:      frameFn,
		closeFn:      closeFn,
		allocFn:      allocFn,
		memory:       memory,
		invCtx:       cfg.InvocationContext,
		inbound:      make(chan []byte, maxInflight),
		logger:       logger,
		frameTimeout: frameTimeout,
		runDone:      make(chan struct{}),
	}, nil
}

// withInvCtx returns a derived ctx carrying this instance's invocation
// context. Used by every export call so host functions read identity from
// the per-instance ctx instead of the shared HostFunctions singleton.
//
// Returns ctx unchanged when invCtx is nil — preserves backwards-compat
// for callers that didn't populate Config.InvocationContext.
//
// It also attaches a fresh host log budget (bugboard #424); the caller runs
// the returned summarize when the export call returns.
func (i *Instance) withInvCtx(ctx context.Context) (context.Context, func()) {
	i.invCtxMu.RLock()
	cur := i.invCtx
	i.invCtxMu.RUnlock()
	if cur != nil {
		ctx = serverless.WithInvocationContext(ctx, cur)
	}
	// Fresh per-frame pubsub publish counter so the pubsub host functions can
	// bound how many messages one frame floods onto the shared gossipsub
	// router (scoped per export call, like the rest of withInvCtx).
	ctx = serverless.WithPublishCounter(ctx)
	ctx = serverless.WithWebRTCCounter(ctx)
	ctx = serverless.WithDeviceAdmissionCounter(ctx)
	// Attach a fresh per-call LogBuffer so oh.LogInfo / oh.LogError from
	// inside this ws_open / ws_frame / ws_close call write to a
	// scoped slice instead of the HostFunctions singleton (bugboard
	// #108 fix). Persistent WS doesn't currently persist these logs to
	// function_logs (no logInvocation for persistent frames), so the
	// buffer is discarded when the call returns — the point is to
	// avoid leaking entries into the singleton where a concurrent
	// stateless Execute would otherwise see them.
	ctx = serverless.WithLogBuffer(ctx, serverless.NewLogBuffer())
	ctx, budget := serverless.WithHostLogBudget(ctx)
	// A fresh function_invoke_last_error slot per call, so one frame never
	// reads the error of a nested invoke another frame made.
	ctx = serverless.WithInvokeErrorSlot(ctx)
	summarize := func() {
		fields := []zap.Field{zap.String("client_id", i.clientID)}
		if cur != nil {
			fields = append(fields, zap.String("namespace", cur.Namespace), zap.String("function", cur.FunctionName))
		}
		budget.Summarize(i.logger, fields...)
	}
	return ctx, summarize
}

// UpdateInvocationContext atomically swaps the per-instance invocation
// context. Used by the WS handler to apply a mid-session JWT rotation
// (bugboard #321 — `__orama:auth.refresh` control frame) so the
// client's new JWT subject / wallet / claims propagate to every
// subsequent host call WITHOUT tearing down the WS.
//
// Thread-safe: callers can call this from the WS read loop while the
// frame-processing goroutine is concurrently reading the field via
// withInvCtx. The swap is a single pointer-write under a write lock;
// in-flight host calls that already wrapped their ctx with the OLD
// invCtx keep using the old identity until they return — that's
// correct (an in-flight invocation should complete under the identity
// it started with, not get swapped mid-call).
//
// Rejects nil to preserve the "invCtx is required" invariant baked in
// at NewInstance. A nil swap would silently re-open the cross-tenant
// race documented in pkg/serverless/invocation_context.go.
func (i *Instance) UpdateInvocationContext(newInvCtx *serverless.InvocationContext) error {
	if newInvCtx == nil {
		return fmt.Errorf("persistent: UpdateInvocationContext: nil invCtx (would re-open the cross-tenant identity-leak race)")
	}
	i.invCtxMu.Lock()
	i.invCtx = newInvCtx
	i.invCtxMu.Unlock()
	return nil
}

// CurrentInvocationContext returns the per-instance invocation context
// snapshot (the same pointer withInvCtx would attach to the next host
// call's ctx). Used by the WS handler to audit identity transitions on
// mid-session JWT refresh (bug #321) without re-reading from the lock.
// May return nil if the instance was constructed without an invCtx.
func (i *Instance) CurrentInvocationContext() *serverless.InvocationContext {
	i.invCtxMu.RLock()
	defer i.invCtxMu.RUnlock()
	return i.invCtx
}

// ClientID returns the WebSocket client ID this instance serves.
func (i *Instance) ClientID() string { return i.clientID }

// Open invokes ws_open with the given input. Returns an error if the WASM
// returns non-zero (connection rejected) or the call traps.
func (i *Instance) Open(ctx context.Context, input WSOpenInput) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("persistent.Open: marshal input: %w", err)
	}
	callCtx, summarize := i.withInvCtx(ctx)
	defer summarize()
	ctx, cancel := context.WithTimeout(callCtx, i.frameTimeout)
	defer cancel()

	rc, err := i.callExport(ctx, i.openFn, payload)
	if err != nil {
		return fmt.Errorf("persistent.Open: ws_open trap: %w", err)
	}
	if rc != 0 {
		return fmt.Errorf("persistent.Open: ws_open rejected (rc=%d)", rc)
	}
	return nil
}

// Submit enqueues a frame for Run to process. Non-blocking: returns an
// error if the inbound channel is full (caller should drop the connection).
func (i *Instance) Submit(frame []byte) error {
	if i.closed.Load() {
		return fmt.Errorf("persistent.Submit: instance closed")
	}
	select {
	case i.inbound <- frame:
		return nil
	default:
		return fmt.Errorf("persistent.Submit: inbound queue full (max %d)", cap(i.inbound))
	}
}

// Run drains the inbound channel, invoking ws_frame per message. Blocks
// until ctx is cancelled, Close is called, or a frame returns "close".
//
// Frames are processed serially, in arrival order, and that is the contract
// the functions docs give apps (bug-963): wazero instances are not
// goroutine-safe, and wazero cannot set a call aside while it waits in a host
// function, so a frame blocked in function_invoke or a database read holds
// every later frame of the socket. One instance per socket is also what makes
// the function's memory its per-connection state. Concurrency is the
// function's to ask for: function_invoke_async returns at once and runs the
// target in the namespace's execution slots, which bound it per gateway rather
// than per socket.
func (i *Instance) Run(ctx context.Context) {
	i.runStarted.Store(true)
	// Signalled on every exit path so Close never waits on a loop that has
	// already stopped.
	defer i.signalRunDone()

	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-i.inbound:
			if !ok {
				return
			}
			if err := i.handleFrame(ctx, frame); err != nil {
				i.logger.Warn("persistent ws_frame error",
					zap.String("client_id", i.clientID),
					zap.String("function", i.functionName),
					zap.Error(err),
				)
				// Frame errors trigger close — caller should disconnect WS.
				return
			}
		}
	}
}

func (i *Instance) handleFrame(ctx context.Context, frame []byte) error {
	callCtx, summarize := i.withInvCtx(ctx)
	defer summarize()
	frameCtx, cancel := context.WithTimeout(callCtx, i.frameTimeout)
	defer cancel()

	rc, err := i.callExport(frameCtx, i.frameFn, frame)
	if err != nil {
		return fmt.Errorf("ws_frame trap: %w", err)
	}
	if rc == 1 {
		return fmt.Errorf("ws_frame requested close")
	}
	return nil
}

// Close invokes ws_close (best-effort, bounded by frameTimeout) then
// shuts down the wazero instance. Idempotent and safe to call multiple times.
func (i *Instance) Close(ctx context.Context, reason CloseReason) {
	i.closeOnce.Do(func() {
		i.closed.Store(true)
		// The inbound channel is deliberately NOT closed, and there is no
		// drain goroutine.
		//
		// Closing it raced Submit: the closed-flag check and the send are not
		// atomic, so a frame arriving during teardown could send on an
		// already-closed channel — an unrecoverable panic in the WebSocket
		// read loop. The drain goroutine that motivated the close was itself
		// unnecessary, because Submit is non-blocking (select with default),
		// so no sender is ever parked on this channel.
		//
		// Leaving it open is safe: once Run has returned nothing reads it, any
		// buffered frames are dropped, and the channel is reclaimed with the
		// instance.

		// Wait for the frame loop to unwind before entering the module again.
		//
		// The caller cancels Run's context just before calling us, but
		// cancellation is asynchronous: a frame already executing inside wazero
		// keeps running until the guest yields. Entering the module for
		// ws_close while that frame is still in flight is a concurrent entry
		// into a non-goroutine-safe instance, which crashes the PROCESS rather
		// than failing the request (see callMu).
		//
		// callMu alone would make that safe, but waiting also preserves the
		// ABI's ordering guarantee — ws_close is the last callback a function
		// sees, after the final ws_frame. The wait is bounded so a wedged guest
		// delays teardown instead of leaking the goroutine; callMu still holds
		// the safety property if the wait times out.
		i.waitForRunLoop()

		// Best-effort ws_close — don't propagate errors; we're shutting down.
		callCtx, summarize := i.withInvCtx(ctx)
		defer summarize()
		closeCtx, cancel := context.WithTimeout(callCtx, i.frameTimeout)
		defer cancel()
		if _, err := i.callExport(closeCtx, i.closeFn, []byte(reason)); err != nil {
			i.logger.Debug("persistent ws_close ignored error",
				zap.String("client_id", i.clientID),
				zap.Error(err),
			)
		}
		// module.Close tears down the same instance the frame loop executes in,
		// so it takes callMu too.
		i.callMu.Lock()
		err := i.module.Close(context.Background())
		i.callMu.Unlock()
		if err != nil {
			i.logger.Debug("persistent module.Close error",
				zap.String("client_id", i.clientID),
				zap.Error(err))
		}
	})
}

// runLoopDrainTimeout bounds how long Close waits for the frame loop to unwind
// before proceeding anyway. Sized above the per-frame timeout so a normal
// in-flight frame always gets to finish; past that the guest is wedged and
// teardown must not be held hostage (callMu still guarantees safety).
const runLoopDrainGrace = 2 * time.Second

// waitForRunLoop blocks until Run has returned, or until the frame timeout plus
// a small grace elapses. Returns immediately when Run was never started.
func (i *Instance) waitForRunLoop() {
	if !i.runStarted.Load() {
		return
	}
	timer := time.NewTimer(i.frameTimeout + runLoopDrainGrace)
	defer timer.Stop()
	select {
	case <-i.runDone:
	case <-timer.C:
		i.logger.Warn("persistent frame loop did not stop before teardown deadline; closing anyway",
			zap.String("client_id", i.clientID),
			zap.String("function", i.functionName),
		)
	}
}

// callExport allocates space in the guest, copies `payload` into it, calls
// the export with (ptr, len), then returns the export's first return value
// (or 0 if void).
//
// It does NOT free the allocated memory — the WASM module is short-lived
// at the per-instance level, and we rely on the module being closed at
// session end. For long-running instances with very high frame rates,
// memory growth is bounded by the function's memory_limit_mb.
func (i *Instance) callExport(ctx context.Context, fn guestFunc, payload []byte) (uint32, error) {
	// Single entry point into the module — see callMu. Every export call and
	// the module teardown funnel through this lock, so a wazero instance is
	// only ever executing on one goroutine at a time.
	i.callMu.Lock()
	defer i.callMu.Unlock()

	var ptr uint64
	if len(payload) > 0 {
		results, err := i.allocFn.Call(ctx, uint64(len(payload)))
		if err != nil {
			return 0, fmt.Errorf("alloc: %w", err)
		}
		ptr = results[0]
		if !i.memory.Write(uint32(ptr), payload) {
			return 0, fmt.Errorf("memory write failed (oom?)")
		}
	}
	results, err := fn.Call(ctx, ptr, uint64(len(payload)))
	if err != nil {
		return 0, err
	}
	if len(results) == 0 {
		return 0, nil
	}
	return uint32(results[0]), nil
}
