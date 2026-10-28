//go:build e2e_fleet

package authdeviceadmission

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Orama-Network/orama/e2e/harness"
	"github.com/Orama-Network/orama/e2e/harness/gw"
	"github.com/Orama-Network/orama/e2e/harness/ns"
	"github.com/Orama-Network/orama/e2e/harness/wallet"
)

const (
	fixtureDir   = "testdata/admitfn"
	fixturePerm  = 0o644
	cleanupLimit = 2 * time.Minute
	pollEvery    = time.Second
	// revocationBudget: a revoked device's tokens are refused on every
	// gateway within ten seconds
	// (docs/whitepaper/technical-reference/vol1/13-identity.md#revoking-a-device),
	// plus the round trip.
	revocationBudget = 15 * time.Second

	// admitFn is deployed open to pending devices, closedFn is not; both run
	// the same fixture.
	admitFn  = "e2e-admit"
	closedFn = "e2e-admit-closed"

	policyFunction = "function"
	policyOptional = "optional"

	statePending = "pending"
	stateActive  = "active"

	codePending            = "DEVICE_PENDING_APPROVAL"
	codeLinkFunctionPolicy = "DEVICE_LINK_FUNCTION_POLICY"

	roleRuntime = "runtime"
	roleAdmin   = "admin"

	pathDeviceStart = "/v1/auth/device"
	pathDevices     = "/v1/auth/devices"
	pathSessions    = "/v1/auth/sessions"
	pathPubsubWS    = "/v1/pubsub/ws"
	pathPublish     = "/v1/pubsub/publish"
	pathNSDevices   = "/v1/namespace/devices"
)

// fixture is a namespace under the function device policy with both fixture
// functions deployed, an end user, and an admin (exempt from the policy).
type fixture struct {
	n     *ns.Namespace
	c     *gw.Client
	user  *wallet.EVM
	admin *gw.Session
}

func setup(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("tinygo"); err != nil {
		harness.SkipNotApplicable(t, "tinygo is not on the runner's PATH; `orama function deploy` builds functions with it")
	}
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{Via: ns.ViaOperator})
	deploy(t, n, admitFn, "open_to_pending_devices: true\n")
	deploy(t, n, closedFn, "")
	fx := &fixture{n: n, c: harness.GW(t), user: newWallet(t)}
	n.CLI.MustOK(t, "members", "add", fx.user.Address(), "--role", roleRuntime)
	admin := newWallet(t)
	n.CLI.MustOK(t, "members", "add", admin.Address(), "--role", roleAdmin)
	fx.admin = signIn(t, fx.c, admin, n.Name, nil)
	n.CLI.MustOK(t, "namespace", "session-policy", "--device-policy", policyFunction)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupLimit)
		defer cancel()
		if res, err := n.CLI.Run(ctx, "namespace", "session-policy", "--device-policy", policyOptional); err != nil || res.Exit != 0 {
			t.Errorf("cleanup: failed to restore %s's device policy: %v %s", n.Name, err, res.Stderr)
		}
	})
	return fx
}

// deploy copies the fixture to a scratch directory and deploys it as name,
// private, with extra function.yaml lines.
func deploy(t *testing.T, n *ns.Namespace, name, extra string) {
	t.Helper()
	dir := t.TempDir()
	for _, file := range []string{"function.go", "go.mod"} {
		src, err := os.ReadFile(filepath.Join(fixtureDir, file))
		if err != nil {
			t.Fatalf("fixture %s: %v", file, err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), src, fixturePerm); err != nil {
			t.Fatal(err)
		}
	}
	yaml := "name: " + name + "\npublic: false\nmemory: 64\ntimeout: 30\n" + extra
	if err := os.WriteFile(filepath.Join(dir, "function.yaml"), []byte(yaml), fixturePerm); err != nil {
		t.Fatal(err)
	}
	n.CLI.MustOK(t, "function", "deploy", dir)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupLimit)
		defer cancel()
		if res, err := n.CLI.Run(ctx, "function", "delete", name, "--force"); err != nil || res.Exit != 0 {
			t.Errorf("cleanup: failed to delete function %s: %v %s", name, err, res.Stderr)
		}
	})
}

func newWallet(t testing.TB) *wallet.EVM {
	t.Helper()
	w, err := wallet.NewEVM()
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func signIn(t testing.TB, c *gw.Client, w *wallet.EVM, namespace string, d *wallet.Device) *gw.Session {
	t.Helper()
	s, err := c.For(t).SignIn(t.Context(), w, namespace, d)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// refresh rotates s with d's proof and returns the new session.
func refresh(t testing.TB, fx *fixture, s *gw.Session, d *wallet.Device) (*gw.Session, *gw.Response) {
	t.Helper()
	p, err := d.NewProof(wallet.ProofRefresh, fx.n.Name, s.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	next, resp, err := fx.c.For(t).Refresh(t.Context(), s.RefreshToken, fx.n.Name, p)
	if err != nil {
		return nil, resp
	}
	return next, resp
}

// call invokes fn with op as bearer.
func call(t testing.TB, fx *fixture, fn, bearer string, op map[string]string) *gw.Response {
	t.Helper()
	body, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	return fx.c.MustSend(t, gw.Req{Method: http.MethodPost, Path: "/v1/functions/" + fn + "/invoke",
		Query: url.Values{"namespace": {fx.n.Name}}, Bearer: bearer,
		Header: http.Header{"Content-Type": {"application/json"}}, Body: body})
}

// caller is what the fixture was told about whoever called it.
type caller struct {
	Subject     string `json:"subject"`
	DeviceID    string `json:"device_id"`
	DeviceState string `json:"device_state"`
}

func whoCalled(t testing.TB, fx *fixture, bearer string) caller {
	t.Helper()
	var c caller
	if err := call(t, fx, admitFn, bearer, nil).Expect(t, http.StatusOK).Decode(&c); err != nil {
		t.Fatal(err)
	}
	return c
}

// decide runs approve or deny in the fixture as bearer and reports whether
// the gateway carried it out.
func decide(t testing.TB, fx *fixture, bearer, op, subject, device string) bool {
	t.Helper()
	var out struct {
		Done bool `json:"done"`
	}
	resp := call(t, fx, admitFn, bearer, map[string]string{"op": op, "subject": subject, "device": device})
	if err := resp.Expect(t, http.StatusOK).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Done
}

// refusedPending fails unless resp is the pending-device refusal: 403 with
// DEVICE_PENDING_APPROVAL, at the top level (the gateway's) or in the RPC
// envelope (the invoker's).
func refusedPending(t testing.TB, resp *gw.Response, what string) {
	t.Helper()
	var body struct {
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(resp.Body, &body)
	if resp.Status != http.StatusForbidden || (body.Code != codePending && body.Error.Code != codePending) {
		t.Errorf("%s: want 403 %s, got %d: %.400s", what, codePending, resp.Status, resp.Body)
	}
}
