//go:build e2e_fleet

package authdevices

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Orama-Network/orama/e2e/harness"
	"github.com/Orama-Network/orama/e2e/harness/gw"
	"github.com/Orama-Network/orama/e2e/harness/ns"
	"github.com/Orama-Network/orama/e2e/harness/wallet"
)

type pending struct {
	Status     string `json:"status"`
	Code       string `json:"code"`
	DeviceID   string `json:"device_id"`
	DeviceCode string `json:"device_code"`
	UserCode   string `json:"user_code"`
	Interval   int    `json:"interval"`
}

// approveLink is POST /v1/auth/devices/approve from an active device's session.
func approveLink(t testing.TB, c *gw.Client, bearer string, approver *wallet.Device, namespace, userCode string) *gw.Response {
	t.Helper()
	// A device proof carries its issue time and the gateway accepts it for a
	// short window: the pacer's wait for this request's token comes first, and
	// the proof is built only after it, so it is fresh when it is sent.
	paid := prepay(t, c)
	body := map[string]any{"user_code": userCode}
	if approver != nil {
		body["device_proof"] = proof(t, approver, wallet.ProofApprove, namespace, userCode)
	}
	return postJSON(t, paid, pathLinkApprove, bearer, body)
}

// claim collects a linked session with the new device's proof over the code.
func claim(t testing.TB, c *gw.Client, dev *wallet.Device, namespace, deviceCode string) *gw.Response {
	t.Helper()
	paid := prepay(t, c)
	var p *wallet.Proof
	if dev != nil {
		p = proof(t, dev, wallet.ProofClaim, namespace, deviceCode)
	}
	return poll(t, paid, deviceCode, p)
}

// TestSessionPolicy_approvalPendsANewDevice: under `approval` an account's
// first device is active; its next device's sign-in answers 202 with codes,
// holds no session until an active device approves it, and a wallet
// signature alone cannot approve it (docs/whitepaper/technical-reference/vol1/13-identity.md#linking-a-device-from-a-device).
func TestSessionPolicy_approvalPendsANewDevice(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	setPolicy(t, n, policyApproval)
	user := member(t, n, roleRuntime)
	first, second := gw.NewDevice(t, wallet.AlgEd25519), gw.NewDevice(t, wallet.AlgES256)
	firstS := signInSession(t, c, user, n.Name, first)
	resp := postJSON(t, c, gw.PathVerify, "", signInReq(t, c, user, n.Name, second))
	var p pending
	if err := resp.Expect(t, http.StatusAccepted).Decode(&p); err != nil {
		t.Fatal(err)
	}
	protect(t, c, p.DeviceCode)
	if p.Status != "pending_approval" || p.Code != "DEVICE_PENDING" || p.DeviceID != second.ID() || p.UserCode == "" || p.DeviceCode == "" {
		t.Fatalf("pending sign-in answered %+v", p)
	}
	if strings.Contains(string(resp.Body), "access_token") {
		t.Fatal("a pending device was handed a token")
	}
	expectOAuthError(t, claim(t, c, second, n.Name, p.DeviceCode), "authorization_pending")
	if r := approveWithWallet(t, c, user, n.Name, p.UserCode, false); r.Status < 400 || r.Status >= 500 {
		t.Errorf("a wallet signature approved a device link: %d %s", r.Status, r.Body)
	}
	plain := signInSession(t, c, n.Owner.Wallet, n.Name, nil)
	expectCode(t, approveLink(t, c, plain.AccessToken, nil, n.Name, p.UserCode), http.StatusUnauthorized, "DEVICE_PROOF_REQUIRED")
	approveLink(t, c, firstS.AccessToken, first, n.Name, p.UserCode).Expect(t, http.StatusOK)
	expectCode(t, claim(t, c, nil, n.Name, p.DeviceCode), http.StatusUnauthorized, "DEVICE_PROOF_REQUIRED")
	expectCode(t, claim(t, c, first, n.Name, p.DeviceCode), http.StatusUnauthorized, "DEVICE_PROOF_INVALID")
	var s gw.Session
	if err := claim(t, c, second, n.Name, p.DeviceCode).Expect(t, http.StatusOK).Decode(&s); err != nil {
		t.Fatal(err)
	}
	protect(t, c, s.AccessToken, s.RefreshToken)
	if s.DeviceID != second.ID() || !strings.EqualFold(s.Subject, user.Address()) {
		t.Fatalf("the claimed session is %s on device %s", s.Subject, s.DeviceID)
	}
	expectOAuthError(t, claim(t, c, second, n.Name, p.DeviceCode), "invalid_grant")
}

