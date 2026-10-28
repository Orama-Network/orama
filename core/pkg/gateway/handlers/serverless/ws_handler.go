package serverless

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/wssession"
	"github.com/Orama-Network/orama/core/pkg/httputil"
	"github.com/Orama-Network/orama/core/pkg/serverless"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// checkWSOrigin validates WebSocket origins against the request's Host header.
// Non-browser clients (no Origin) are allowed. Browser clients must match the host.
//
// Bug #240/#249 root cause: when this handler runs on a NAMESPACE gateway,
// the request has been proxied through `handleNamespaceGatewayRequest`
// which REWRITES `r.Host` to the backend target's IP:port (e.g.
// "10.0.0.6:10004") before forwarding. The original public host (e.g.
// "ns-anchat-test.devnet.orama.network") is preserved in the
// `X-Forwarded-Host` header. If we only compare the Origin against
// `r.Host`, browser/RN-iOS clients (which always send Origin) are
// rejected with 403 because their Origin's `ns-anchat-test.devnet.orama.network`
// will never match the proxied `10.0.0.6` target. Curl tests that don't
// send Origin slip through, masking the bug.
//
// Prefer X-Forwarded-Host (the original public host) when present,
// falling back to r.Host for direct (non-proxied) connections.
func checkWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if host == "" {
		return false
	}
	// Strip port from host if present
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	originHost := parsed.Hostname()
	return originHost == host || strings.HasSuffix(originHost, "."+host)
}

// closeClient is how the session sweeper ends one function WebSocket. The
// handler's read loop sees the connection go and tears the rest down.
func (h *ServerlessHandlers) closeClient(clientID string) wssession.Closer {
	return func(code int, reason string) {
		err := h.wsManager.CloseClient(clientID, code, reason)
		if err != nil && !errors.Is(err, serverless.ErrWSClientNotFound) {
			h.logger.Warn("could not close a WebSocket whose token no longer authorizes it",
				zap.String("client_id", clientID), zap.Int("code", code), zap.Error(err))
		}
	}
}

// HandleWebSocket handles WebSocket connections for function streaming.
// It upgrades HTTP connections to WebSocket and manages bi-directional communication
// for real-time function invocation and streaming responses.
//
// Routes to one of two execution models based on function metadata:
//   - WSPersistent=true: persistent per-connection WASM instance (plan 06)
//   - WSPersistent=false (default): per-frame stateless invocation
func (h *ServerlessHandlers) HandleWebSocket(w http.ResponseWriter, r *http.Request, name string, version int) {
	// A capability is the whole authorization of its socket, checked here —
	// before the upgrade and before a persistent instance is taken. It carries
	// no credential, so there is no credential namespace to manage with: the
	// namespace is the one the URL names (?namespace=), which the capability's
	// key is derived from.
	if token := capabilityToken(r); token != "" {
		h.serveCapabilityWebSocket(w, r, h.invokeNamespace(r), name, version, token)
		return
	}

	// Any other WebSocket needs a credential, and it runs functions of that
	// credential's namespace (bugboard #423).
	namespace, ok := managedNamespace(w, r)
	if !ok {
		return
	}
	// A grant narrowed to `fn:name=` limits which functions a socket may run,
	// as it limits HTTP invocation (invoke_handler.go); checked before the
	// upgrade and before a persistent instance is taken.
	if err := auth.AuthorizeResource(r.Context(), auth.Resource{Domain: auth.SelectorFn, Name: name}); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	// Look up the function once to decide which execution model to use.
	fn, lookupErr := h.registry.Get(r.Context(), namespace, name, version)
	// A function that declares invocable_by is refused to a client on both
	// models, before the upgrade (feat-971): every frame of a stateless socket
	// would be refused by the invoker, and a persistent one never reaches it.
	if lookupErr == nil && fn != nil {
		if err := serverless.RefuseClientInvocation(fn, h.getCallerIsAdminFromRequest(r)); err != nil {
			writeRPCError(w, http.StatusForbidden, httputil.ErrCodeFunctionNotDirectlyInvocable, err.Error())
			return
		}
	}
	if lookupErr == nil && fn != nil && fn.WSPersistent {
		// The persistent path builds its InvocationContext directly and never
		// reaches Invoke, so the authorization that the stateless path gets
		// per-frame has to happen here, before the upgrade.
		//
		// It used to check only that an internal function had an admin caller
		// (bugboard #152). That left the rest of the same decision out: a
		// function that is merely private — not public, not internal — was
		// reachable over a persistent WebSocket by a caller with no wallet and
		// no invoke grant, while the identical function over the stateless
		// path or over HTTP refused them. Same decision, one place.
		if !serverless.CanInvokeFunction(
			fn,
			h.getWalletFromRequest(r),
			h.getCallerIsAdminFromRequest(r),
			h.getCallerHasInvokeFromRequest(r),
		) {
			http.Error(w, "forbidden: not authorized to invoke this function", http.StatusForbidden)
			return
		}
		h.handlePersistentWebSocket(w, r, fn, namespace)
		return
	}
	// (lookup error not fatal — fall through; per-frame path's invoker will
	// re-resolve and surface a proper error.)
	h.handleStatelessWebSocket(w, r, namespace, name, version)
}

