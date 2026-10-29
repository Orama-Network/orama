//go:build e2e_fleet

package authdevices

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/Orama-Network/orama/e2e/harness"
	"github.com/Orama-Network/orama/e2e/harness/gw"
	"github.com/Orama-Network/orama/e2e/harness/ns"
	"github.com/Orama-Network/orama/e2e/harness/wallet"
)

// TestDeviceRevoke_reachesEveryGateway: revoking a device ends its access
// tokens on every gateway — each node's public gateway and each node's
// namespace gateway — within ten seconds, ends its refresh token, and leaves
// the account's other device signed in everywhere (docs/whitepaper/technical-reference/vol1/13-identity.md#revoking-a-device).
func TestDeviceRevoke_reachesEveryGateway(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	w := member(t, n, roleRuntime)
	lost, kept := gw.NewDevice(t, wallet.AlgES256), gw.NewDevice(t, wallet.AlgEd25519)
	lostS := signInSession(t, c, w, n.Name, lost)
	keptS := signInSession(t, c, w, n.Name, kept)
	gateways := append(perNode(t, f, c), perNode(t, f, n.Client)...)
	acceptedEverywhere(t, gateways, lostS.AccessToken, "the device's token before revocation")
	if _, err := c.For(t).RevokeDevice(t.Context(), keptS.AccessToken, lost.ID(), proof(t, kept, wallet.ProofRevoke, n.Name, lost.ID())); err != nil {
		t.Fatalf("revoking a lost device from the kept one: %v", err)
	}
	deviceRevokedEverywhere(t, gateways, lostS.AccessToken, "the revoked device's access token")
	acceptedEverywhere(t, gateways, keptS.AccessToken, "the other device's token")
	resp := refreshWith(t, c, lostS.RefreshToken, n.Name, proof(t, lost, wallet.ProofRefresh, n.Name, lostS.RefreshToken))
	expectCode(t, resp, http.StatusForbidden, codeDeviceRevoked)
	listed, _, err := c.For(t).Devices(t.Context(), keptS.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, d := range listed {
		states[d.ID] = d.RevokedAt
	}
	if states[lost.ID()] == "" || states[kept.ID()] != "" {
		t.Fatalf("devices list: revoked device revoked_at %q, kept device revoked_at %q", states[lost.ID()], states[kept.ID()])
	}
}

// TestDeviceRevoke_unknownAndForeignDevices: an id that is not a thumbprint
// is not a route; a well-formed id of no device, or another account's device,
// is DEVICE_NOT_FOUND, and the other account is untouched.
func TestDeviceRevoke_unknownAndForeignDevices(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	d := gw.NewDevice(t, wallet.AlgEd25519)
	victim := signInSession(t, c, member(t, n, roleRuntime), n.Name, d)
	attacker := signInSession(t, c, member(t, n, roleRuntime), n.Name, nil)
	for _, id := range []string{gw.NewDevice(t, wallet.AlgEd25519).ID(), d.ID()} {
		resp, _ := c.For(t).RevokeDevice(t.Context(), attacker.AccessToken, id, nil)
		expectCode(t, resp, http.StatusNotFound, "DEVICE_NOT_FOUND")
	}
	for _, id := range []string{"not-a-device", "..%2f..%2fsessions"} {
		resp := c.MustSend(t, gw.Req{Method: http.MethodDelete, Path: gw.PathDevices + "/" + id, Bearer: attacker.AccessToken})
		if resp.Status != http.StatusMethodNotAllowed && resp.Status != http.StatusNotFound {
			t.Errorf("DELETE devices/%s: want 405/404, got %d", id, resp.Status)
		}
	}
	if st, _ := whoamiStatus(t, c, victim.AccessToken); st != http.StatusOK {
		t.Fatalf("another account's revoke attempts reached the victim: %d", st)
	}
	anon := c.MustSend(t, gw.Req{Path: gw.PathDevices})
	expectCode(t, anon, http.StatusUnauthorized, "AUTH_MISSING")
}

// TestSessions_listAndEndOne: the list shows each session with its device and
// never the refresh token; ending one refuses its access tokens everywhere
// within ten seconds and its refresh token at once, and leaves the others
// (docs/whitepaper/technical-reference/vol1/13-identity.md#machines-with-no-wallet).
func TestSessions_listAndEndOne(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	w := member(t, n, roleRuntime)
	d := gw.NewDevice(t, wallet.AlgEd25519)
	bound := signInSession(t, c, w, n.Name, d)
	plain := signInSession(t, c, w, n.Name, nil)
	resp := c.MustSend(t, gw.Req{Path: gw.PathSessions, Bearer: plain.AccessToken}).Expect(t, http.StatusOK)
	for _, secret := range []string{bound.RefreshToken, plain.RefreshToken} {
		if strings.Contains(string(resp.Body), secret) {
			t.Fatal("the session list returned a refresh token")
		}
	}
	sessions, _, err := c.For(t).Sessions(t.Context(), plain.AccessToken)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("sessions: %v %+v", err, sessions)
	}
	var boundID int64
	for _, s := range sessions {
		if s.DeviceID == d.ID() {
			boundID = s.ID
		}
	}
	if boundID == 0 {
		t.Fatalf("no listed session carries device %s: %+v", d.ID(), sessions)
	}
	if _, err := c.For(t).EndSession(t.Context(), plain.AccessToken, boundID, nil); err != nil {
		t.Fatalf("ending the device session from the plain one: %v", err)
	}
	refusedEverywhere(t, perNode(t, f, c), bound.AccessToken, "the ended session's access token")
	if st, _ := whoamiStatus(t, c, plain.AccessToken); st != http.StatusOK {
		t.Errorf("ending one session ended the other: %d", st)
	}
	resp = refreshWith(t, c, bound.RefreshToken, n.Name, proof(t, d, wallet.ProofRefresh, n.Name, bound.RefreshToken))
	if resp.Status != http.StatusUnauthorized {
		t.Errorf("the ended session renewed: %d %s", resp.Status, resp.Body)
	}
}

