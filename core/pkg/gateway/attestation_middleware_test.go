package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gwauth "github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/ctxkeys"
	"github.com/Orama-Network/orama/core/pkg/telemetry/traffic"
)

const attestTestDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// attestingGateway is a gateway over a real registry whose "anchat"
// namespace has an Android attestation policy in mode.
func attestingGateway(t *testing.T, mode string) (*Gateway, *sql.DB) {
	t.Helper()
	g, q, db := keyRegistry(t)
	g.logger = newRQLiteTestLogger()
	g.client = &sqliteKeyNet{db: q}
	g.traffic = traffic.New(nil)
	if _, err := db.Exec(`INSERT INTO namespace_attestation_policy(namespace_id, mode, android_package,
		android_signing_digests, android_min_version_code, updated_by) VALUES (10, ?, 'chat.anchat', ?, 400, 'test')`,
		mode, `["`+attestTestDigest+`"]`); err != nil {
		t.Fatalf("store the policy: %v", err)
	}
	return g, db
}

// appRequest is a request to path from a caller in "anchat": claims when the
// caller signed in, otherwise a bare key holding scopes.
func appRequest(method, path string, claims *gwauth.JWTClaims, scopes string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	ctx := context.WithValue(r.Context(), CtxKeyNamespaceOverride, "anchat")
	if claims != nil {
		ctx = context.WithValue(ctx, ctxKeyJWT, claims)
	}
	if scopes != "" {
		ctx = withKeyAuthority(ctx, scopes, nil)
	}
	return r.WithContext(ctx)
}

func attestedClaims(version string) *gwauth.JWTClaims {
	return &gwauth.JWTClaims{Sub: "0x00000000000000000000000000000000000000aa", Namespace: "anchat", Did: "device-1",
		Att: &gwauth.AttestationClaim{Platform: "android", AppID: "chat.anchat", AppVersion: version,
			SigningCertDigests: []string{attestTestDigest}, AttestedAt: time.Now().Unix()}}
}

// serveAttested runs one request through the middleware and reports the
// status, the refusal code and whether the handler ran.
func serveAttested(g *Gateway, r *http.Request) (int, string, *http.Request) {
	var reached *http.Request
	rec := httptest.NewRecorder()
	g.attestationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = r
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, r)
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body.Code, reached
}

func attestationCount(g *Gateway, code, platform, mode string) int64 {
	for _, a := range g.traffic.Snapshot(time.Now()).Attestation {
		if a.Code == code && a.Platform == platform && a.Mode == mode {
			return a.Count
		}
	}
	return 0
}

