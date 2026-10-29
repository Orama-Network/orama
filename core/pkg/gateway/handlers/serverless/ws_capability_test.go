package serverless

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/capability"
	"github.com/Orama-Network/orama/core/pkg/gateway/ctxkeys"
	"github.com/Orama-Network/orama/core/pkg/gateway/wssession"
	"github.com/Orama-Network/orama/core/pkg/serverless"
)

// Every capability refusal is answered before the upgrade — a 403 on a plain
// response, never a WebSocket — and before a persistent instance is taken. The
// handlers here have no engine, so a request that got as far as the persistent
// path would answer 503; a refusal answers before it.

type revokedSet map[string]bool

func (r revokedSet) Revoked(c *auth.JWTClaims) (bool, error) {
	return r[c.Jti] || r["device:"+c.Did], nil
}

func capabilityFn(name string) *serverless.Function {
	fn := persistentFn(name, false, false)
	fn.WSAuth = serverless.WSAuthCapability
	fn.Status = serverless.FunctionStatusActive
	return fn
}

func capabilityHandlers(t *testing.T, fn *serverless.Function, revoked revokedSet) (*ServerlessHandlers, *capability.Authority) {
	t.Helper()
	authority, err := capability.NewAuthority("cluster-secret-for-tests")
	if err != nil {
		t.Fatalf("authority: %v", err)
	}
	h := handlersWith(fn)
	h.SetCapabilities(authority, revoked)
	return h, authority
}

func mintFor(t *testing.T, a *capability.Authority, namespace, function string, at time.Time) (string, *capability.Claims) {
	t.Helper()
	token, claims, err := a.Mint(namespace, function, "mailbox-7", "device-1", time.Hour, at)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return token, claims
}

// capabilityRequest is a socket opened on a capability alone, the way a client
// opens one: the namespace in the URL and no credential, so the request
// carries no credential namespace. upgradeRequest would put one on it, which
// hid a handler that demanded one (the e2e suite saw 403 "the namespace this
// credential belongs to could not be resolved").
func capabilityRequest(token string, ctxValues map[any]any) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/?namespace=anchat-test", nil)
	ctx := r.Context()
	for k, v := range ctxValues {
		ctx = context.WithValue(ctx, k, v)
	}
	r = r.WithContext(ctx)
	q := r.URL.Query()
	q.Set(CapabilityQueryParam, token)
	r.URL.RawQuery = q.Encode()
	return r
}

func openWith(h *ServerlessHandlers, r *http.Request, name string) int {
	rec := httptest.NewRecorder()
	h.HandleWebSocket(rec, r, name, 0)
	return rec.Code
}

func TestHandleWebSocket_capabilityRefusalsHappenBeforeTheUpgrade(t *testing.T) {
	fn := capabilityFn("rpc-router")
	h, authority := capabilityHandlers(t, fn, revokedSet{})
	forger, _ := capability.NewAuthority("another-cluster")
	good, _ := mintFor(t, authority, "anchat-test", "rpc-router", time.Now())
	forged, _ := mintFor(t, forger, "anchat-test", "rpc-router", time.Now())
	expired, _ := mintFor(t, authority, "anchat-test", "rpc-router", time.Now().Add(-2*time.Hour))
	otherFn, _ := mintFor(t, authority, "anchat-test", "other-fn", time.Now())
	otherNS, _ := mintFor(t, authority, "elsewhere", "rpc-router", time.Now())

	for name, token := range map[string]string{
		"forged": forged, "expired": expired, "for another function": otherFn,
		"for another namespace": otherNS, "garbage": "not-a-capability",
	} {
		if code := openWith(h, capabilityRequest(token, nil), "rpc-router"); code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403 before the upgrade", name, code)
		}
	}
	// A genuine one passes every check; this handler has no engine, so the
	// persistent path answers 503 after them.
	if code := openWith(h, capabilityRequest(good, nil), "rpc-router"); code != http.StatusServiceUnavailable {
		t.Errorf("a genuine capability: status %d, want it past the checks (503: no engine here)", code)
	}
}