// TestDeviceLink_seedlessTakesTheApproversAccount: a new device with no
// wallet starts a link, one of the account's devices approves it, and the new
// device collects a session on the approver's account; once the approver is
// revoked, a link it approved can no longer be collected.
func TestDeviceLink_seedlessTakesTheApproversAccount(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	user := member(t, n, roleRuntime)
	approver := gw.NewDevice(t, wallet.AlgEd25519)
	approverS := signInSession(t, c, user, n.Name, approver)
	phone, tablet := gw.NewDevice(t, wallet.AlgES256), gw.NewDevice(t, wallet.AlgEd25519)
	link := startLogin(t, c, map[string]any{"namespace": n.Name, "device_key": phone.PublicJWK()})
	if link.DeviceID != phone.ID() {
		t.Fatalf("the link names device %q, want %s", link.DeviceID, phone.ID())
	}
	approveLink(t, c, approverS.AccessToken, approver, n.Name, link.UserCode).Expect(t, http.StatusOK)
	var s gw.Session
	if err := claim(t, c, phone, n.Name, link.DeviceCode).Expect(t, http.StatusOK).Decode(&s); err != nil {
		t.Fatal(err)
	}
	protect(t, c, s.AccessToken, s.RefreshToken)
	if !strings.EqualFold(s.Subject, user.Address()) || s.DeviceID != phone.ID() {
		t.Fatalf("the linked device signed in as %s on %s, want %s on %s", s.Subject, s.DeviceID, user.Address(), phone.ID())
	}
	late := startLogin(t, c, map[string]any{"namespace": n.Name, "device_key": tablet.PublicJWK()})
	approveLink(t, c, approverS.AccessToken, approver, n.Name, late.UserCode).Expect(t, http.StatusOK)
	if _, err := c.For(t).RevokeDevice(t.Context(), s.AccessToken, approver.ID(), proof(t, phone, wallet.ProofRevoke, n.Name, approver.ID())); err != nil {
		t.Fatalf("revoking the approver: %v", err)
	}
	// The approval is void (access_denied); the collecting device is not the
	// revoked one, and must not be told DEVICE_REVOKED.
	if r := claim(t, c, tablet, n.Name, late.DeviceCode); r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), `"access_denied"`) {
		t.Fatalf("collecting a link approved by a since-revoked device: %d %.300s, want 400 access_denied", r.Status, r.Body)
	}
}

// TestNamespaceDevices_operatorRecovery: a namespace operator (members write)
// lists and revokes an account's devices; with none left the account's next
// device enrols as its first again, even under approval (docs/whitepaper/technical-reference/vol1/13-identity.md,
// "When the user cannot help themselves").
func TestNamespaceDevices_operatorRecovery(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	setPolicy(t, n, policyApproval)
	user := member(t, n, roleRuntime)
	lost := gw.NewDevice(t, wallet.AlgEd25519)
	signInSession(t, c, user, n.Name, lost)
	owner := n.Owner.Token()
	var listed struct {
		Subject string          `json:"subject"`
		Devices []gw.DeviceView `json:"devices"`
	}
	q := map[string][]string{"subject": {user.Address()}}
	if err := c.MustSend(t, gw.Req{Path: pathNSDevices, Query: q, Bearer: owner}).Expect(t, http.StatusOK).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Devices) != 1 || listed.Devices[0].ID != lost.ID() {
		t.Fatalf("operator's list for %s: %+v", user.Address(), listed.Devices)
	}
	if r := c.MustSend(t, gw.Req{Path: pathNSDevices, Bearer: owner}); r.Status != http.StatusBadRequest {
		t.Errorf("no ?subject=: want 400, got %d", r.Status)
	}
	runtimeTok := signInSession(t, c, member(t, n, roleRuntime), n.Name, gw.NewDevice(t, wallet.AlgEd25519)).AccessToken
	if r := c.MustSend(t, gw.Req{Path: pathNSDevices, Query: q, Bearer: runtimeTok}); r.Status != http.StatusForbidden {
		t.Errorf("a runtime member listed another account's devices: %d", r.Status)
	}
	unknown := send(t, c, http.MethodDelete, pathNSDevices+"/"+gw.NewDevice(t, wallet.AlgEd25519).ID(), owner, nil)
	expectCode(t, unknown, http.StatusNotFound, "DEVICE_NOT_FOUND")
	send(t, c, http.MethodDelete, pathNSDevices+"/"+lost.ID(), owner, nil).Expect(t, http.StatusOK)
	s := signInSession(t, c, user, n.Name, gw.NewDevice(t, wallet.AlgES256))
	if s.AccessToken == "" {
		t.Fatal("after recovery the account's new device did not enrol as its first")
	}
}
