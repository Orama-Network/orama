//go:build e2e_fleet

package authdevices

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/e2e/harness/eventually"
	"github.com/Orama-Network/orama/e2e/harness/fleet"
	"github.com/Orama-Network/orama/e2e/harness/gw"
	"github.com/Orama-Network/orama/e2e/harness/ns"
	"github.com/Orama-Network/orama/e2e/harness/wallet"
)

const (
	// revocationStaleness: devices, sessions and tokens stop "within ten
	// seconds on every gateway" (docs/whitepaper/technical-reference/vol1/13-identity.md#revoking-a-device).
	revocationStaleness = 10 * time.Second
	// stalenessSlack covers the round trip and a close frame on top.
	stalenessSlack = 5 * time.Second
	pollEvery      = time.Second
	// codeDeviceRevoked refuses every token of a revoked device, with its
	// revocation (docs/whitepaper/technical-reference/vol1/13-identity.md#revoking-a-device).
	codeDeviceRevoked = "DEVICE_REVOKED"
	// Paths not in the harness's constant set (docs/whitepaper/technical-reference/appendices/i-api-surface.md).
	pathDeviceStart   = "/v1/auth/device"
	pathDeviceApprove = "/v1/auth/device/approve"
	pathDeviceToken   = "/v1/auth/device/token"
	pathLinkApprove   = "/v1/auth/devices/approve"
	pathMembers       = "/v1/namespace/members"
	pathPolicy        = "/v1/namespace/session-policy"
	pathNSDevices     = "/v1/namespace/devices"
	// Roles (docs/whitepaper/technical-reference/vol1/14-authorization.md#roles-and-grants).
	roleRuntime   = "runtime"
	roleDeveloper = "developer"
	roleAdmin     = "admin"
)

// expectCode fails unless resp has status and code, and a 401/403 carries
// {error, code, hint} (docs/whitepaper/technical-reference/vol1/14-authorization.md#refusals-and-the-error-code-table).
func expectCode(t testing.TB, resp *gw.Response, status int, code string) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(resp.Body, &body); err != nil || resp.Status != status || body["code"] != code {
		t.Fatalf("want HTTP %d %s, got %d: %.400s", status, code, resp.Status, resp.Body)
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		for _, k := range []string{"error", "hint"} {
			if s, _ := body[k].(string); strings.TrimSpace(s) == "" {
				t.Errorf("HTTP %d %s lacks %q", status, code, k)
			}
		}
	}
	return body
}

func postJSON(t testing.TB, c *gw.Client, path, bearer string, v any) *gw.Response {
	t.Helper()
	return send(t, c, http.MethodPost, path, bearer, v)
}

func send(t testing.TB, c *gw.Client, method, path, bearer string, v any) *gw.Response {
	t.Helper()
	r := gw.Req{Method: method, Path: path, Bearer: bearer}
	if v != nil {
		body, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		r.Body, r.Header = body, http.Header{"Content-Type": {"application/json"}}
	}
	return c.MustSend(t, r)
}

