package serverless

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/capability"
	"github.com/Orama-Network/orama/core/pkg/gateway/ctxkeys"
	"github.com/Orama-Network/orama/core/pkg/gateway/wssession"
	"github.com/Orama-Network/orama/core/pkg/serverless"
	"go.uber.org/zap"
)

// Opening a function's WebSocket on a capability (feat-264).
//
// `GET /v1/functions/{name}/ws?namespace=<ns>&cap=<token>` carries no
// credential of the caller's: the capability, minted by one of the
// namespace's devices through this function, is the whole authorization.
// Everything that decides whether to let it in happens here, before the
// upgrade and before a persistent instance is taken from the pool. The token
// is checked first, from the URL alone, so a stranger with a made-up token
// costs one HMAC — no registry read — and learns nothing about which
// functions exist.

const (
	// CapabilityQueryParam is the query parameter a capability arrives in. A
	// browser cannot set a header on an upgrade.
	CapabilityQueryParam = "cap"

	// jwtQueryParam is where a WebSocket client that cannot set a header puts
	// its token; the gateway's auth middleware reads it under the same name.
	jwtQueryParam = "jwt"

	// maxSocketsPerCapability bounds the sockets one capability holds open on
	// a gateway at once. A capability is handed to a correspondent, whose
	// devices each open one; without a bound, one leaked capability spread
	// over many addresses could take the whole persistent-socket pool.
	maxSocketsPerCapability = 16
)

// capabilityCtxKey carries a verified capability from the check to the socket.
type capabilityCtxKey struct{}

// CapabilityRevocations answers whether a capability, or the device that
// issued it, has been revoked. *auth.Service is the one the gateway uses.
type CapabilityRevocations interface {
	Revoked(claims *auth.JWTClaims) (bool, error)
}

// SetCapabilities lets these handlers accept capability-opened WebSockets.
// Called once at gateway start; without it every capability is refused, saying
// the gateway cannot check it.
func (h *ServerlessHandlers) SetCapabilities(authority *capability.Authority, revocations CapabilityRevocations) {
	h.capabilities = authority
	h.capabilityRevocations = revocations
	h.capabilitySockets = &socketCounter{open: map[string]int{}}
}

// capabilityToken is the capability a request presents, or "".
func capabilityToken(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get(CapabilityQueryParam))
}

// serveCapabilityWebSocket opens a function's socket on a capability, or
// refuses it before anything is upgraded or acquired.
func (h *ServerlessHandlers) serveCapabilityWebSocket(w http.ResponseWriter, r *http.Request, namespace, name string, version int, token string) {
	r, fn, ok := h.openWithCapability(w, r, namespace, name, version, token)
	if !ok {
		return
	}
	id := requestCapability(r).ID
	if !h.capabilitySockets.acquire(id) {
		http.Error(w, "too many sockets are open on this capability", http.StatusTooManyRequests)
		return
	}
	defer h.capabilitySockets.release(id)
	if fn.WSPersistent {
		h.handlePersistentWebSocket(w, r, fn, namespace)
		return
	}
	h.handleStatelessWebSocket(w, r, namespace, name, version)
}

// openWithCapability checks the capability, then the function it opens. It
// returns the request carrying the verified capability, or writes the refusal
// and returns false.
func (h *ServerlessHandlers) openWithCapability(w http.ResponseWriter, r *http.Request, namespace, name string, version int, token string) (*http.Request, *serverless.Function, bool) {
	claims, ok := h.checkCapability(w, r, namespace, name, version, token)
	if !ok {
		return nil, nil, false
	}
	fn, err := h.registry.Get(r.Context(), namespace, name, 0)
	if err != nil && !serverless.IsNotFound(err) {
		h.logger.Warn("capability WebSocket: function lookup failed",
			zap.String("namespace", namespace), zap.String("function", name), zap.Error(err))
		http.Error(w, "could not look the function up; retry", http.StatusServiceUnavailable)
		return nil, nil, false
	}
	// A function that is gone, disabled, internal, or no longer declares
	// ws_auth: capability is refused like a bad token.
	if fn == nil || fn.Status != serverless.FunctionStatusActive ||
		fn.WSAuth != serverless.WSAuthCapability || fn.IsInternal {
		http.Error(w, "forbidden: "+capability.ErrInvalid.Error(), http.StatusForbidden)
		return nil, nil, false
	}
	return r.WithContext(context.WithValue(r.Context(), capabilityCtxKey{}, claims)), fn, true
}

