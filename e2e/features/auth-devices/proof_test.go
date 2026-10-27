//go:build e2e_fleet

package authdevices

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/e2e/harness"
	"github.com/Orama-Network/orama/e2e/harness/eventually"
	"github.com/Orama-Network/orama/e2e/harness/gw"
	"github.com/Orama-Network/orama/e2e/harness/ns"
	"github.com/Orama-Network/orama/e2e/harness/wallet"
)

const (
	// proofWindow: "iat must be within 60 seconds of the gateway's clock".
	proofWindow = 60 * time.Second
	// skewedClock is how far off the phone's clock is in the bug-961 report.
	skewedClock = 10 * time.Minute
	// deviceRefreshPrefix marks a device-bound refresh token (docs/whitepaper/technical-reference/vol1/13-identity.md#signing-in-with-a-device).
	deviceRefreshPrefix = "dv1_"
)

func refreshWith(t testing.TB, c *gw.Client, token, namespace string, p *wallet.Proof) *gw.Response {
	t.Helper()
	body := map[string]any{"refresh_token": token, "namespace": namespace}
	if p != nil {
		body["device_proof"] = p
	}
	return postJSON(t, c, gw.PathRefresh, "", body)
}

// TestDeviceRefresh_needsAFreshSingleUseProof: a device-bound refresh token
// is marked dv1_ and renews only with the device's proof: fresh, single use,
// for this action on this token, from this device. A refused refresh spends
// nothing — the token and its rotation are still there for the real device
// (docs/whitepaper/technical-reference/vol1/13-identity.md#linking-a-device-from-a-device).
func TestDeviceRefresh_needsAFreshSingleUseProof(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	d, other := gw.NewDevice(t, wallet.AlgEd25519), gw.NewDevice(t, wallet.AlgES256)
	s := signInSession(t, c, member(t, n, roleRuntime), n.Name, d)
	if !strings.HasPrefix(s.RefreshToken, deviceRefreshPrefix) {
		t.Errorf("a device-bound refresh token is not marked %s", deviceRefreshPrefix)
	}
	rt := s.RefreshToken
	for _, tc := range badRefreshProofs(t, d, other, n.Name, rt) {
		expectCodeNamed(t, tc.name, refreshWith(t, c, rt, n.Name, tc.p), http.StatusUnauthorized, tc.code)
	}
	good := proof(t, d, wallet.ProofRefresh, n.Name, rt)
	var next gw.Session
	if err := refreshWith(t, c, rt, n.Name, good).Expect(t, http.StatusOK).Decode(&next); err != nil {
		t.Fatal(err)
	}
	protect(t, c, next.AccessToken, next.RefreshToken)
	if next.DeviceID != "" && next.DeviceID != d.ID() {
		t.Errorf("the refreshed session names device %q", next.DeviceID)
	}
	replayed := proof(t, d, wallet.ProofRefresh, n.Name, next.RefreshToken)
	refreshWith(t, c, next.RefreshToken, n.Name, replayed).Expect(t, http.StatusOK)
	expectCodeNamed(t, "a spent proof id", refreshWith(t, c, next.RefreshToken, n.Name, replayed), http.StatusUnauthorized, "DEVICE_PROOF_INVALID")
}

type proofCase struct {
	name string
	p    *wallet.Proof
	code string
}

// TestDeviceRefresh_aSkewedClockIsToldTheGatewaysTime: a phone whose clock is
// ten minutes ahead or behind is refused by name (DEVICE_PROOF_CLOCK_SKEW),
// not as a forged or replayed proof, and the refusal and every answer's Date
// header carry the gateway's clock. A proof stamped on that clock refreshes;
// replaying it is still refused, as is every spent proof (bug-961,
// docs/whitepaper/technical-reference/vol1/13-identity.md#device-proofs).
func TestDeviceRefresh_aSkewedClockIsToldTheGatewaysTime(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	d := gw.NewDevice(t, wallet.AlgEd25519)
	s := signInSession(t, c, member(t, n, roleRuntime), n.Name, d)
	rt := s.RefreshToken
	for name, skew := range map[string]time.Duration{"ahead": skewedClock, "behind": -skewedClock} {
		skewed, err := d.ProofAt(wallet.ProofRefresh, n.Name, rt, time.Now().Add(skew), randomB64(t, 24))
		if err != nil {
			t.Fatal(err)
		}
		resp := refreshWith(t, c, rt, n.Name, skewed)
		body := expectCode(t, resp, http.StatusUnauthorized, "DEVICE_PROOF_CLOCK_SKEW")
		serverTime, ok := body["server_time"].(float64)
		if !ok {
			t.Fatalf("a clock %s: the refusal carries no server_time: %s", name, resp.Body)
		}
		if drift := time.Since(time.Unix(int64(serverTime), 0)).Abs(); drift > proofWindow {
			t.Errorf("a clock %s: server_time is %s from the runner's clock", name, drift)
		}
		date, err := http.ParseTime(resp.Header.Get("Date"))
		if err != nil {
			t.Errorf("a clock %s: the refusal has no Date header the SDK can read: %v", name, err)
		} else if drift := date.Sub(time.Unix(int64(serverTime), 0)).Abs(); drift > 2*time.Second {
			t.Errorf("a clock %s: Date %s and server_time %v disagree", name, date, serverTime)
		}
	}
	corrected, err := d.ProofAt(wallet.ProofRefresh, n.Name, rt, time.Now(), randomB64(t, 24))
	if err != nil {
		t.Fatal(err)
	}
	var next gw.Session
	if err := refreshWith(t, c, rt, n.Name, corrected).Expect(t, http.StatusOK).Decode(&next); err != nil {
		t.Fatalf("the proof on the gateway's clock after two skew refusals (nothing was spent): %v", err)
	}
	protect(t, c, next.AccessToken, next.RefreshToken)
	expectCodeNamed(t, "the corrected proof replayed", refreshWith(t, c, next.RefreshToken, n.Name, corrected), http.StatusUnauthorized, "DEVICE_PROOF_INVALID")
}