// Under enforce, a script holding a valid key or session but no attestation
// is refused by name on the app surface — function invoke and the data plane —
// and counted; the namespace's own attested app is admitted.
func TestAttestationMiddleware_enforceRefusesTheUnattested(t *testing.T) {
	g, _ := attestingGateway(t, "enforce")
	script := &gwauth.JWTClaims{Sub: "0x00000000000000000000000000000000000000bb", Namespace: "anchat", Did: "device-2"}
	for _, r := range []*http.Request{
		appRequest(http.MethodPost, "/v1/functions/rpc/invoke", nil, "invoke"),
		appRequest(http.MethodPost, "/v1/invoke/anchat/rpc", script, ""),
		appRequest(http.MethodGet, "/v1/pubsub/ws", script, ""),
		appRequest(http.MethodPost, "/v1/storage/upload", script, ""),
	} {
		if status, code, reached := serveAttested(g, r); status != http.StatusForbidden || code != CodeAttestationRequired || reached != nil {
			t.Errorf("%s: %d %s, handler ran=%v; want 403 %s", r.URL.Path, status, code, reached != nil, CodeAttestationRequired)
		}
	}
	if n := attestationCount(g, CodeAttestationRequired, attestationPlatformNone, "enforce"); n != 4 {
		t.Errorf("counted %d refusals, want 4", n)
	}
	if status, _, _ := serveAttested(g, appRequest(http.MethodPost, "/v1/functions/rpc/invoke", attestedClaims("412"), "")); status != http.StatusOK {
		t.Errorf("the attested app was refused: %d", status)
	}
	status, code, _ := serveAttested(g, appRequest(http.MethodPost, "/v1/functions/rpc/invoke", attestedClaims("399"), ""))
	if status != http.StatusForbidden || code != CodeAppUpdateRequired {
		t.Errorf("an old build: %d %s, want 403 %s", status, code, CodeAppUpdateRequired)
	}
	if n := attestationCount(g, CodeAppUpdateRequired, "android", "enforce"); n != 1 {
		t.Errorf("the old build was counted %d times", n)
	}

	// feat-965: an app signed with a certificate the policy does not name is
	// refused by name, and a token that records no certificate attests again.
	resigned := attestedClaims("412")
	resigned.Att.SigningCertDigests = []string{"ab" + attestTestDigest[2:]}
	if status, code, _ := serveAttested(g, appRequest(http.MethodPost, "/v1/functions/rpc/invoke", resigned, "")); status != http.StatusForbidden || code != CodeAttestationInvalid {
		t.Errorf("a certificate the policy does not name: %d %s, want 403 %s", status, code, CodeAttestationInvalid)
	}
	unrecorded := attestedClaims("412")
	unrecorded.Att.SigningCertDigests = nil
	if status, code, _ := serveAttested(g, appRequest(http.MethodPost, "/v1/functions/rpc/invoke", unrecorded, "")); status != http.StatusForbidden || code != CodeAttestationRequired {
		t.Errorf("a token that records no certificate: %d %s, want 403 %s", status, code, CodeAttestationRequired)
	}
}

// Monitor refuses nothing, and counts what enforce would refuse.
func TestAttestationMiddleware_monitorRefusesNothingAndCounts(t *testing.T) {
	g, _ := attestingGateway(t, "monitor")
	if status, _, reached := serveAttested(g, appRequest(http.MethodPost, "/v1/functions/rpc/invoke", nil, "invoke")); status != http.StatusOK || reached == nil {
		t.Fatalf("monitor refused: %d", status)
	}
	if n := attestationCount(g, CodeAttestationRequired, attestationPlatformNone, "monitor"); n != 1 {
		t.Errorf("the would-refuse count is %d, want 1", n)
	}
}

// What attestation does not judge: the control plane, sign-in and the
// attestation routes, the open routes, a key with a control-plane grant, and a
// deployed app's workload token.
func TestAttestationMiddleware_whatItDoesNotJudge(t *testing.T) {
	g, _ := attestingGateway(t, "enforce")
	cases := map[string]*http.Request{
		"deploying a function":        appRequest(http.MethodPost, "/v1/functions", nil, "invoke"),
		"refreshing a session":        appRequest(http.MethodPost, "/v1/auth/refresh", nil, "invoke"),
		"asking for a challenge":      appRequest(http.MethodPost, "/v1/attest/challenge", &gwauth.JWTClaims{Sub: "0xab", Did: "d"}, ""),
		"setting the policy":          appRequest(http.MethodPut, "/v1/namespace/attestation-policy", nil, "admin"),
		"health":                      appRequest(http.MethodGet, "/v1/health", nil, ""),
		"an admin key invoking":       appRequest(http.MethodPost, "/v1/functions/rpc/invoke", nil, "admin"),
		"a workload token publishing": appRequest(http.MethodPost, "/v1/pubsub/publish", &gwauth.JWTClaims{Sub: "app:anchat/backend", Namespace: "anchat"}, ""),
	}
	for name, r := range cases {
		if status, code, _ := serveAttested(g, r); status != http.StatusOK {
			t.Errorf("%s: %d %s", name, status, code)
		}
	}
}

