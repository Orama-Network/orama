package serverless

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/wssession"
	"github.com/Orama-Network/orama/core/pkg/serverless"
	"github.com/Orama-Network/orama/core/pkg/serverless/persistent"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// oramaControlFramePrefix is a cheap byte-level sniff for the WS
// control-frame envelope shape `{"__orama":"..."}`. We peek for this
// before JSON-decoding to keep the per-frame fast path free of
// json.Unmarshal cost — the vast majority of inbound frames are
// application traffic that goes straight to WASM. Bugboard #321.
var oramaControlFramePrefix = []byte(`"__orama"`)

// oramaControlFrame is the wire shape for gateway-handled control
// frames on a persistent WS. The single Type field discriminates;
// payload fields specific to each Type ride alongside.
//
// Today supports:
//
//	{"__orama":"auth.refresh","jwt":"<new-token>"}
//
// Future types (e.g. "ping.app", "subscribe.status") follow the same
// shape. Reserve "__orama" as the namespace so application frames
// never collide.
type oramaControlFrame struct {
	Type string `json:"__orama"`
	JWT  string `json:"jwt,omitempty"`
}

// authRefreshUnavailableMessage is the ack error for a token whose revocation
// state could not be read. Constant, and worded as retryable, so a client can
// tell it from a refused token ("invalid or expired jwt").
const authRefreshUnavailableMessage = "cannot check whether this token was revoked; retry shortly"

