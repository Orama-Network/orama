//go:build e2e_fleet

package deviceattestation

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/Orama-Network/orama/e2e/harness"
	"github.com/Orama-Network/orama/e2e/harness/eventually"
	"github.com/Orama-Network/orama/e2e/harness/gw"
	"github.com/Orama-Network/orama/e2e/harness/oramacli"
)

// TestAttestationPolicy_setReadAndWhoMay: the CLI and the route set and read
// the policy; an incomplete one is refused whole; a runtime member may not set
// it (docs/whitepaper/technical-reference/vol1/14-authorization.md#device-attestation-at-the-gateway).
func TestAttestationPolicy_setReadAndWhoMay(t *testing.T) {
	t.Parallel()
	fx := setup(t)
	got := setPolicy(t, fx, modeMonitor)
	if got.Mode != modeMonitor || got.Android.Package != testPackage || len(got.Android.SigningCertDigests) != 1 ||
		got.Android.SigningCertDigests[0] != testDigest || got.ReattestInterval != "168h0m0s" {
		t.Errorf("the CLI set %+v", got)
	}
	var read policyView
	if err := fx.c.MustSend(t, gw.Req{Path: pathPolicy, Bearer: fx.admin.AccessToken}).Expect(t, http.StatusOK).Decode(&read); err != nil {
		t.Fatal(err)
	}
	if read.Mode != modeMonitor || read.Namespace != fx.n.Name {
		t.Errorf("the route reads %+v", read)
	}
	res, err := fx.n.CLI.Run(t.Context(), "namespace", "attestation-policy", "--namespace", fx.n.Name,
		"--android-signing-digest", "not-a-digest")
	if err != nil || res.Exit == 0 {
		t.Errorf("a malformed digest was accepted: %v", err)
	}
	expectCode(t, send(t, fx.c, http.MethodPut, pathPolicy, fx.admin.AccessToken, map[string]any{"android": map[string]any{"signing_cert_digests": []string{}}}),
		http.StatusBadRequest, "ATTESTATION_POLICY_INVALID")
	expectCode(t, send(t, fx.c, http.MethodPut, pathPolicy, fx.admin.AccessToken, map[string]any{"mood": "enforce"}),
		http.StatusBadRequest, "ATTESTATION_MALFORMED")
	runtime := signIn(t, fx.c, fx.user, fx.n.Name, nil)
	expectCode(t, send(t, fx.c, http.MethodPut, pathPolicy, runtime.AccessToken, map[string]any{"mode": modeOff}),
		http.StatusForbidden, "INSUFFICIENT_SCOPE")
}

// TestAttestation_enforceRefusesAScript: under enforce, a script holding a
// valid session JWT and the app's API key reaches neither the function over
// HTTP nor its WebSocket nor a pub/sub subscription, and is told why by name;
// the owner, who is not judged, still invokes.
func TestAttestation_enforceRefusesAScript(t *testing.T) {
	t.Parallel()
	fx := setup(t)
	session := signIn(t, fx.c, fx.user, fx.n.Name, fx.dev)
	if resp := invoke(t, fx, session.AccessToken, nil); resp.Status != http.StatusOK {
		t.Fatalf("before any policy the session could not invoke: %d %s", resp.Status, resp.Body)
	}
	setPolicy(t, fx, modeEnforce)

	eventually.Require(t, pollEvery, policyBudget, "every gateway to refuse the unattested session", func() (bool, error) {
		resp := invoke(t, fx, session.AccessToken, nil)
		if resp.Status == http.StatusForbidden && resp.ErrorCode() == codeRequired {
			return true, nil
		}
		return false, fmt.Errorf("HTTP %d %s", resp.Status, resp.ErrorCode())
	})
	expectCode(t, invoke(t, fx, "", http.Header{"X-API-Key": {fx.appKey}}), http.StatusForbidden, codeRequired)

	for name, open := range map[string]func() (int, string){
		"the function WebSocket with the session": func() (int, string) {
			return refusedUpgrade(t, fx, "/v1/functions/"+fixtureName+"/ws?"+url.Values{"namespace": {fx.n.Name}}.Encode(), session.AccessToken, nil)
		},
		"the function WebSocket with the key": func() (int, string) {
			return refusedUpgrade(t, fx, "/v1/functions/"+fixtureName+"/ws?"+url.Values{"namespace": {fx.n.Name}}.Encode(), "", http.Header{"X-API-Key": {fx.appKey}})
		},
		"a subscription with the session": func() (int, string) {
			return refusedUpgrade(t, fx, pathPubsubWS+"?topic=e2e-attest", session.AccessToken, nil)
		},
	} {
		if status, code := open(); status != http.StatusForbidden || code != codeRequired {
			t.Errorf("%s: HTTP %d %s, want 403 %s", name, status, code, codeRequired)
		}
	}
	// The operator who owns the namespace is not judged: the CLI is no app.
	fx.n.CLI.MustOK(t, "function", "invoke", fixtureName)
}

// TestAttestation_monitorRefusesNothingAndCounts: under monitor the same
// script is served, and the cluster's would-refuse count rises.
func TestAttestation_monitorRefusesNothingAndCounts(t *testing.T) {
	t.Parallel()
	fx := setup(t)
	before := wouldRefuse(t)
	setPolicy(t, fx, modeMonitor)
	session := signIn(t, fx.c, fx.user, fx.n.Name, fx.dev)
	for range monitorBurst {
		if resp := invoke(t, fx, session.AccessToken, nil); resp.Status != http.StatusOK {
			t.Fatalf("monitor refused the session: %d %s", resp.Status, resp.Body)
		}
		if resp := invoke(t, fx, "", http.Header{"X-API-Key": {fx.appKey}}); resp.Status != http.StatusOK {
			t.Fatalf("monitor refused the key: %d %s", resp.Status, resp.Body)
		}
	}
	conn, resp, err := fx.c.For(t).DialWS(t.Context(), pathPubsubWS+"?topic=e2e-attest", session.AccessToken, nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("monitor refused a subscription: HTTP %d: %v", status, err)
	}
	conn.Close()
	eventually.Require(t, pollEvery, countBudget, "the would-refuse count to rise", func() (bool, error) {
		now := wouldRefuse(t)
		if now >= before+2*monitorBurst {
			return true, nil
		}
		return false, fmt.Errorf("would-refuse count %d, was %d", now, before)
	})
}