func TestHandleWebSocket_aRevokedCapabilityOrIssuerIsRefused(t *testing.T) {
	fn := capabilityFn("rpc-router")
	revoked := revokedSet{}
	h, authority := capabilityHandlers(t, fn, revoked)
	token, claims := mintFor(t, authority, "anchat-test", "rpc-router", time.Now())

	revoked[capability.RevocationID("anchat-test", claims.ID)] = true
	if code := openWith(h, capabilityRequest(token, nil), "rpc-router"); code != http.StatusForbidden {
		t.Errorf("a revoked capability: status %d, want 403", code)
	}
	delete(revoked, capability.RevocationID("anchat-test", claims.ID))
	revoked["device:device-1"] = true
	if code := openWith(h, capabilityRequest(token, nil), "rpc-router"); code != http.StatusForbidden {
		t.Errorf("a capability from a revoked device: status %d, want 403", code)
	}
}

func TestHandleWebSocket_capabilityNeedsTheFunctionToAcceptIt(t *testing.T) {
	fn := persistentFn("rpc-router", true, false) // public, but ws_auth unset
	fn.Status = serverless.FunctionStatusActive
	h, authority := capabilityHandlers(t, fn, revokedSet{})
	token, _ := mintFor(t, authority, "anchat-test", "rpc-router", time.Now())
	if code := openWith(h, capabilityRequest(token, nil), "rpc-router"); code != http.StatusForbidden {
		t.Errorf("status %d, want 403: the function does not declare ws_auth: capability", code)
	}

	internal := capabilityFn("migrate")
	internal.IsInternal = true
	h, authority = capabilityHandlers(t, internal, revokedSet{})
	token, _ = mintFor(t, authority, "anchat-test", "migrate", time.Now())
	if code := openWith(h, capabilityRequest(token, nil), "migrate"); code != http.StatusForbidden {
		t.Errorf("status %d, want 403: an internal function is never opened on a capability", code)
	}
}

// Presented alone: a credential beside the capability would name the sender.
func TestHandleWebSocket_aCapabilityWithACredentialIsRefused(t *testing.T) {
	fn := capabilityFn("rpc-router")
	h, authority := capabilityHandlers(t, fn, revokedSet{})
	token, _ := mintFor(t, authority, "anchat-test", "rpc-router", time.Now())
	for name, ctx := range map[string]map[any]any{
		"a token":    {ctxkeys.JWT: &auth.JWTClaims{Sub: "0xwallet", Namespace: "anchat-test"}},
		"an API key": {ctxkeys.APIKey: "ak_something"},
		"hop scopes": {ctxkeys.Scopes: auth.ScopeSet{auth.ScopeInvoke: {}}},
	} {
		if code := openWith(h, capabilityRequest(token, ctx), "rpc-router"); code != http.StatusBadRequest {
			t.Errorf("with %s: status %d, want 400", name, code)
		}
	}
	// A credential that did not verify still names who offered it; the
	// middleware lets it through unverified on a route that needs none.
	for name, offer := range map[string]func(*http.Request){
		"an Authorization header": func(r *http.Request) { r.Header.Set("Authorization", "Bearer x.y.z") },
		"an X-API-Key header":     func(r *http.Request) { r.Header.Set("X-API-Key", "ak_unknown") },
		"a ?jwt= token":           func(r *http.Request) { addQuery(r, "jwt", "x.y.z") },
		"an ?api_key= key":        func(r *http.Request) { addQuery(r, "api_key", "ak_unknown") },
	} {
		r := capabilityRequest(token, nil)
		offer(r)
		if code := openWith(h, r, "rpc-router"); code != http.StatusBadRequest {
			t.Errorf("with %s: status %d, want 400", name, code)
		}
	}
}

func addQuery(r *http.Request, key, value string) {
	q := r.URL.Query()
	q.Set(key, value)
	r.URL.RawQuery = q.Encode()
}