// A WebSocket judged at its upgrade carries the namespace whose policy holds
// it; the sweeper's gate then closes it once the policy enforces and does not
// admit its token, and monitor closes nothing.
func TestAttestation_socketsAreHeldToThePolicy(t *testing.T) {
	g, db := attestingGateway(t, "monitor")
	r := appRequest(http.MethodGet, "/v1/pubsub/ws", nil, "pubsub")
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	_, _, reached := serveAttested(g, r)
	if reached == nil {
		t.Fatal("monitor refused the upgrade")
	}
	if ns, _ := reached.Context().Value(ctxkeys.AttestationNamespace).(string); ns != "anchat" {
		t.Errorf("the upgrade holds the socket to %q", ns)
	}

	gate := attestationSocketGate{authService: g.authService}
	if code, err := gate.AttestationRefusal("anchat", &gwauth.JWTClaims{}, time.Now()); err != nil || code != "" {
		t.Errorf("monitor closed a socket: %q, %v", code, err)
	}
	if _, err := db.Exec(`UPDATE namespace_attestation_policy SET mode = 'enforce'`); err != nil {
		t.Fatal(err)
	}
	if err := g.authService.AttestationPolicies().Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if code, err := gate.AttestationRefusal("anchat", &gwauth.JWTClaims{}, time.Now()); err != nil || code != CodeAttestationRequired {
		t.Errorf("enforce left an unattested socket open: %q, %v", code, err)
	}
	if code, _ := gate.AttestationRefusal("anchat", attestedClaims("412"), time.Now()); code != "" {
		t.Errorf("enforce closed the attested app's socket: %q", code)
	}
}

// The index gateway judges a request it forwards to a namespace gateway with
// the credential it validated, and counts it there.
func TestAdmitAttestedProxy_judgesTheValidatedCredential(t *testing.T) {
	g, _ := attestingGateway(t, "enforce")
	r := httptest.NewRequest(http.MethodPost, "/v1/functions/rpc/invoke", nil)
	rec := httptest.NewRecorder()
	if g.admitAttestedProxy(rec, r, "anchat", namespaceProxyAuth{namespace: "anchat", key: keyAuthority{scopes: "invoke"}}) {
		t.Fatal("an unattested key was forwarded")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("answered %d", rec.Code)
	}
	if !g.admitAttestedProxy(httptest.NewRecorder(), r, "anchat", namespaceProxyAuth{namespace: "anchat", claims: attestedClaims("412")}) {
		t.Error("the attested app was not forwarded")
	}
}

// A wallet that operates the namespace is not judged: the CLI is no app build,
// and locking out the owner would lock out the one who sets the policy. A
// wallet that holds no grant — an end user — is.
func TestAttestationMiddleware_theOwnerIsNotJudged(t *testing.T) {
	g, db := attestingGateway(t, "enforce")
	owner := "0x00000000000000000000000000000000000000cc"
	res, err := db.Exec(`INSERT INTO principals(type, identifier, created_by) VALUES ('wallet', ?, 'test')`, owner)
	if err != nil {
		t.Fatalf("principal: %v", err)
	}
	id, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO grants(principal_id, namespace_id, role, created_by) VALUES (?, 10, 'owner', 'test')`, id); err != nil {
		t.Fatalf("grant: %v", err)
	}
	ownerSession := &gwauth.JWTClaims{Sub: owner, Namespace: "anchat"}
	if status, code, _ := serveAttested(g, appRequest(http.MethodPost, "/v1/functions/rpc/invoke", ownerSession, "")); status != http.StatusOK {
		t.Errorf("the owner was refused: %d %s", status, code)
	}
	endUser := &gwauth.JWTClaims{Sub: "0x00000000000000000000000000000000000000dd", Namespace: "anchat"}
	if status, _, _ := serveAttested(g, appRequest(http.MethodPost, "/v1/functions/rpc/invoke", endUser, "")); status != http.StatusForbidden {
		t.Errorf("an end user's unattested session was admitted: %d", status)
	}
}
