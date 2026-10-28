package serverless

import (
	"context"
	"sync/atomic"
)

// invCtxKey is the unexported context-value key used to attach an
// InvocationContext to a Go context. The empty struct is the standard
// Go pattern for context keys (avoids string-collision risk).
type invCtxKey struct{}

// WithInvocationContext returns a derived ctx that carries invCtx. Host
// function accessors check the ctx FIRST and only fall back to the
// HostFunctions singleton field when nothing is carried on ctx.
//
// Why this exists: HostFunctions is a process-wide singleton (one per
// gateway engine). Its `invCtx` field is shared across all WASM instances.
// For STATELESS functions the gateway sets/clears that field per-call
// (executor contextSetter/contextClearer), but the lock is released
// before WASM runs — two concurrent invocations CAN race on the field,
// and one's host call CAN read the other's identity.
//
// For PERSISTENT WS functions the race is far worse: the field used to be
// bound ONCE at instantiation and reused for the connection's lifetime.
// Two simultaneous persistent WS connections from different users
// overwrote each other's invCtx, and every subsequent function_invoke /
// GetCallerJWTSubject / GetSecret call from inside the WASM read whatever
// was bound LAST — silently leaking identity across tenants.
//
// The fix is per-call invCtx propagation through Go's context.Context.
// wazero passes the ctx given to api.Function.Call all the way through
// to host function callbacks (engine.go's host-function wrappers receive
// it), so every WASM-host hop carries its own invCtx and never reads the
// shared field.
//
// Persistent WS uses this exclusively (see persistent.Instance, which
// wraps every export call's ctx with the per-instance invCtx).
//
// Stateless Engine.Execute also attaches invCtx via this helper since
// bugboard #348 — AnChat's pubsub-triggered message-push-handler
// confirmed the "microseconds" race window was actually observable
// under production fan-out load: concurrent invocations either
// cross-tenant-leaked the namespace (silent) or saw a nil singleton
// during the brief window between contextSetter on one goroutine and
// contextClearer on another, producing "no namespace in invocation
// context" errors at host-fn entry. The singleton SetInvocationContext
// path remains in place as defense-in-depth — every host fn resolves
// via currentInvocationContext, which prefers ctx-attached over the
// singleton field, so the race is closed for the live path.
func WithInvocationContext(ctx context.Context, invCtx *InvocationContext) context.Context {
	if invCtx == nil {
		return ctx
	}
	return context.WithValue(ctx, invCtxKey{}, invCtx)
}

// InvocationContextFromCtx extracts the invCtx attached via
// WithInvocationContext, or nil if none is present. Exported so the
// hostfunctions package and any other consumer can read it without
// duplicating the key type.
func InvocationContextFromCtx(ctx context.Context) *InvocationContext {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(invCtxKey{}).(*InvocationContext)
	return v
}

// publishCounterKey is the unexported context-value key for the per-invocation
// pubsub publish counter.
type publishCounterKey struct{}

// publishCounter tracks how many pubsub messages a single invocation has
// published, so the host layer can cap intra-invocation publish volume. It
// rides the invocation's context (same per-call propagation model as
// InvocationContext) so concurrent invocations each get their own counter.
type publishCounter struct{ n atomic.Int64 }

// WithPublishCounter returns a derived ctx carrying a FRESH per-invocation
// publish counter. Engine.Execute (stateless) and the persistent WS frame
// handler attach this so the pubsub host functions can bound how many messages
// one invocation publishes — the WASM runtime has no fuel metering and the
// rate limiter only gates invocation FREQUENCY, not per-invocation host-call
// volume, so without this a single admitted invocation could flood the shared
// gossipsub router (amplified to every peer by FloodPublish).
func WithPublishCounter(ctx context.Context) context.Context {
	return context.WithValue(ctx, publishCounterKey{}, &publishCounter{})
}

// AddPublishCount adds n to the invocation's publish counter and returns the
// new running total. Returns -1 when the ctx carries no counter (an untracked
// path) so callers can skip enforcement rather than reject.
func AddPublishCount(ctx context.Context, n int) int64 {
	if ctx == nil || n <= 0 {
		return -1
	}
	if pc, ok := ctx.Value(publishCounterKey{}).(*publishCounter); ok {
		return pc.n.Add(int64(n))
	}
	return -1
}

// webrtcCounterKey is the unexported context-value key for the per-invocation
// webrtc_* call counter.
type webrtcCounterKey struct{}

// webrtcCounter tracks how many webrtc_admit, webrtc_kick and webrtc_mute calls
// a single invocation has made.
type webrtcCounter struct{ n atomic.Int64 }

// WithWebRTCCounter returns a derived ctx carrying a FRESH per-invocation
// counter of webrtc_* host calls, for the same reason as WithPublishCounter:
// the rate limiter gates invocation frequency, not host-call volume, and each
// call writes the namespace's database and makes an HTTP call to every SFU.
func WithWebRTCCounter(ctx context.Context) context.Context {
	return context.WithValue(ctx, webrtcCounterKey{}, &webrtcCounter{})
}

// AddWebRTCCount adds one to the invocation's webrtc_* call counter and returns
// the new running total. Returns -1 when the ctx carries no counter (an
// untracked path) so callers can skip enforcement rather than reject.
func AddWebRTCCount(ctx context.Context) int64 {
	if ctx == nil {
		return -1
	}
	if wc, ok := ctx.Value(webrtcCounterKey{}).(*webrtcCounter); ok {
		return wc.n.Add(1)
	}
	return -1
}

// deviceAdmissionCounterKey is the unexported context-value key for the
// per-invocation approve_device / deny_device call counter.
type deviceAdmissionCounterKey struct{}

// deviceAdmissionCounter tracks how many approve_device and deny_device calls a
// single invocation has made.
type deviceAdmissionCounter struct{ n atomic.Int64 }

// WithDeviceAdmissionCounter returns a derived ctx carrying a FRESH
// per-invocation counter of approve_device and deny_device calls, for the same
// reason as WithWebRTCCounter: each call writes the gateway's device registry.
func WithDeviceAdmissionCounter(ctx context.Context) context.Context {
	return context.WithValue(ctx, deviceAdmissionCounterKey{}, &deviceAdmissionCounter{})
}

// AddDeviceAdmissionCount adds one to the invocation's device-admission call
// counter and returns the new running total. Returns -1 when the ctx carries no
// counter (an untracked path) so callers can skip enforcement rather than
// reject.
func AddDeviceAdmissionCount(ctx context.Context) int64 {
	if ctx == nil {
		return -1
	}
	if dc, ok := ctx.Value(deviceAdmissionCounterKey{}).(*deviceAdmissionCounter); ok {
		return dc.n.Add(1)
	}
	return -1
}