// The function sees what the capability grants and no caller: not even the
// namespace that domain routing put on the request, which a credential-less
// caller would otherwise be reported as.
func TestBuildPersistentInvocationContext_aCapabilitySocketNamesNoCaller(t *testing.T) {
	fn := capabilityFn("rpc-router")
	h, authority := capabilityHandlers(t, fn, revokedSet{})
	token, claims := mintFor(t, authority, "anchat-test", "rpc-router", time.Now())
	r := capabilityRequest(token, map[any]any{ctxkeys.NamespaceOverride: "anchat-test"})
	authorized, _, ok := h.openWithCapability(httptest.NewRecorder(), r, "anchat-test", "rpc-router", 0, token)
	if !ok {
		t.Fatal("a genuine capability was refused")
	}

	got := h.buildPersistentInvocationContext(authorized, fn, "client-1")
	if got.CallerWallet != "" || got.CallerJWTSubject != "" || got.CallerDeviceID != "" || got.CallerIsAdmin || got.CallerHasInvoke {
		t.Errorf("a capability socket reported a caller: %+v", got)
	}
	if got.CallerCapability == nil || *got.CallerCapability != *claims.Grant() {
		t.Errorf("the function sees capability %+v, want %+v", got.CallerCapability, claims.Grant())
	}
	if held := h.socketClaims(authorized); held.Sub != "" || held.Jti != capability.RevocationID("anchat-test", claims.ID) {
		t.Errorf("the socket is held to %+v", held)
	}
}

func TestHandleWebSocket_aGatewayWithoutAuthorityCannotCheckCapabilities(t *testing.T) {
	h := handlersWith(capabilityFn("rpc-router"))
	if code := openWith(h, capabilityRequest("anything", nil), "rpc-router"); code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", code)
	}
}

// The token is checked before the function is looked up: a made-up token costs
// no registry read, and a genuine one for a function that is gone gets the
// same 403, so no probe learns which functions exist.
func TestHandleWebSocket_theTokenIsCheckedBeforeTheFunction(t *testing.T) {
	reg := newMockRegistry()
	h := newTestHandlers(reg)
	authority, _ := capability.NewAuthority("cluster-secret-for-tests")
	h.SetCapabilities(authority, revokedSet{})
	reg.getErr = errRegistryDown

	if code := openWith(h, capabilityRequest("made-up", nil), "ghost"); code != http.StatusForbidden {
		t.Errorf("a made-up token reached the registry: status %d, want 403", code)
	}
	reg.getErr = nil
	token, _ := mintFor(t, authority, "anchat-test", "ghost", time.Now())
	if code := openWith(h, capabilityRequest(token, nil), "ghost"); code != http.StatusForbidden {
		t.Errorf("a function that does not exist: status %d, want the same 403", code)
	}
}

// A capability names a function, not a version, and the function answers to
// being disabled: neither an old version nor a disabled one is opened.
func TestHandleWebSocket_aCapabilityOpensOnlyTheLiveFunction(t *testing.T) {
	fn := capabilityFn("rpc-router")
	h, authority := capabilityHandlers(t, fn, revokedSet{})
	token, _ := mintFor(t, authority, "anchat-test", "rpc-router", time.Now())

	rec := httptest.NewRecorder()
	h.HandleWebSocket(rec, capabilityRequest(token, nil), "rpc-router", 3)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a pinned version: status %d, want 403", rec.Code)
	}
	fn.Status = serverless.FunctionStatusInactive
	if code := openWith(h, capabilityRequest(token, nil), "rpc-router"); code != http.StatusForbidden {
		t.Errorf("a disabled function: status %d, want 403", code)
	}
}

// A socket is opened with GET; any other method on the same path is refused
// before anything else is considered.
func TestHandleWebSocket_aCapabilityIsOnlyAGetUpgrade(t *testing.T) {
	h, authority := capabilityHandlers(t, capabilityFn("rpc-router"), revokedSet{})
	token, _ := mintFor(t, authority, "anchat-test", "rpc-router", time.Now())
	r := capabilityRequest(token, nil)
	r.Method = http.MethodPost
	if code := openWith(h, r, "rpc-router"); code != http.StatusMethodNotAllowed {
		t.Errorf("status %d, want 405", code)
	}
}

// A stateless function is held to the same checks as a persistent one.
func TestHandleWebSocket_aStatelessFunctionRefusesABadCapability(t *testing.T) {
	fn := capabilityFn("relay")
	fn.WSPersistent = false
	h, authority := capabilityHandlers(t, fn, revokedSet{})
	expired, _ := mintFor(t, authority, "anchat-test", "relay", time.Now().Add(-2*time.Hour))
	if code := openWith(h, capabilityRequest(expired, nil), "relay"); code != http.StatusForbidden {
		t.Errorf("status %d, want 403", code)
	}
}