// oramaControlAck is the response shape sent back on the WS after a
// control frame is handled. Clients SHOULD await this before assuming
// the gateway has applied the change.
type oramaControlAck struct {
	Type    string `json:"__orama_ack"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Subject string `json:"subject,omitempty"` // populated on successful auth.refresh
	// Code is DEVICE_REVOKED on an auth.refresh refused because the token's
	// device was revoked, with Revocation its revocation (feat-976), the
	// record a 403 DEVICE_REVOKED carries. Empty on every other ack.
	Code       string                       `json:"code,omitempty"`
	Revocation *auth.DeviceRevocationRecord `json:"revocation,omitempty"`
}

// ackCodeDeviceRevoked is the ack Code of an auth.refresh refused because the
// token's device was revoked: the code a request answers in that case.
const ackCodeDeviceRevoked = "DEVICE_REVOKED"

// handlePersistentWebSocket runs the per-connection persistent function model.
// One WASM instance is bound to this WS for its entire lifetime. Frames are
// processed serially via the instance's inbound channel.
//
// See plan: core/plans/platform/06_PERSISTENT_WS_FUNCTIONS.md
func (h *ServerlessHandlers) handlePersistentWebSocket(
	w http.ResponseWriter, r *http.Request, fn *serverless.Function, namespace string,
) {
	// Hard prerequisites — without engine + manager, persistent WS can't run.
	if h.engine == nil || h.persistentMgr == nil {
		http.Error(w, "persistent WebSocket support not configured", http.StatusServiceUnavailable)
		return
	}

	// Capacity check BEFORE upgrade so we don't leak a half-open WS.
	if !h.persistentMgr.Acquire() {
		http.Error(w, "gateway at persistent-ws capacity", http.StatusServiceUnavailable)
		return
	}
	releaseSlot := true
	defer func() {
		if releaseSlot {
			h.persistentMgr.Release()
		}
	}()

	upgrader := websocket.Upgrader{CheckOrigin: checkWSOrigin}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("persistent WS upgrade failed", zap.Error(err))
		return
	}

	clientID := uuid.New().String()
	wsConn := &serverless.GorillaWSConn{Conn: conn}
	h.wsManager.Register(clientID, wsConn)
	defer h.wsManager.Unregister(clientID)

	// Bridge bookkeeping (mirrors stateless path): the persistent WASM
	// instance can call ws_pubsub_bridge from ws_open or any frame handler;
	// the bridge needs to know which namespace owns this client.
	if h.wsBridge != nil {
		h.registerBridgeClient(r, clientID, namespace)
		defer h.wsBridge.RemoveClient(context.Background(), clientID)
	}

	invCtx := h.buildPersistentInvocationContext(r, fn, clientID)
	callerWallet := invCtx.CallerWallet

	// Hold the socket to the token that opened it (bugboard #868): the
	// gateway's sweeper closes it once the token expires or is revoked, and
	// the read loop below refuses frames past expiry in between sweeps. A
	// successful auth.refresh control frame moves it to the new token.
	sock := h.registerSocket(r, h.closeClient(clientID))
	defer sock.Unregister()

	// Instantiate the persistent module. This compiles once (cached) and
	// creates one wazero instance bound to this connection.
	module, err := h.engine.InstantiatePersistent(r.Context(), fn, invCtx)
	if err != nil {
		h.logger.Warn("persistent WS instantiate failed",
			zap.String("function", fn.Name),
			zap.String("namespace", fn.Namespace),
			zap.Error(err))
		_ = conn.Close()
		return
	}

	inst, err := persistent.NewInstance(module, persistent.Config{
		ClientID:          clientID,
		FunctionName:      fn.Name,
		Namespace:         fn.Namespace,
		FrameTimeoutSec:   fn.TimeoutSeconds,
		MaxInflightFrames: fn.WSMaxInflightPerConn,
		// Per-instance identity binding. The persistent.Instance attaches
		// this to the ctx of every WASM-host call (ws_open / ws_frame /
		// ws_close + nested function_invoke), so caller identity is
		// race-free across concurrent persistent WS connections — fixes
		// the cross-tenant identity-leak on the shared HostFunctions
		// singleton (security audit follow-up to Layer 7 of Feature #73).
		InvocationContext: invCtx,
	}, h.logger)
	if err != nil {
		h.logger.Warn("persistent WS NewInstance failed",
			zap.String("function", fn.Name),
			zap.Error(err))
		_ = module.Close(context.Background())
		_ = conn.Close()
		return
	}

	h.persistentMgr.Register(inst)
	// Hand the slot off to instance lifecycle. Released when we Close below.
	releaseSlot = false
	defer h.persistentMgr.Release()
	defer h.persistentMgr.Unregister(clientID)

	// ws_open — invoked synchronously. A non-zero return rejects the upgrade.
	openInput := persistent.WSOpenInput{
		ClientID:  clientID,
		Wallet:    callerWallet,
		Namespace: namespace,
	}
	if err := inst.Open(r.Context(), openInput); err != nil {
		h.logger.Info("persistent WS rejected by ws_open",
			zap.String("function", fn.Name),
			zap.String("client_id", clientID),
			zap.Error(err))
		inst.Close(context.Background(), persistent.CloseReasonRejected)
		_ = conn.Close()
		return
	}

	// Spawn the per-instance frame processor.
	runCtx, runCancel := context.WithCancel(context.Background())
	go inst.Run(runCtx)

	// Server-side keepalive (matches stateless WS handler's behavior).
	const (
		pingInterval = 30 * time.Second
		pongWait     = 60 * time.Second
	)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	pingDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-ticker.C:
				_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			}
		}
	}()

	// Read loop — enqueue frames into the instance. Bugboard #321:
	// gateway-handled control frames (e.g. {"__orama":"auth.refresh"})
	// are intercepted here BEFORE submission so they don't reach WASM.
	for {
		_, frame, readErr := conn.ReadMessage()
		if readErr != nil {
			break
		}
		h.wsManager.RecordInbound(clientID, len(frame))

		// Cheap byte-level prefix sniff so the per-frame fast path
		// avoids json.Unmarshal for every application frame. Only
		// frames carrying the `"__orama"` key get parsed.
		if bytes.Contains(frame, oramaControlFramePrefix) {
			handled, ackErr := h.handleOramaControlFrame(r.Context(), frame, fn, inst, sock, namespace, clientID, conn)
			if ackErr != nil {
				h.logger.Warn("persistent WS: control-frame ack write failed",
					zap.String("client_id", clientID),
					zap.Error(ackErr))
				// Don't kill the WS for an ack write failure — the
				// client will time-out the ack and retry. Continue.
			}
			if handled {
				continue // Don't forward control frames to WASM.
			}
			// Not actually a control frame (false-positive prefix
			// match — e.g. a JSON string literal containing
			// `"__orama"`); fall through and submit as a normal
			// application frame.
		}

		// Bugboard #868: a persistent WS authenticates ONCE at upgrade.
		// Before handing an application frame to WASM, reject it once the
		// authorizing JWT is past exp+grace, rather than serving it until
		// the next sweep. The client keeps the socket alive by sending an
		// {"__orama":"auth.refresh"} control frame (handled above, which
		// bypasses this check) before the token expires.
		if sock.Expired(time.Now()) {
			h.logger.Info("persistent WS: closing — JWT expired without refresh",
				zap.String("client_id", clientID),
				zap.String("namespace", namespace),
				zap.Int64("jwt_exp", sock.Claims().Exp))
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(wssession.CloseExpired, wssession.ReasonExpired),
				time.Now().Add(wssession.CloseFrameTimeout))
			break
		}

		if err := inst.Submit(frame); err != nil {
			h.logger.Warn("persistent WS submit failed (queue full?)",
				zap.String("client_id", clientID),
				zap.Error(err))
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(1009, "queue full"),
				time.Now().Add(time.Second))
			break
		}
	}

	// Tear down: stop ping, stop instance Run, invoke ws_close, close WS.
	close(pingDone)
	runCancel()
	inst.Close(context.Background(), persistent.CloseReasonClientDisconnect)
	_ = conn.Close()
}

// buildPersistentInvocationContext constructs the per-connection InvocationContext
// for a persistent WS instance. Extracted from handlePersistentWebSocket so the
// auth-field plumbing can be unit-tested without doing a real WS upgrade.
//
// IMPORTANT: this context is sticky for the lifetime of the connection — it is
// bound once at instantiation (pkg/serverless/engine.go InstantiatePersistent)
// and reused for every ws_open / ws_frame / ws_close call, as well as for any
// nested function_invoke call originating inside the WASM instance. Missing a
// field here (notably CallerJWTSubject) means every sub-function invoked via
// `oh.FunctionInvoke` sees an empty value for the missing field — Layer 7 of
// the WS bug chain (Feature #73 on bugboard; AnChat sync-deltas was returning
// AUTH_REQUIRED because oh.JwtSubjectUserID() was "" inside the sub-function).
//
// Keep this in sync with the stateless WS handler's InvokeRequest construction
// in ws_handler.go — they must populate the same auth-identity fields.
func (h *ServerlessHandlers) buildPersistentInvocationContext(
	r *http.Request, fn *serverless.Function, clientID string,
) *serverless.InvocationContext {
	// CallerHasInvoke has to travel with the socket. The upgrade checks it,
	// then every nested function_invoke reads it back from this context.
	// Dropping it made a private handler unauthorized for the same wallet
	// that had just been allowed to open the socket: the async dispatch
	// returned, the child never ran, and the client got no reply.
	callerWallet, callerIsAdmin, callerHasInvoke := h.socketCaller(r)
	return &serverless.InvocationContext{
		FunctionID:        fn.ID,
		FunctionName:      fn.Name,
		Namespace:         fn.Namespace,
		CallerWallet:      callerWallet,
		CallerIsAdmin:     callerIsAdmin,
		CallerHasInvoke:   callerHasInvoke,
		CallerIP:          extractRemoteIP(r),
		CallerKey:         h.socketCallerKey(r),
		CallerClaims:      h.getCallerClaimsFromRequest(r),
		CallerJWTSubject:  h.getJWTSubjectFromRequest(r),
		CallerDeviceID:    h.getDeviceIDFromRequest(r),
		CallerDeviceState: h.getDeviceStateFromRequest(r),
		CallerAttestation: h.getAttestationFromRequest(r),
		CallerCapability:  socketCapability(r),
		WSClientID:        clientID,
		TriggerType:       serverless.TriggerTypeWebSocket,
	}
}

// handleOramaControlFrame parses a frame as the orama control envelope
// and dispatches by type. Returns (handled=true, _) if the frame was a
// well-formed control frame (regardless of whether it succeeded);
// (false, nil) for false-positives where the byte sniff matched but
// the JSON shape isn't ours. The returned error reflects only the ack
// write — not the underlying control action (which surfaces via the
// ack body's ok/error fields).
//
// Bugboard #321: introduced for the auth.refresh path so persistent
// WS connections survive JWT rotation without a close+reconnect.
func (h *ServerlessHandlers) handleOramaControlFrame(
	ctx context.Context,
	frame []byte,
	fn *serverless.Function,
	inst *persistent.Instance,
	sock *wssession.Socket,
	namespace, clientID string,
	conn *websocket.Conn,
) (handled bool, ackErr error) {
	var ctrl oramaControlFrame
	if err := json.Unmarshal(frame, &ctrl); err != nil {
		// Not JSON, or doesn't match our shape. Treat as application
		// frame (false-positive on the prefix sniff).
		return false, nil
	}
	if ctrl.Type == "" {
		return false, nil
	}

	switch ctrl.Type {
	case "auth.refresh":
		return true, h.handleAuthRefresh(ctx, ctrl, fn, inst, sock, namespace, clientID, conn)
	default:
		// Unknown control type — ack with an error so the client knows
		// the frame was seen but ignored. Treat as handled (don't
		// forward to WASM), since the `__orama` namespace is reserved.
		return true, h.writeControlAck(conn, oramaControlAck{
			Type:  ctrl.Type,
			OK:    false,
			Error: "unknown __orama control type",
		})
	}
}

// refusedRefreshAck is the ack of an auth.refresh whose token
// ParseAndVerifyJWT refused with err. A token refused for its device is
// answered DEVICE_REVOKED with the device's revocation when the registry,
// read only now that the token is refused, confirms it; as a refused token
// when the registry holds no such device; and as retryable when the registry
// disagrees or cannot be read, as a revocation list that cannot be read is.
// The ack never carries a read's error, which can name why it failed.
func (h *ServerlessHandlers) refusedRefreshAck(ctx context.Context, err error, clientID string) oramaControlAck {
	var revokedDevice *auth.DeviceTokenRevokedError
	switch {
	case errors.As(err, &revokedDevice):
		rec, rerr := h.jwtVerifier.DeviceRevocationRecord(ctx, revokedDevice.Claims.Namespace, revokedDevice.Claims.Did)
		if errors.Is(rerr, auth.ErrDeviceNotRevoked) {
			return oramaControlAck{Type: "auth.refresh", Error: "invalid or expired jwt: " + auth.ErrTokenRevoked.Error()}
		}
		if rerr != nil {
			h.logger.Warn("persistent WS: could not read a revoked device's revocation for auth.refresh",
				zap.String("client_id", clientID),
				zap.Error(rerr))
			return oramaControlAck{Type: "auth.refresh", Error: authRefreshUnavailableMessage}
		}
		return oramaControlAck{
			Type:       "auth.refresh",
			Error:      "invalid or expired jwt: " + err.Error(),
			Code:       ackCodeDeviceRevoked,
			Revocation: rec,
		}
	case errors.Is(err, auth.ErrRevocationsUnavailable):
		// Not a verdict on the token: the gateway could not tell whether it
		// was revoked. The client keeps its socket and retries.
		h.logger.Warn("persistent WS: auth.refresh could not be checked against the revocation list",
			zap.String("client_id", clientID),
			zap.Error(err))
		return oramaControlAck{Type: "auth.refresh", Error: authRefreshUnavailableMessage}
	}
	h.logger.Info("persistent WS: auth.refresh rejected (invalid jwt)",
		zap.String("client_id", clientID),
		zap.Error(err))
	return oramaControlAck{Type: "auth.refresh", Error: "invalid or expired jwt: " + err.Error()}
}

// handleAuthRefresh validates the new JWT, swaps the persistent
// instance's invocation context atomically, and acks the client.
// On invalid JWT: ack with ok=false and a reason. Does NOT close the
// WS — the client can retry with a fresh token. Bugboard #321.
func (h *ServerlessHandlers) handleAuthRefresh(
	ctx context.Context,
	ctrl oramaControlFrame,
	fn *serverless.Function,
	inst *persistent.Instance,
	sock *wssession.Socket,
	namespace, clientID string,
	conn *websocket.Conn,
) error {
	if h.jwtVerifier == nil {
		return h.writeControlAck(conn, oramaControlAck{
			Type:  "auth.refresh",
			OK:    false,
			Error: "mid-session auth refresh not supported on this gateway",
		})
	}
	// A socket held to no account — an API key's, or one opened with a
	// capability — cannot take one on. It is refused before the token is
	// even read, so an anonymous socket is never logged beside an account.
	if !sock.HoldsAccount() {
		return h.writeControlAck(conn, oramaControlAck{
			Type:  "auth.refresh",
			OK:    false,
			Error: wssession.ErrNotRefreshable.Error(),
		})
	}
	if ctrl.JWT == "" {
		return h.writeControlAck(conn, oramaControlAck{
			Type:  "auth.refresh",
			OK:    false,
			Error: "jwt field required",
		})
	}
	claims, err := h.jwtVerifier.ParseAndVerifyJWT(ctrl.JWT)
	if err != nil {
		return h.writeControlAck(conn, h.refusedRefreshAck(ctx, err, clientID))
	}

	if reason := validateRefreshClaims(claims, fn.Namespace); reason != "" {
		h.logger.Warn("persistent WS: auth.refresh rejected",
			zap.String("client_id", clientID),
			zap.String("reason", reason),
			zap.String("ws_namespace", fn.Namespace),
			zap.String("jwt_namespace", claims.Namespace),
			zap.String("jwt_subject", auth.RedactSubject(claims.Sub)),
		)
		return h.writeControlAck(conn, oramaControlAck{
			Type:  "auth.refresh",
			OK:    false,
			Error: reason,
		})
	}

	// A pending device's restricted session is installed only on a socket to
	// a function open to pending devices, as it could only have opened one.
	if err := serverless.PendingDeviceMayInvoke(fn, claims.DeviceState()); err != nil {
		h.logger.Warn("persistent WS: auth.refresh rejected",
			zap.String("client_id", clientID),
			zap.Error(err))
		return h.writeControlAck(conn, oramaControlAck{
			Type:  "auth.refresh",
			OK:    false,
			Error: err.Error(),
		})
	}

	// A token for another subject is refused before anything changes — a
	// refresh keeps this socket open, it does not hand it to somebody else,
	// and ws_open ran for the original identity.
	if err := sock.CheckRefresh(claims); err != nil {
		h.logger.Warn("persistent WS: auth.refresh rejected",
			zap.String("client_id", clientID),
			zap.String("jwt_subject", auth.RedactSubject(claims.Sub)),
			zap.Error(err))
		return h.writeControlAck(conn, oramaControlAck{
			Type:  "auth.refresh",
			OK:    false,
			Error: err.Error(),
		})
	}

	if err := inst.UpdateInvocationContext(refreshedInvocationContext(inst, fn, claims, clientID)); err != nil {
		h.logger.Error("persistent WS: UpdateInvocationContext failed",
			zap.String("client_id", clientID),
			zap.Error(err))
		return h.writeControlAck(conn, oramaControlAck{
			Type:  "auth.refresh",
			OK:    false,
			Error: "internal: failed to apply refresh",
		})
	}

	// Only once the instance runs as the new token does the socket answer to
	// it: its expiry and its jti are what the sweeper checks from here on.
	if err := sock.Refresh(claims); err != nil {
		h.logger.Error("persistent WS: auth.refresh applied to the instance but not the socket",
			zap.String("client_id", clientID),
			zap.Error(err))
		return h.writeControlAck(conn, oramaControlAck{
			Type:  "auth.refresh",
			OK:    false,
			Error: "internal: failed to apply refresh",
		})
	}

	h.logger.Info("persistent WS: auth.refresh applied",
		zap.String("client_id", clientID),
		zap.String("namespace", namespace),
		zap.String("subject", auth.RedactSubject(claims.Sub)))

	return h.writeControlAck(conn, oramaControlAck{
		Type:    "auth.refresh",
		OK:      true,
		Subject: claims.Sub,
	})
}

// refreshedInvocationContext is the persistent instance's identity after a
// token refresh. The connection-scoped fields (function, namespace, client id,
// trigger type) do not change, and neither do the authorization and client IP
// established at the upgrade — a refresh keeps the socket alive, it does not
// re-open it. Dropping CallerIsAdmin here would silently strip admin from an
// admin's internal→internal child invokes after a refresh (bugboard #152).
// Dropping CallerHasInvoke would refuse every private handler the socket was
// allowed to dispatch. The subject is the one the socket was opened with; the
// custom claims may have moved on.
func refreshedInvocationContext(
	inst *persistent.Instance, fn *serverless.Function, claims *auth.JWTClaims, clientID string,
) *serverless.InvocationContext {
	prevIsAdmin, prevHasInvoke, prevIP := false, false, ""
	if cur := inst.CurrentInvocationContext(); cur != nil {
		prevIsAdmin, prevHasInvoke, prevIP = cur.CallerIsAdmin, cur.CallerHasInvoke, cur.CallerIP
	}
	customClaims := make(map[string]string, len(claims.Custom))
	for k, v := range claims.Custom {
		customClaims[k] = v
	}
	return &serverless.InvocationContext{
		FunctionID:        fn.ID,
		FunctionName:      fn.Name,
		Namespace:         fn.Namespace,
		CallerWallet:      claims.Sub,
		CallerClaims:      customClaims,
		CallerJWTSubject:  claims.Sub,
		CallerDeviceID:    claims.Did,
		CallerDeviceState: claims.DeviceState(),
		CallerAttestation: callerAttestation(claims.Att),
		CallerIP:          prevIP,
		CallerIsAdmin:     prevIsAdmin,
		CallerHasInvoke:   prevHasInvoke,
		WSClientID:        clientID,
		TriggerType:       serverless.TriggerTypeWebSocket,
	}
}

// validateRefreshClaims is the policy decision for whether a
// post-validation JWT may be installed on a persistent WS via the
// auth.refresh control frame. Returns "" if allowed, or a
// human-readable reason string suitable for the ack body.
//
// SECURITY (bug #321 audit HIGH #9): reject JWTs minted for a
// DIFFERENT namespace. Without this check, an attacker who
// legitimately owns an account in namespace B could rotate their
// already-established namespace-A WS to run as their B-subject
// against A's WASM/secrets/data. The upgrade-time auth middleware
// already enforces namespace match; this preserves the invariant
// across mid-session rotations.
//
// Empty claims.Namespace is treated as a hard reject — JWTs minted
// by this gateway always populate it; an empty value either means
// a foreign issuer slipped through or a malformed token. Either
// way, refuse rather than silently default to the WS's namespace.
//
// Extracted as a pure function so the policy decision can be
// regression-tested without a live WS connection.
func validateRefreshClaims(claims *auth.JWTClaims, wsNamespace string) string {
	if claims == nil {
		return "internal: nil claims after verification"
	}
	if claims.Namespace == "" {
		return "jwt missing namespace claim"
	}
	if claims.Namespace != wsNamespace {
		return "jwt namespace does not match websocket namespace"
	}
	if claims.Sub == "" {
		// Subject-less JWTs would swap the WS into an anonymous
		// identity, breaking every downstream auth check. Reject.
		return "jwt missing subject claim"
	}
	return ""
}

// writeControlAck JSON-encodes the ack and writes it as a single text
// message back to the client. Bounded write deadline so a slow client
// doesn't block the read loop.
func (h *ServerlessHandlers) writeControlAck(conn *websocket.Conn, ack oramaControlAck) error {
	payload, err := json.Marshal(ack)
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetWriteDeadline(time.Time{})
	return conn.WriteMessage(websocket.TextMessage, payload)
}