// TestAttestation_verdictInEveryMode: get_caller_attestation_verdict tells a
// function the gateway's verdict on its caller under the policy as it is at
// the call: not_configured while the mode is off, and under monitor (which
// refuses nothing) what enforcing would decide; under enforce a caller the
// gate exempts (an admin) is still told its device never attested
// (website/src/docs/developer/device-attestation.mdx#in-a-function).
func TestAttestation_verdictInEveryMode(t *testing.T) {
	t.Parallel()
	fx := setup(t)
	session := signIn(t, fx.c, fx.user, fx.n.Name, fx.dev)
	if got := fixtureVerdict(t, invoke(t, fx, session.AccessToken, nil)); got.Mode != modeOff || got.Verdict != verdictNotConfigured {
		t.Errorf("with no policy the function was told %+v", got)
	}

	for _, step := range []struct {
		mode, bearer string
	}{
		{modeMonitor, session.AccessToken},
		{modeEnforce, fx.admin.AccessToken},
	} {
		setPolicy(t, fx, step.mode)
		eventually.Require(t, pollEvery, policyBudget, "the function to be told the "+step.mode+" verdict", func() (bool, error) {
			resp := invoke(t, fx, step.bearer, nil)
			if resp.Status != http.StatusOK {
				return false, fmt.Errorf("HTTP %d %s", resp.Status, resp.ErrorCode())
			}
			got := fixtureVerdict(t, resp)
			if got.Mode == step.mode && got.Verdict == verdictRequired && got.Message != "" {
				return true, nil
			}
			return false, fmt.Errorf("told %+v", got)
		})
	}
}

// TestAttest_challengeAndProofs: a challenge is the device-bound session's,
// used once; a key gets none; a forged Android chain or iOS attestation is
// ATTESTATION_INVALID and says nothing about which check failed.
func TestAttest_challengeAndProofs(t *testing.T) {
	t.Parallel()
	fx := setup(t)
	setPolicy(t, fx, modeMonitor)
	session := signIn(t, fx.c, fx.user, fx.n.Name, fx.dev)
	b64 := base64.StdEncoding.EncodeToString

	ch := challenge(t, fx, session.AccessToken)
	if ch.DeviceID != fx.dev.ID() {
		t.Errorf("the challenge names device %q, want %q", ch.DeviceID, fx.dev.ID())
	}
	forged := []string{b64([]byte("leaf")), b64([]byte("intermediate")), b64([]byte("root"))}
	body := expectCode(t, postJSON(t, fx.c, pathAttestAndroid, session.AccessToken, map[string]any{"challenge": ch.Challenge, "chain": forged}),
		http.StatusForbidden, "ATTESTATION_INVALID")
	if msg, _ := body["error"].(string); msg != invalidMessage {
		t.Errorf("the refusal says %q, want only %q", msg, invalidMessage)
	}
	expectCode(t, postJSON(t, fx.c, pathAttestAndroid, session.AccessToken, map[string]any{"challenge": ch.Challenge, "chain": forged}),
		http.StatusUnauthorized, "ATTESTATION_CHALLENGE_INVALID")

	setPolicy(t, fx, modeMonitor, "--ios-team-id", "ABCDE12345", "--ios-bundle-id", "network.orama.e2e")
	ios := challenge(t, fx, session.AccessToken)
	expectCode(t, postJSON(t, fx.c, pathAttestIOS, session.AccessToken,
		map[string]any{"challenge": ios.Challenge, "key_id": b64([]byte("key")), "attestation": b64([]byte("not cbor"))}),
		http.StatusForbidden, "ATTESTATION_INVALID")

	expectCode(t, postJSON(t, fx.c, pathChallenge, "", nil), http.StatusUnauthorized, "AUTH_MISSING")
	keyed := fx.c.MustSend(t, gw.Req{Method: http.MethodPost, Path: pathChallenge, Header: http.Header{"X-API-Key": {fx.appKey}}})
	expectCode(t, keyed, http.StatusUnauthorized, "ATTESTATION_NEEDS_DEVICE")
	unbound := signIn(t, fx.c, fx.user, fx.n.Name, nil)
	expectCode(t, postJSON(t, fx.c, pathChallenge, unbound.AccessToken, nil), http.StatusForbidden, "ATTESTATION_NEEDS_DEVICE")
}

// wouldRefuse is the cluster's count of requests monitor mode would have
// refused for want of an attestation (`orama status traffic --json`).
func wouldRefuse(t *testing.T) int64 {
	t.Helper()
	var v struct {
		Attestation []struct {
			Code  string `json:"code"`
			Mode  string `json:"mode"`
			Count int64  `json:"count"`
		} `json:"attestation"`
	}
	res := harness.CLI(t).MustOK(t, "status", "traffic", "--json", "--env", harness.Fleet(t).State.Env)
	if err := oramacli.DecodeJSON(res, &v); err != nil {
		t.Fatalf("orama status traffic --json: %v", err)
	}
	var n int64
	for _, a := range v.Attestation {
		if a.Code == codeRequired && a.Mode == modeMonitor {
			n += a.Count
		}
	}
	return n
}