// checkCapability checks everything about a capability that the URL can say:
// how it was sent, the token, and whether it or its device was revoked.
func (h *ServerlessHandlers) checkCapability(w http.ResponseWriter, r *http.Request, namespace, name string, version int, token string) (*capability.Claims, bool) {
	if r.Method != http.MethodGet {
		http.Error(w, "a WebSocket is opened with GET", http.StatusMethodNotAllowed)
		return nil, false
	}
	// Presented alone or not at all: a socket opened on a capability is one
	// the gateway cannot tie to an account, and a credential beside it would
	// tie it to one.
	if presentsCredential(r) {
		http.Error(w, "a capability is presented alone: this request also carries a credential", http.StatusBadRequest)
		return nil, false
	}
	if h.capabilities == nil || h.capabilityRevocations == nil {
		http.Error(w, "this gateway cannot check capabilities: it has no cluster secret", http.StatusServiceUnavailable)
		return nil, false
	}
	// A capability names a function, not a version: it opens the live one,
	// so disabling or redeploying the function is what it answers to.
	if version != 0 {
		http.Error(w, "forbidden: "+capability.ErrInvalid.Error(), http.StatusForbidden)
		return nil, false
	}
	claims, err := h.capabilities.Verify(token, namespace, name, time.Now())
	if err != nil {
		if !errors.Is(err, capability.ErrInvalid) {
			h.logger.Error("capability WebSocket: the check failed",
				zap.String("namespace", namespace), zap.String("function", name), zap.Error(err))
			http.Error(w, "could not check the capability; retry", http.StatusServiceUnavailable)
			return nil, false
		}
		http.Error(w, "forbidden: "+capability.ErrInvalid.Error(), http.StatusForbidden)
		return nil, false
	}
	revoked, err := h.capabilityRevocations.Revoked(claims.RevocationClaims())
	if err != nil {
		h.logger.Error("capability WebSocket: the revocation list could not be checked",
			zap.String("namespace", namespace), zap.String("function", name), zap.Error(err))
		http.Error(w, "could not check the capability; retry", http.StatusServiceUnavailable)
		return nil, false
	}
	if revoked {
		http.Error(w, "forbidden: this capability, or the device that issued it, was revoked", http.StatusForbidden)
		return nil, false
	}
	return claims, true
}

// presentsCredential reports whether a request authenticated as anybody, or
// carries anything offered as a credential. One that does not verify is still
// refused: on a route that needs none, the middleware lets it through
// unverified, and the sender meant to be named by it.
func presentsCredential(r *http.Request) bool {
	ctx := r.Context()
	for _, key := range []ctxkeys.ContextKey{ctxkeys.JWT, ctxkeys.APIKey, ctxkeys.Scopes, ctxkeys.Grant, ctxkeys.Permissions} {
		if ctx.Value(key) != nil {
			return true
		}
	}
	return strings.TrimSpace(r.Header.Get("Authorization")) != "" ||
		strings.TrimSpace(r.URL.Query().Get(jwtQueryParam)) != "" ||
		auth.APIKeyFromRequest(r, true) != ""
}

// requestCapability is the capability a socket was opened with, or nil.
func requestCapability(r *http.Request) *capability.Claims {
	claims, _ := r.Context().Value(capabilityCtxKey{}).(*capability.Claims)
	return claims
}

// socketCapability is what the capability a socket was opened with grants,
// for the function to read, or nil.
func socketCapability(r *http.Request) *serverless.CapabilityGrant {
	if c := requestCapability(r); c != nil {
		return c.Grant()
	}
	return nil
}

// socketCaller is the caller a socket reports to the function: its wallet,
// and whether it is an admin or holds the invoke grant. A socket opened on a
// capability reports none of them, whatever the request's namespace routing
// put on it.
func (h *ServerlessHandlers) socketCaller(r *http.Request) (wallet string, isAdmin, hasInvoke bool) {
	if requestCapability(r) != nil {
		return "", false, false
	}
	return h.getWalletFromRequest(r), h.getCallerIsAdminFromRequest(r), h.getCallerHasInvokeFromRequest(r)
}

// socketCallerKey is the credential fingerprint the socket's invocations are
// rate-limited by: none for a capability, which names no account.
func (h *ServerlessHandlers) socketCallerKey(r *http.Request) string {
	if requestCapability(r) != nil {
		return ""
	}
	return h.getCallerKeyFromRequest(r)
}

// socketClaims is what a socket is held to: the verified token it was opened
// with, or its capability, which the sweeper checks the same way.
func (h *ServerlessHandlers) socketClaims(r *http.Request) *auth.JWTClaims {
	if c := requestCapability(r); c != nil {
		return c.RevocationClaims()
	}
	return h.getJWTClaimsFromRequest(r)
}

// registerSocket holds a socket to the credential that opened it: its token
// or capability (socketClaims), or the bare API key, by the key's registry id,
// so revoking the key closes it too (bug-702). A socket whose upgrade the
// attestation middleware judged is held to its namespace's policy as well.
func (h *ServerlessHandlers) registerSocket(r *http.Request, closeFn wssession.Closer) *wssession.Socket {
	var sock *wssession.Socket
	switch claims := h.socketClaims(r); {
	case claims != nil && requestCapability(r) != nil:
		sock = h.sessions.RegisterCapability(claims, closeFn)
	case claims != nil:
		sock = h.sessions.Register(claims, closeFn)
	default:
		keyID, _ := r.Context().Value(ctxkeys.KeyID).(int64)
		sock = h.sessions.RegisterKey(keyID, closeFn)
	}
	ns, _ := r.Context().Value(ctxkeys.AttestationNamespace).(string)
	sock.HoldToAttestation(ns)
	return sock
}

// socketCounter counts the sockets each capability holds open.
type socketCounter struct {
	mu   sync.Mutex
	open map[string]int
}

func (c *socketCounter) acquire(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.open[id] >= maxSocketsPerCapability {
		return false
	}
	c.open[id]++
	return true
}

func (c *socketCounter) release(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.open[id] <= 1 {
		delete(c.open, id)
		return
	}
	c.open[id]--
}
