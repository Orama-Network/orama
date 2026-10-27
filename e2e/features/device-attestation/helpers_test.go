//go:build e2e_fleet

package deviceattestation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/e2e/harness"
	"github.com/Orama-Network/orama/e2e/harness/gw"
	"github.com/Orama-Network/orama/e2e/harness/ns"
	"github.com/Orama-Network/orama/e2e/harness/oramacli"
	"github.com/Orama-Network/orama/e2e/harness/wallet"
)

const (
	fixtureDir   = "testdata/attfn"
	fixtureName  = "e2e-attest"
	fixturePerm  = 0o644
	cleanupLimit = 2 * time.Minute
	pollEvery    = time.Second
	// policyBudget: a changed policy reaches every gateway within ten seconds
	// (docs/whitepaper/technical-reference/vol1/14-authorization.md#consistency-and-caching),
	// plus the round trip.
	policyBudget = 15 * time.Second
	// countBudget: the cluster gateways collect their report every 10 s and
	// the snapshot is cached (website/src/docs/operator/monitoring.mdx
	// "Self collection"); a minute and a half covers both and a slow node.
	countBudget = 90 * time.Second
	// monitorBurst is how many requests of each kind the monitor test makes.
	monitorBurst = 3

	modeOff     = "off"
	modeMonitor = "monitor"
	modeEnforce = "enforce"

	testPackage = "network.orama.e2e"
	testDigest  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	codeRequired = "ATTESTATION_REQUIRED"

	// The verdicts get_caller_attestation_verdict tells a function
	// (website/src/docs/developer/device-attestation.mdx#in-a-function).
	verdictNotConfigured = "not_configured"
	verdictRequired      = "attestation_required"
	// invalidMessage is all a refused proof is told
	// (core/pkg/gateway/handlers/attest/errors.go).
	invalidMessage = "this proof does not show this namespace's app on a genuine device"

	pathPolicy        = "/v1/namespace/attestation-policy"
	pathChallenge     = "/v1/attest/challenge"
	pathAttestIOS     = "/v1/attest/ios"
	pathAttestAndroid = "/v1/attest/android"
	pathPubsubWS      = "/v1/pubsub/ws"
)

// fixture is a namespace the operator created, with the fixture function
// deployed, the app's API key (app-runtime, the kind a bundle ships), an end
// user holding the runtime role with a device, and an admin.
type fixture struct {
	n      *ns.Namespace
	c      *gw.Client
	user   *wallet.EVM
	dev    *wallet.Device
	admin  *gw.Session
	appKey string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("tinygo"); err != nil {
		harness.SkipNotApplicable(t, "tinygo is not on the runner's PATH; `orama function deploy` builds functions with it")
	}
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{Via: ns.ViaOperator})
	deploy(t, n)
	fx := &fixture{n: n, c: harness.GW(t), user: newWallet(t), dev: gw.NewDevice(t, wallet.AlgEd25519)}
	n.CLI.MustOK(t, "members", "add", fx.user.Address(), "--role", "runtime")
	admin := newWallet(t)
	n.CLI.MustOK(t, "members", "add", admin.Address(), "--role", "admin")
	fx.admin = signIn(t, fx.c, admin, n.Name, nil)
	fx.appKey = appKey(t, fx)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupLimit)
		defer cancel()
		if res, err := n.CLI.Run(ctx, "namespace", "attestation-policy", "--namespace", n.Name, "--mode", modeOff); err != nil || res.Exit != 0 {
			t.Errorf("cleanup: failed to turn attestation off in %s: %v %s", n.Name, err, res.Stderr)
		}
	})
	return fx
}

// deploy copies the fixture to a scratch directory and deploys it as the
// namespace's operator; it is private, so a caller needs the invoke grant.
func deploy(t *testing.T, n *ns.Namespace) {
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
	yaml := "name: " + fixtureName + "\npublic: false\nmemory: 64\ntimeout: 30\n"
	if err := os.WriteFile(filepath.Join(dir, "function.yaml"), []byte(yaml), fixturePerm); err != nil {
		t.Fatal(err)
	}
	n.CLI.MustOK(t, "function", "deploy", dir)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupLimit)
		defer cancel()
		if res, err := n.CLI.Run(ctx, "function", "delete", fixtureName, "--force"); err != nil || res.Exit != 0 {
			t.Errorf("cleanup: failed to delete function %s: %v %s", fixtureName, err, res.Stderr)
		}
	})
}

// cliKeyShape and cliKeyID are how `orama namespace keys create` prints the
// key it shows once and its id.
var (
	cliKeyShape = regexp.MustCompile(`(?m)^\s*(orama_(?:sk|rk)_[0-9A-Za-z]+_[0-9A-Za-z]+)\s*$`)
	cliKeyID    = regexp.MustCompile(`(?m)^\s+id:\s+(\d+)\s*$`)
)