// One capability holds a bounded number of sockets on a gateway at once.
func TestSocketCounter_boundsTheSocketsOfOneCapability(t *testing.T) {
	c := &socketCounter{open: map[string]int{}}
	for i := range maxSocketsPerCapability {
		if !c.acquire("cap-1") {
			t.Fatalf("socket %d of %d was refused", i+1, maxSocketsPerCapability)
		}
	}
	if c.acquire("cap-1") {
		t.Error("a capability opened more sockets than its bound")
	}
	if !c.acquire("cap-2") {
		t.Error("another capability was charged for the first one's sockets")
	}
	c.release("cap-1")
	if !c.acquire("cap-1") {
		t.Error("a closed socket did not free its place")
	}
	for range maxSocketsPerCapability {
		c.release("cap-1")
	}
	if _, left := c.open["cap-1"]; left {
		t.Error("a capability with no sockets is still counted")
	}
}

// auth.refresh on a socket opened with a capability is refused before the
// token is read: the socket stays anonymous, and nothing links it to the
// account whose token was offered.
func TestHandleAuthRefresh_aCapabilitySocketIsRefusedBeforeTheTokenIsRead(t *testing.T) {
	sessions := wssession.NewRegistry(nil)
	sock := sessions.Register(&auth.JWTClaims{Jti: "cap:anchat:1", Did: "device-1",
		Exp: time.Now().Add(time.Hour).Unix()}, func(int, string) {})
	verifier := &fakeJWTVerifier{claims: &auth.JWTClaims{Sub: "0xalice", Namespace: "anchat"}}
	h := newTestHandlers(nil)
	h.SetJWTVerifier(verifier)
	server, client := serverConn(t)

	fn := &serverless.Function{Name: "rpc", Namespace: "anchat"}
	if err := h.handleAuthRefresh(context.Background(), oramaControlFrame{Type: "auth.refresh", JWT: "x.y.z"}, fn, nil, sock, "anchat", "c1", server); err != nil {
		t.Fatalf("ack write: %v", err)
	}
	if ack := readAck(t, client); ack.OK {
		t.Fatal("a capability socket took a token")
	}
	if verifier.calls != 0 {
		t.Errorf("the offered token was verified %d times before the refusal", verifier.calls)
	}
}

var errRegistryDown = errors.New("registry unavailable")

// A capability socket is judged by the capability, not by a credential's
// namespace: with none on the request a genuine token gets past every check,
// and a bad one gets the identical refusal, never the management scoping's.
func TestHandleWebSocket_aCapabilityNeedsNoCredentialNamespace(t *testing.T) {
	fn := capabilityFn("rpc-router")
	h, authority := capabilityHandlers(t, fn, revokedSet{})
	good, _ := mintFor(t, authority, "anchat-test", "rpc-router", time.Now())
	otherNS, _ := mintFor(t, authority, "elsewhere", "rpc-router", time.Now())

	r := capabilityRequest(good, nil)
	if ns := credentialNamespace(r); ns != "" {
		t.Fatalf("the test request carries credential namespace %q", ns)
	}
	if code := openWith(h, r, "rpc-router"); code != http.StatusServiceUnavailable {
		t.Errorf("a genuine capability with no credential: status %d, want it past the checks (503: no engine here)", code)
	}

	rec := httptest.NewRecorder()
	h.HandleWebSocket(rec, capabilityRequest(otherNS, nil), "rpc-router", 0)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), capability.ErrInvalid.Error()) {
		t.Errorf("a capability for another namespace: %d %q, want the identical refusal", rec.Code, rec.Body.String())
	}

	noNamespace := capabilityRequest(good, nil)
	noNamespace.URL.RawQuery = CapabilityQueryParam + "=" + good
	rec = httptest.NewRecorder()
	h.HandleWebSocket(rec, noNamespace, "rpc-router", 0)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), capability.ErrInvalid.Error()) {
		t.Errorf("a capability naming no namespace: %d %q, want the identical refusal", rec.Code, rec.Body.String())
	}
}