func newWallet(t testing.TB) *wallet.EVM {
	t.Helper()
	w, err := wallet.NewEVM()
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// signInReq builds a verify request for w (and dev, when set) in namespace.
func signInReq(t testing.TB, c *gw.Client, w *wallet.EVM, namespace string, dev *wallet.Device) gw.VerifyRequest {
	t.Helper()
	creq := gw.ChallengeRequest{Wallet: w.Address(), Namespace: namespace}
	if dev != nil {
		creq.DeviceID = dev.ID()
	}
	ch, _, err := c.For(t).Challenge(t.Context(), creq)
	if err != nil {
		t.Fatalf("challenge for %s in %q: %v", w.Address(), namespace, err)
	}
	sig, err := w.Sign(ch.Message)
	if err != nil {
		t.Fatal(err)
	}
	req := gw.VerifyRequest{Message: ch.Message, Signature: sig}
	if dev != nil {
		ds, err := dev.Sign([]byte(ch.Message))
		if err != nil {
			t.Fatal(err)
		}
		req.DeviceKey, req.DeviceSignature, req.DeviceLabel = dev.PublicJWK(), ds, "e2e-"+dev.Alg()
	}
	return req
}

// signInSession signs in and requires a 200 session.
func signInSession(t testing.TB, c *gw.Client, w *wallet.EVM, namespace string, dev *wallet.Device) *gw.Session {
	t.Helper()
	s, err := c.For(t).SignIn(t.Context(), w, namespace, dev)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// addMember grants w role in n as n's owner (docs/whitepaper/technical-reference/vol1/14-authorization.md#roles-and-grants).
func addMember(t testing.TB, n *ns.Namespace, w *wallet.EVM, role string) {
	t.Helper()
	resp := postJSON(t, n.Owner.Client, pathMembers, n.Owner.Token(), map[string]string{"wallet": w.Address(), "role": role})
	if resp.Status != http.StatusCreated {
		t.Fatalf("adding %s as %s to %s: %d %s", w.Address(), role, n.Name, resp.Status, resp.Body)
	}
}

// member is a fresh wallet holding role in n.
func member(t testing.TB, n *ns.Namespace, role string) *wallet.EVM {
	t.Helper()
	w := newWallet(t)
	addMember(t, n, w, role)
	return w
}

func proof(t testing.TB, d *wallet.Device, action, namespace, binding string) *wallet.Proof {
	t.Helper()
	p, err := d.NewProof(action, namespace, binding)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// prepay takes the pacer token of the one credential request about to be
// sent and returns a client that sends it without waiting again.
func prepay(t testing.TB, c *gw.Client) *gw.Client {
	t.Helper()
	paid, err := c.Prepay(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	return paid
}

// whoamiStatus is GET /v1/auth/whoami's status for bearer on c.
func whoamiStatus(t testing.TB, c *gw.Client, bearer string) (int, string) {
	t.Helper()
	resp := c.MustSend(t, gw.Req{Path: gw.PathWhoami, Bearer: bearer})
	return resp.Status, resp.ErrorCode()
}

// nodeClient is a client whose every connection goes to one node.
type nodeClient struct {
	Node   fleet.Node
	Client *gw.Client
}

// perNode pins c (same URL, same trust, same pacing) to each core node's
// public address (gw.Client.PinTo).
func perNode(t testing.TB, f *fleet.Fleet, c *gw.Client) []nodeClient {
	t.Helper()
	out := make([]nodeClient, 0, len(f.State.Nodes))
	for _, n := range f.State.Nodes {
		out = append(out, nodeClient{Node: n, Client: c.PinTo(n.PublicIP)})
	}
	return out
}

// refusedEverywhere waits until every client refuses bearer with a 401,
// within the revocation staleness.
func refusedEverywhere(t testing.TB, nodes []nodeClient, bearer, what string) {
	t.Helper()
	refusedEverywhereWith(t, nodes, bearer, what, http.StatusUnauthorized, "")
}

// deviceRevokedEverywhere waits until every client refuses bearer, a token of
// a revoked device, with 403 DEVICE_REVOKED, within the revocation staleness
// (docs/whitepaper/technical-reference/vol1/13-identity.md#revoking-a-device).
func deviceRevokedEverywhere(t testing.TB, nodes []nodeClient, bearer, what string) {
	t.Helper()
	refusedEverywhereWith(t, nodes, bearer, what, http.StatusForbidden, codeDeviceRevoked)
}

// refusedEverywhereWith waits until every client refuses bearer with status
// and, unless it is "", code.
func refusedEverywhereWith(t testing.TB, nodes []nodeClient, bearer, what string, status int, code string) {
	t.Helper()
	start := time.Now()
	for _, nc := range nodes {
		eventually.Require(t, pollEvery, revocationStaleness+stalenessSlack, nc.Node.Name+" to refuse "+what, func() (bool, error) {
			st, got := whoamiStatus(t, nc.Client, bearer)
			if st == status && (code == "" || got == code) {
				return true, nil
			}
			return false, fmt.Errorf("HTTP %d %s", st, got)
		})
	}
	if took := time.Since(start); took > revocationStaleness+stalenessSlack {
		t.Errorf("%s took %s to be refused everywhere, the promise is %s", what, took, revocationStaleness)
	}
}

// acceptedEverywhere requires every client to accept bearer now.
func acceptedEverywhere(t testing.TB, nodes []nodeClient, bearer, what string) {
	t.Helper()
	for _, nc := range nodes {
		if st, code := whoamiStatus(t, nc.Client, bearer); st != http.StatusOK {
			t.Errorf("%s refused %s: %d %s", nc.Node.Name, what, st, code)
		}
	}
}

// protect registers credentials minted outside the typed gw helpers with the
// evidence redactor, so they never reach the evidence file in clear.
func protect(t testing.TB, c *gw.Client, values ...string) {
	t.Helper()
	red := c.Recorder().Redactor()
	if red == nil {
		return
	}
	if err := red.Add(values...); err != nil {
		t.Errorf("failed to register minted credentials for redaction: %v", err)
	}
}