// appKey mints the key an app bundle ships (app-runtime) and revokes it at
// cleanup.
func appKey(t *testing.T, fx *fixture) string {
	t.Helper()
	out := fx.n.CLI.MustOK(t, "namespace", "keys", "create", "--scope", "app-runtime", "--label", "e2e-attest").Stdout
	key, id := cliKeyShape.FindStringSubmatch(out), cliKeyID.FindStringSubmatch(out)
	if key == nil || id == nil {
		t.Fatal("keys create printed no key or id")
	}
	if red := fx.c.Recorder().Redactor(); red != nil {
		if err := red.Add(key[1]); err != nil {
			t.Errorf("failed to register the key for redaction: %v", err)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupLimit)
		defer cancel()
		if res, err := fx.n.CLI.Run(ctx, "namespace", "keys", "revoke", "--id", id[1]); err != nil || res.Exit != 0 {
			t.Errorf("cleanup: failed to revoke key %s: %v %s", id[1], err, res.Stderr)
		}
	})
	return key[1]
}

// policyView is the policy as the CLI and the route show it.
type policyView struct {
	Namespace string `json:"namespace"`
	Mode      string `json:"mode"`
	Android   struct {
		Package            string   `json:"package"`
		SigningCertDigests []string `json:"signing_cert_digests"`
	} `json:"android"`
	ReattestInterval string `json:"reattest_interval"`
}

// setPolicy sets an Android app and mode with the CLI, plus extra flags, and
// returns what the gateway stored.
func setPolicy(t *testing.T, fx *fixture, mode string, extra ...string) policyView {
	t.Helper()
	args := append([]string{"namespace", "attestation-policy", "--namespace", fx.n.Name, "--mode", mode,
		"--android-package", testPackage, "--android-signing-digest", testDigest, "--json"}, extra...)
	var got policyView
	if err := oramacli.DecodeJSON(fx.n.CLI.MustOK(t, args...), &got); err != nil {
		t.Fatalf("attestation-policy --json: %v", err)
	}
	return got
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

// invoke posts to the fixture with bearer, or with header alone.
func invoke(t testing.TB, fx *fixture, bearer string, header http.Header) *gw.Response {
	t.Helper()
	h := http.Header{"Content-Type": {"application/json"}}
	for k, v := range header {
		h[k] = v
	}
	return fx.c.MustSend(t, gw.Req{Method: http.MethodPost, Path: "/v1/functions/" + fixtureName + "/invoke",
		Query: url.Values{"namespace": {fx.n.Name}}, Bearer: bearer, Header: h, Body: []byte("{}")})
}

// verdictView is get_caller_attestation_verdict's answer.
type verdictView struct {
	Mode    string `json:"mode"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// fixtureVerdict reads the verdict the fixture was told from its answer.
func fixtureVerdict(t testing.TB, resp *gw.Response) verdictView {
	t.Helper()
	var out struct {
		Verdict string `json:"verdict"`
	}
	if err := resp.Expect(t, http.StatusOK).Decode(&out); err != nil {
		t.Fatal(err)
	}
	var v verdictView
	if err := json.Unmarshal([]byte(out.Verdict), &v); err != nil {
		t.Fatalf("the fixture was told %q, not a verdict: %v", out.Verdict, err)
	}
	return v
}

// refusedUpgrade opens a WebSocket that must be refused, and returns the
// refusal's status and code.
func refusedUpgrade(t testing.TB, fx *fixture, pathQuery, bearer string, header http.Header) (int, string) {
	t.Helper()
	conn, resp, err := fx.c.For(t).DialWS(t.Context(), pathQuery, bearer, header)
	if conn != nil {
		conn.Close()
		return http.StatusSwitchingProtocols, ""
	}
	if resp == nil {
		t.Fatalf("the upgrade got no answer: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, body.Code
}

type challengeView struct {
	Challenge string `json:"challenge"`
	ExpiresAt string `json:"expires_at"`
	DeviceID  string `json:"device_id"`
}

func challenge(t testing.TB, fx *fixture, bearer string) challengeView {
	t.Helper()
	var ch challengeView
	if err := postJSON(t, fx.c, pathChallenge, bearer, nil).Expect(t, http.StatusOK).Decode(&ch); err != nil {
		t.Fatal(err)
	}
	if ch.Challenge == "" {
		t.Fatal("no challenge")
	}
	return ch
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

// expectCode fails unless resp has status and code, and a 401/403 carries an
// error (docs/whitepaper/technical-reference/vol1/14-authorization.md#refusals-and-the-error-code-table).
func expectCode(t testing.TB, resp *gw.Response, status int, code string) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(resp.Body, &body); err != nil || resp.Status != status || body["code"] != code {
		t.Fatalf("want HTTP %d %s, got %d: %.400s", status, code, resp.Status, resp.Body)
	}
	if msg, _ := body["error"].(string); strings.TrimSpace(msg) == "" {
		t.Errorf("HTTP %d %s carries no error", status, code)
	}
	return body
}