// TestSessions_endRefusals: malformed ids are 400, someone else's session and
// an ended one are 404, an API key is not a signed-in wallet (403).
func TestSessions_endRefusals(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	mine := signInSession(t, c, member(t, n, roleRuntime), n.Name, nil)
	theirs := signInSession(t, c, member(t, n, roleRuntime), n.Name, nil)
	their, _, err := c.For(t).Sessions(t.Context(), theirs.AccessToken)
	if err != nil || len(their) != 1 {
		t.Fatalf("sessions: %v %+v", err, their)
	}
	for _, id := range []string{"abc", "0", "-1", "1.5"} {
		resp := c.MustSend(t, gw.Req{Method: http.MethodDelete, Path: gw.PathSessions + "/" + id, Bearer: mine.AccessToken})
		if resp.Status != http.StatusBadRequest {
			t.Errorf("DELETE sessions/%s: want 400, got %d", id, resp.Status)
		}
	}
	resp := c.MustSend(t, gw.Req{Method: http.MethodDelete, Path: gw.PathSessions + "/" + strconv.FormatInt(their[0].ID, 10), Bearer: mine.AccessToken})
	if resp.Status != http.StatusNotFound {
		t.Errorf("ending another wallet's session: want 404, got %d %s", resp.Status, resp.Body)
	}
	if st, _ := whoamiStatus(t, c, theirs.AccessToken); st != http.StatusOK {
		t.Fatalf("another wallet's session was ended: %d", st)
	}
	if wrong := c.MustSend(t, gw.Req{Method: http.MethodPost, Path: gw.PathSessions, Bearer: mine.AccessToken}); wrong.Status != http.StatusMethodNotAllowed {
		t.Errorf("POST sessions: want 405, got %d", wrong.Status)
	}
	key := n.Owner.Session.APIKey
	if key == "" {
		t.Fatal("the owner's namespace sign-in handed out no API key to test with")
	}
	if byKey := c.MustSend(t, gw.Req{Path: gw.PathSessions, Bearer: key}); byKey.Status != http.StatusForbidden {
		t.Errorf("sessions listed for an API key: want 403, got %d", byKey.Status)
	}
}