// gatewaySecondAfter waits until the gateway's clock, as its Date header
// reads it, is past the second resp was answered in.
func gatewaySecondAfter(t testing.TB, c *gw.Client, resp *gw.Response) {
	t.Helper()
	answered, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		t.Fatalf("the gateway's answer has no Date header: %v", err)
	}
	eventually.Require(t, pollEvery, revocationStaleness, "the gateway's clock to leave "+answered.String(), func() (bool, error) {
		now, err := http.ParseTime(c.MustSend(t, gw.Req{Path: gw.PathWhoami}).Header.Get("Date"))
		if err != nil {
			return false, err
		}
		return now.After(answered), nil
	})
}

// badRefreshProofs is every way a refresh proof can be wrong: missing, stale,
// from the future, a short id, another action, token, namespace or device.
func badRefreshProofs(t testing.TB, d, other *wallet.Device, namespace, rt string) []proofCase {
	t.Helper()
	now := time.Now()
	at := func(when time.Time, id string) *wallet.Proof {
		p, err := d.ProofAt(wallet.ProofRefresh, namespace, rt, when, id)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	return []proofCase{
		{"no proof", nil, "DEVICE_PROOF_REQUIRED"},
		{"stale proof", at(now.Add(-2*proofWindow), randomB64(t, 24)), "DEVICE_PROOF_CLOCK_SKEW"},
		{"proof from the future", at(now.Add(2*proofWindow), randomB64(t, 24)), "DEVICE_PROOF_CLOCK_SKEW"},
		{"id too short", at(now, "tooShort"), "DEVICE_PROOF_INVALID"},
		{"proof for another action", proof(t, d, wallet.ProofRevoke, namespace, rt), "DEVICE_PROOF_INVALID"},
		{"proof over another token", proof(t, d, wallet.ProofRefresh, namespace, rt+"x"), "DEVICE_PROOF_INVALID"},
		{"proof for another namespace", proof(t, d, wallet.ProofRefresh, "default", rt), "DEVICE_PROOF_INVALID"},
		{"another device's proof", proof(t, other, wallet.ProofRefresh, namespace, rt), "DEVICE_PROOF_INVALID"},
	}
}

// expectCodeNamed is expectCode reporting with Errorf, so a table goes on.
func expectCodeNamed(t testing.TB, name string, resp *gw.Response, status int, code string) {
	t.Helper()
	if resp.Status != status || resp.ErrorCode() != code {
		t.Errorf("%s: want %d %s, got %d %.300s", name, status, code, resp.Status, resp.Body)
	}
}

// TestDeviceSession_actsNeedTheDevicesProof: from a device-bound session,
// ending a session and revoking a device take the device's proof over the
// target; a session bound to no device needs none (docs/whitepaper/technical-reference/vol1/13-identity.md#revoking-a-device).
func TestDeviceSession_actsNeedTheDevicesProof(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	w := member(t, n, roleRuntime)
	d, spare := gw.NewDevice(t, wallet.AlgEd25519), gw.NewDevice(t, wallet.AlgEd25519)
	bound := signInSession(t, c, w, n.Name, d)
	signInSession(t, c, w, n.Name, spare)
	plain := signInSession(t, c, w, n.Name, nil)
	sessions, _, err := c.For(t).Sessions(t.Context(), plain.AccessToken)
	if err != nil || len(sessions) < 3 {
		t.Fatalf("sessions: %v %+v", err, sessions)
	}
	target := sessions[0].ID
	resp, _ := c.For(t).EndSession(t.Context(), bound.AccessToken, target, nil)
	expectCode(t, resp, http.StatusUnauthorized, "DEVICE_PROOF_REQUIRED")
	resp, _ = c.For(t).EndSession(t.Context(), bound.AccessToken, target, proof(t, d, wallet.ProofEndSession, n.Name, "999999"))
	expectCode(t, resp, http.StatusUnauthorized, "DEVICE_PROOF_INVALID")
	resp, _ = c.For(t).RevokeDevice(t.Context(), bound.AccessToken, spare.ID(), nil)
	expectCode(t, resp, http.StatusUnauthorized, "DEVICE_PROOF_REQUIRED")
	resp, _ = c.For(t).RevokeDevice(t.Context(), bound.AccessToken, spare.ID(), proof(t, d, wallet.ProofRevoke, n.Name, d.ID()))
	expectCode(t, resp, http.StatusUnauthorized, "DEVICE_PROOF_INVALID")
	if _, err := c.For(t).RevokeDevice(t.Context(), plain.AccessToken, spare.ID(), nil); err != nil {
		t.Fatalf("a session bound to no device (only the wallet can make one) must revoke without a proof: %v", err)
	}
	if _, err := c.For(t).RevokeDevice(t.Context(), bound.AccessToken, spare.ID(), proof(t, d, wallet.ProofRevoke, n.Name, spare.ID())); err != nil {
		t.Fatalf("revoking again with a proper proof is safe to repeat: %v", err)
	}
}

// TestDeviceSession_logoutAllNeedsTheDevicesProof: from a device-bound
// session, POST /v1/auth/logout {"all": true} with the access token alone is
// refused by name and every other session of the account stays valid; a
// proof for another action does not do; the device's logout-all proof over its
// own id ends every session on every gateway, and is good once. A session
// bound to no device logs out everywhere without one (bug-960,
// docs/whitepaper/technical-reference/vol1/13-identity.md#revoking-a-device).
func TestDeviceSession_logoutAllNeedsTheDevicesProof(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	nodes := perNode(t, f, c)
	w := member(t, n, roleRuntime)
	d, spare := gw.NewDevice(t, wallet.AlgEd25519), gw.NewDevice(t, wallet.AlgES256)
	bound := signInSession(t, c, w, n.Name, d)
	other := signInSession(t, c, w, n.Name, spare)
	plain := signInSession(t, c, w, n.Name, nil)

	resp, _ := c.For(t).LogoutAll(t.Context(), bound.AccessToken, bound.RefreshToken, n.Name, nil)
	expectCode(t, resp, http.StatusUnauthorized, "DEVICE_PROOF_REQUIRED")
	acceptedEverywhere(t, nodes, other.AccessToken, "the other device's token after a refused logout-all")
	acceptedEverywhere(t, nodes, plain.AccessToken, "the wallet's own token after a refused logout-all")
	resp, _ = c.For(t).LogoutAll(t.Context(), bound.AccessToken, bound.RefreshToken, n.Name, proof(t, d, wallet.ProofEndSession, n.Name, d.ID()))
	expectCode(t, resp, http.StatusUnauthorized, "DEVICE_PROOF_INVALID")

	good := proof(t, d, wallet.ProofLogoutAll, n.Name, d.ID())
	loggedOut, err := c.For(t).LogoutAll(t.Context(), bound.AccessToken, bound.RefreshToken, n.Name, good)
	if err != nil {
		t.Fatalf("logout-all with the device's proof: %v", err)
	}
	refusedEverywhere(t, nodes, other.AccessToken, "the other device's token after logout-all")
	refusedEverywhere(t, nodes, plain.AccessToken, "the wallet's own token after logout-all")

	// A token minted in the revocation's own second is refused with the ones
	// before it (iat has one-second resolution), so the device signs in again
	// once the gateway's clock has left that second.
	gatewaySecondAfter(t, c, loggedOut)
	again := signInSession(t, c, w, n.Name, d)
	resp, _ = c.For(t).LogoutAll(t.Context(), again.AccessToken, again.RefreshToken, n.Name, good)
	expectCode(t, resp, http.StatusUnauthorized, "DEVICE_PROOF_INVALID")
	acceptedEverywhere(t, nodes, again.AccessToken, "the device's new session after a replayed logout-all proof")

	unbound := signInSession(t, c, w, n.Name, nil)
	if _, err := c.For(t).Logout(t.Context(), unbound.AccessToken, unbound.RefreshToken, n.Name, true); err != nil {
		t.Fatalf("a session bound to no device logs out everywhere without a proof: %v", err)
	}
	refusedEverywhere(t, nodes, again.AccessToken, "the device's session after the wallet logged out everywhere")
}