// handleStatelessWebSocket runs the per-frame model: every frame is one
// invocation, authorized by the invoker as the caller the socket was opened
// for.
func (h *ServerlessHandlers) handleStatelessWebSocket(w http.ResponseWriter, r *http.Request, namespace, name string, version int) {
	// Upgrade to WebSocket
	upgrader := websocket.Upgrader{
		CheckOrigin: checkWSOrigin,
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("WebSocket upgrade failed", zap.Error(err))
		return
	}

	clientID := uuid.New().String()
	wsConn := &serverless.GorillaWSConn{Conn: conn}

	// Register connection
	h.wsManager.Register(clientID, wsConn)
	defer h.wsManager.Unregister(clientID)

	// Every frame is invoked with the identity captured below, so the socket
	// is only as good as the token — or the capability — that opened it: the
	// gateway's sweeper closes it once that expires or is revoked.
	sock := h.registerSocket(r, h.closeClient(clientID))
	defer sock.Unregister()

	// Track client → namespace for ws_pubsub_bridge auth checks, and
	// auto-clean any bridged topics when the connection ends.
	if h.wsBridge != nil {
		h.registerBridgeClient(r, clientID, namespace)
		defer h.wsBridge.RemoveClient(context.Background(), clientID)
	}

	// Server-side keepalive: ping every 30s, expect pong within 60s.
	// Without this, a half-open TCP can hang for 2h before the OS notices.
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
	defer close(pingDone)

	h.logger.Info("WebSocket connected",
		zap.String("client_id", clientID),
		zap.String("function", name),
	)

	callerWallet, callerIsAdmin, callerHasInvoke := h.socketCaller(r)
	callerIP := extractRemoteIP(r)
	callerKey := h.socketCallerKey(r)
	// Capture custom claims at upgrade time and reuse for every frame —
	// the JWT context is request-scoped and won't survive past upgrade.
	callerClaims := h.getCallerClaimsFromRequest(r)
	callerJWTSubject := h.getJWTSubjectFromRequest(r)
	callerDeviceID := h.getDeviceIDFromRequest(r)
	callerAttestation := h.getAttestationFromRequest(r)
	callerCapability := socketCapability(r)

	// Message loop
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				h.logger.Warn("WebSocket error", zap.Error(err))
			}
			break
		}
		h.wsManager.RecordInbound(clientID, len(message))

		// Invoke function with WebSocket context
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

		req := &serverless.InvokeRequest{
			Namespace:         namespace,
			FunctionName:      name,
			Version:           version,
			Input:             message,
			TriggerType:       serverless.TriggerTypeWebSocket,
			CallerWallet:      callerWallet,
			CallerIsAdmin:     callerIsAdmin,
			CallerHasInvoke:   callerHasInvoke,
			CallerIP:          callerIP,
			CallerKey:         callerKey,
			CallerClaims:      callerClaims,
			CallerJWTSubject:  callerJWTSubject,
			CallerDeviceID:    callerDeviceID,
			CallerAttestation: callerAttestation,
			CallerCapability:  callerCapability,
			WSClientID:        clientID,
		}

		resp, err := h.invoker.Invoke(ctx, req)
		// Bugboard #24 diagnostic — when the 30s WS-handler timeout
		// actually fires, log a structured warning so AnChat's next
		// "signaling.relay timed out" report includes request_id +
		// function + namespace + duration. Pre-fix this surfaced as
		// opaque "RPC timeout after 30s" with no way to correlate to a
		// specific invocation in engine logs.
		if err != nil && ctx.Err() == context.DeadlineExceeded {
			fields := []zap.Field{
				zap.String("namespace", namespace),
				zap.String("function", name),
				zap.String("ws_client_id", clientID),
				zap.Int64("duration_ms", resp.DurationMS),
				zap.Int("timeout_ms", 30000),
				zap.String("caller_wallet", callerWallet),
			}
			if resp.RequestID != "" {
				fields = append(fields, zap.String("request_id", resp.RequestID))
			}
			h.logger.Warn("WS function-invoke hit 30s ceiling (bug-24)", fields...)
		}
		cancel()

		// Send response back
		response := map[string]interface{}{
			"request_id":  resp.RequestID,
			"status":      resp.Status,
			"duration_ms": resp.DurationMS,
		}

		if err != nil {
			// Carry the same machine-readable code + retryable bit as the HTTP
			// path so the rpc-router can distinguish a transient cold-WASM
			// FUNCTION_UNAVAILABLE (retry the exact request) from a real
			// FUNCTION_EXECUTION_FAILED (do not retry). Previously the WS frame
			// gave clients no retry signal at all (bugboard #137).
			_, code, retryable := classifyInvokeError(err)
			msg := resp.Error
			if msg == "" {
				msg = err.Error()
			}
			response["error"] = msg
			response["code"] = string(code)
			response["retryable"] = retryable
		} else if len(resp.Output) > 0 {
			// Try to parse output as JSON
			var output interface{}
			if json.Unmarshal(resp.Output, &output) == nil {
				response["output"] = output
			} else {
				response["output"] = string(resp.Output)
			}
		}

		respBytes, _ := json.Marshal(response)
		if err := conn.WriteMessage(websocket.TextMessage, respBytes); err != nil {
			break
		}
	}
}
