// Package persistent implements long-lived per-WebSocket WASM function
// instances. A persistent function is bound to one WS connection for its
// entire lifetime — its WASM module is instantiated once at upgrade,
// retains memory across frames, and is torn down on disconnect.
//
// See plan: core/plans/platform/06_PERSISTENT_WS_FUNCTIONS.md
//
// ABI: persistent functions export three WASM functions instead of using
// the default _start:
//
//	ws_open(payloadPtr, payloadLen) → uint32   // 0 = accept, 1 = reject
//	ws_frame(payloadPtr, payloadLen) → uint32  // 0 = ok, 1 = close
//	ws_close(reasonPtr, reasonLen) → void
//
// The host calls each export at the appropriate lifecycle point. Frames
// are processed serially per connection, in arrival order — wazero instances
// are NOT goroutine-safe and cannot yield inside a host call, so a frame
// blocked in a synchronous host call holds the frames behind it. Slow work
// belongs in function_invoke_async (see Instance.Run).
//
// Replies use the existing ws_send / ws_broadcast host functions; the
// function caches its own client_id (passed in ws_open) for outbound writes.
package persistent
