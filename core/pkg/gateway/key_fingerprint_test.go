package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
)

// bug-934: the invoke rate limiter buckets a caller with no wallet by its
// key's fingerprint. The fingerprint is of the stored form, so a key presented
// bare and the same key exchanged for a token are one caller, and it travels
// over the proxy hop under the v5 MAC so a namespace gateway sees it too.

const fingerprintTestKey = "ak_narrowed:alice"

func TestKeyFingerprint_theMainGatewayForwardsOneForBothSpellings(t *testing.T) {
	g := chainGateway(t, "alice", &stubKeyDatabase{namespace: "alice", scopes: "invoke", found: true})
	want := auth.KeyFingerprint(g.authService.HashAPIKey(fingerprintTestKey))

	_, _, bare, errMsg := g.validateAuthForNamespaceProxy(chainRequest(http.MethodPost, "/v1/invoke/alice/x",
		map[string]string{"X-API-Key": fingerprintTestKey}))
	if errMsg != "" || bare.fingerprint != want {
		t.Errorf("bare key forwarded fingerprint %q (err %q), want %q", bare.fingerprint, errMsg, want)
	}

	token, _, err := g.authService.GenerateJWT("alice", g.authService.HashAPIKey(fingerprintTestKey), time.Hour, map[string]string{"scopes": "invoke"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	_, _, exchanged, errMsg := g.validateAuthForNamespaceProxy(chainRequest(http.MethodPost, "/v1/invoke/alice/x",
		map[string]string{"Authorization": "Bearer " + token}))
	if errMsg != "" || exchanged.fingerprint != want {
		t.Errorf("exchanged key forwarded fingerprint %q (err %q), want %q", exchanged.fingerprint, errMsg, want)
	}

	h := http.Header{}
	setInternalAuthKeyHeaders(h, bare)
	if h.Get(HeaderInternalAuthKeyFingerprint) != want {
		t.Errorf("hop header = %q, want %q", h.Get(HeaderInternalAuthKeyFingerprint), want)
	}
}

func TestKeyFingerprint_aBareKeyOnTheGatewayItself(t *testing.T) {
	g := chainGateway(t, "alice", &stubKeyDatabase{namespace: "alice", scopes: "invoke", found: true})
	var got string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = r.Context().Value(ctxKeyKeyFingerprint).(string)
	})
	g.authMiddleware(next).ServeHTTP(httptest.NewRecorder(),
		chainRequest(http.MethodPost, "/v1/invoke/alice/x", map[string]string{"X-API-Key": fingerprintTestKey}))
	if want := auth.KeyFingerprint(g.authService.HashAPIKey(fingerprintTestKey)); got != want {
		t.Errorf("fingerprint in the request = %q, want %q", got, want)
	}
}

// fingerprintHop is a bare-key hop that carries a fingerprint.
func fingerprintHop(t *testing.T, g *Gateway, fingerprint string) *http.Request {
	t.Helper()
	const path = "/v1/functions/rpc/invoke"
	r := httptest.NewRequest(http.MethodPost, path, nil)
	r.Header.Set(HeaderInternalAuthValidated, "true")
	r.Header.Set(HeaderInternalAuthNamespace, hopNamespace)
	setInternalAuthKeyHeaders(r.Header, keyAuthority{scopes: "invoke", fingerprint: fingerprint})
	if err := signInternalAuthHeaders(g.internalAuthKey, r.Header, r.Method, path, time.Now()); err != nil {
		t.Fatalf("sign: %v", err)
	}
	return r
}

func TestKeyFingerprint_behindTheHop(t *testing.T) {
	g, _ := namespaceGatewayForHops(t, "runtime")
	var got string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = r.Context().Value(ctxKeyKeyFingerprint).(string)
	})
	g.internalAuthMiddleware(g.authMiddleware(next)).ServeHTTP(httptest.NewRecorder(), fingerprintHop(t, g, "key_0123456789ab"))
	if got != "key_0123456789ab" {
		t.Errorf("fingerprint behind the hop = %q, want key_0123456789ab", got)
	}
}

func TestKeyFingerprint_theHopMACCoversIt(t *testing.T) {
	g, _ := namespaceGatewayForHops(t, "runtime")
	for name, edit := range map[string]func(http.Header){
		"changed": func(h http.Header) { h.Set(HeaderInternalAuthKeyFingerprint, "key_ffffffffffff") },
		"removed": func(h http.Header) { h.Del(HeaderInternalAuthKeyFingerprint) },
	} {
		r := fingerprintHop(t, g, "key_0123456789ab")
		edit(r.Header)
		if verifyInternalAuthHeaders(g.internalAuthKey, r, time.Now()) {
			t.Errorf("a %s fingerprint still verified", name)
		}
	}
}

// A main gateway that predates v5 forwards no fingerprint the namespace
// gateway may believe: one it carries anyway is dropped, not read.
func TestKeyFingerprint_aV4HopCarriesNone(t *testing.T) {
	g, _ := namespaceGatewayForHops(t, "runtime")
	r := fingerprintHop(t, g, "key_0123456789ab")
	r.Header.Del(HeaderInternalAuthMACv8)
	r.Header.Del(HeaderInternalAuthMACv7)
	r.Header.Del(HeaderInternalAuthMACv6)
	r.Header.Del(HeaderInternalAuthMACv5)
	var got string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = r.Context().Value(ctxKeyKeyFingerprint).(string)
	})
	g.internalAuthMiddleware(g.authMiddleware(next)).ServeHTTP(httptest.NewRecorder(), r)
	if got != "" {
		t.Errorf("a v4 hop's unauthenticated fingerprint %q reached the handler", got)
	}
}
