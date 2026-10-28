//go:build e2e_fleet

package authdevices

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Orama-Network/orama/e2e/harness"
	"github.com/Orama-Network/orama/e2e/harness/gw"
	"github.com/Orama-Network/orama/e2e/harness/ns"
	"github.com/Orama-Network/orama/e2e/harness/wallet"
)

// maxRevocationEvidence is the bound on what a revoker attaches
// (docs/whitepaper/technical-reference/vol1/13-identity.md#revoking-a-device).
const maxRevocationEvidence = 16 << 10

// revocation is the `revocation` of a DEVICE_REVOKED answer.
type revocation struct {
	DeviceID  string  `json:"device_id"`
	RevokedAt *string `json:"revoked_at"`
	RevokedBy string  `json:"revoked_by"`
	Proof     *struct {
		Namespace string `json:"namespace"`
		DeviceID  string `json:"device_id"`
		PublicKey struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			X   string `json:"x"`
		} `json:"public_key"`
		IssuedAt  int64  `json:"iat"`
		ID        string `json:"id"`
		Signature string `json:"sig"`
	} `json:"proof"`
	Evidence *string `json:"evidence"`
}

// revokedAnswer is the revocation a refused sign-in of d carries.
func revokedAnswer(t *testing.T, c *gw.Client, w *wallet.EVM, namespace string, d *wallet.Device) revocation {
	t.Helper()
	resp := rawDeviceSignIn(t, c, w, namespace, d.ID(), d.PublicJWK(), signWith(t, d))
	expectCode(t, resp, http.StatusForbidden, "DEVICE_REVOKED")
	var body struct {
		Revocation *revocation `json:"revocation"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil || body.Revocation == nil {
		t.Fatalf("DEVICE_REVOKED without its revocation: %v %.400s", err, resp.Body)
	}
	if !strings.Contains(string(resp.Body), `"proof":`) || !strings.Contains(string(resp.Body), `"evidence":`) {
		t.Errorf("the revocation leaves proof or evidence out instead of saying null: %.400s", resp.Body)
	}
	return *body.Revocation
}

// TestDeviceRevoked_refusalCarriesTheRevocation: a device revoked by another
// device is refused with that device's proof, which verifies with the
// revoker's key over the revoke statement naming the revoked device and the
// namespace (and for no other device), and with the application's evidence
// verbatim; an operator's revocation says so and carries no proof; evidence
// past the bound revokes nothing (docs/whitepaper/technical-reference/vol1/13-identity.md#revoking-a-device).
func TestDeviceRevoked_refusalCarriesTheRevocation(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	w := member(t, n, roleRuntime)
	lost, kept, operated := gw.NewDevice(t, wallet.AlgES256), gw.NewDevice(t, wallet.AlgEd25519), gw.NewDevice(t, wallet.AlgEd25519)
	signInSession(t, c, w, n.Name, lost)
	keptS := signInSession(t, c, w, n.Name, kept)
	signInSession(t, c, w, n.Name, operated)

	tooBig := strings.Repeat("e", maxRevocationEvidence+1)
	if r := send(t, c, http.MethodDelete, gw.PathDevices+"/"+lost.ID(), keptS.AccessToken,
		map[string]any{"device_proof": proof(t, kept, wallet.ProofRevoke, n.Name, lost.ID()), "evidence": tooBig}); r.Status != http.StatusBadRequest {
		t.Fatalf("evidence past the bound: want 400, got %d %.200s", r.Status, r.Body)
	}
	signInSession(t, c, w, n.Name, lost) // still enrolled and active

	evidence := "eyJyb3N0ZXJfdmVyc2lvbiI6OH0"
	send(t, c, http.MethodDelete, gw.PathDevices+"/"+lost.ID(), keptS.AccessToken,
		map[string]any{"device_proof": proof(t, kept, wallet.ProofRevoke, n.Name, lost.ID()), "evidence": evidence}).Expect(t, http.StatusOK)

	rec := revokedAnswer(t, c, w, n.Name, lost)
	if rec.DeviceID != lost.ID() || rec.RevokedBy != "device" || rec.RevokedAt == nil || rec.Evidence == nil || *rec.Evidence != evidence {
		t.Fatalf("revocation %+v", rec)
	}
	p := rec.Proof
	if p == nil || p.DeviceID != kept.ID() || p.Namespace != n.Name {
		t.Fatalf("proof %+v, want device %s in %s", p, kept.ID(), n.Name)
	}
	if got := wallet.Thumbprint(`{"crv":"Ed25519","kty":"OKP","x":"` + p.PublicKey.X + `"}`); got != p.DeviceID {
		t.Fatalf("the proof's key is device %s, not %s", got, p.DeviceID)
	}
	pub, errKey := base64.RawURLEncoding.DecodeString(p.PublicKey.X)
	sig, errSig := base64.RawURLEncoding.DecodeString(p.Signature)
	if errKey != nil || errSig != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("the proof's key or signature is not base64url: %v %v", errKey, errSig)
	}
	if !ed25519.Verify(pub, wallet.ProofMessage(wallet.ProofRevoke, p.Namespace, lost.ID(), p.IssuedAt, p.ID), sig) {
		t.Error("the revoker's proof does not verify for the revoked device")
	}
	if ed25519.Verify(pub, wallet.ProofMessage(wallet.ProofRevoke, p.Namespace, operated.ID(), p.IssuedAt, p.ID), sig) {
		t.Error("the revoker's proof verifies for another device")
	}

	send(t, c, http.MethodDelete, pathNSDevices+"/"+operated.ID(), n.Owner.Token(),
		map[string]any{"evidence": "roster-v9"}).Expect(t, http.StatusOK)
	op := revokedAnswer(t, c, w, n.Name, operated)
	if op.RevokedBy != "operator" || op.Proof != nil || op.Evidence == nil || *op.Evidence != "roster-v9" {
		t.Errorf("an operator's revocation reads %+v, want operator with no proof and its evidence", op)
	}
}

// revocationIn is the revocation of resp, which must be 403 DEVICE_REVOKED.
func revocationIn(t *testing.T, what string, resp *gw.Response) revocation {
	t.Helper()
	expectCode(t, resp, http.StatusForbidden, codeDeviceRevoked)
	var body struct {
		Revocation *revocation `json:"revocation"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil || body.Revocation == nil {
		t.Fatalf("%s: DEVICE_REVOKED without its revocation: %v %.400s", what, err, resp.Body)
	}
	return *body.Revocation
}

// TestDeviceRevoked_everyRefusalOfItsTokenCarriesTheRevocation: once a device
// is revoked, its open socket closes with 4410, and its access token is
// answered 403 DEVICE_REVOKED with the revocation on every public and
// namespace gateway, on a request, on a function call (an open route, which
// would otherwise serve it as anonymous), on a WebSocket upgrade, and on its
// refresh token's refresh. A revoked session's token stays AUTH_REVOKED
// (docs/whitepaper/technical-reference/vol1/13-identity.md#revoking-a-device).
func TestDeviceRevoked_everyRefusalOfItsTokenCarriesTheRevocation(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	w := member(t, n, roleRuntime)
	lost, kept := gw.NewDevice(t, wallet.AlgEd25519), gw.NewDevice(t, wallet.AlgEd25519)
	lostS := signInSession(t, c, w, n.Name, lost)
	keptS := signInSession(t, c, w, n.Name, kept)
	open := mustSubscribe(t, n.Client, lostS.AccessToken, nil)

	const evidence = "roster-v10"
	send(t, c, http.MethodDelete, gw.PathDevices+"/"+lost.ID(), keptS.AccessToken,
		map[string]any{"device_proof": proof(t, kept, wallet.ProofRevoke, n.Name, lost.ID()), "evidence": evidence}).Expect(t, http.StatusOK)

	if code := open.closeCode(t, sweepBudget); code != closeDeviceRevoked {
		t.Errorf("the revoked device's open socket closed with %d, want %d within %s", code, closeDeviceRevoked, sweepBudget)
	}
	gateways := append(perNode(t, f, c), perNode(t, f, n.Client)...)
	deviceRevokedEverywhere(t, gateways, lostS.AccessToken, "the revoked device's access token")

	answers := map[string]*gw.Response{
		"whoami":          c.MustSend(t, gw.Req{Path: gw.PathWhoami, Bearer: lostS.AccessToken}),
		"function call":   n.Client.MustSend(t, gw.Req{Method: http.MethodPost, Path: "/v1/invoke/" + n.Name + "/e2e-none", Bearer: lostS.AccessToken}),
		"refresh":         refreshWith(t, c, lostS.RefreshToken, n.Name, proof(t, lost, wallet.ProofRefresh, n.Name, lostS.RefreshToken)),
		"socket upgrade":  wsRefusal(t, n.Client, lostS.AccessToken),
		"proxied upgrade": wsRefusal(t, c, lostS.AccessToken),
	}
	for what, resp := range answers {
		rec := revocationIn(t, what, resp)
		if rec.DeviceID != lost.ID() || rec.RevokedBy != "device" || rec.Proof == nil || rec.Proof.DeviceID != kept.ID() ||
			rec.Evidence == nil || *rec.Evidence != evidence {
			t.Errorf("%s: revocation %+v, want the kept device's, with its evidence", what, rec)
		}
	}

	// Starting a device link proves nothing about holding the key, so its
	// refusal names the revocation but does not hand its record to whoever
	// presented the public key.
	link := postJSON(t, c, pathDeviceStart, "", map[string]any{"namespace": n.Name, "device_key": lost.PublicJWK()})
	if body := expectCode(t, link, http.StatusForbidden, "DEVICE_REVOKED"); body["revocation"] != nil {
		t.Errorf("a device link started with the revoked key was handed its revocation: %.400s", link.Body)
	}

	if _, err := c.For(t).Logout(t.Context(), keptS.AccessToken, keptS.RefreshToken, n.Name, false); err != nil {
		t.Fatal(err)
	}
	refusedEverywhereWith(t, perNode(t, f, c), keptS.AccessToken, "a logged-out session's token", http.StatusUnauthorized, "AUTH_REVOKED")
}

// wsRefusal is the answer to a pub/sub upgrade with bearer, which must be
// refused before the upgrade.
func wsRefusal(t *testing.T, c *gw.Client, bearer string) *gw.Response {
	t.Helper()
	conn, resp, err := subscribe(t, c, bearer, nil)
	if conn != nil || resp == nil {
		t.Fatalf("the upgrade was not refused: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the refused upgrade's body: %v", err)
	}
	return &gw.Response{Status: resp.StatusCode, Header: resp.Header, Body: body}
}
