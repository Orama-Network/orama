//go:build e2e_fleet

package sdkts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Orama-Network/orama/e2e/features/internal/cliconf"
	"github.com/Orama-Network/orama/e2e/features/internal/tenancy"
	"github.com/Orama-Network/orama/e2e/harness"
	"github.com/Orama-Network/orama/e2e/harness/evidence"
	"github.com/Orama-Network/orama/e2e/harness/fleet"
	"github.com/Orama-Network/orama/e2e/harness/gw"
	"github.com/Orama-Network/orama/e2e/harness/ns"
	"github.com/Orama-Network/orama/e2e/harness/pace"
	"github.com/Orama-Network/orama/e2e/harness/wallet"
)

const (
	// vitestBudget bounds the whole TypeScript run.
	vitestBudget = 20 * time.Minute
	// vitestWaitDelay bounds the output pipes staying open after vitest's
	// process group was killed.
	vitestWaitDelay = 10 * time.Second
	// sdkTestsDir is the SDK's own e2e suite, under sdk/.
	sdkTestsDir = "tests/e2e"
	// featureTestsDir holds this feature's TypeScript tests.
	featureTestsDir = "e2e/features/sdk-ts/tests"
	// The TypeScript tests' credential calls are not paced by the harness
	// (they are the SDK's own fetches), so their tokens are drawn up front:
	// verify, refresh and a replayed verify on the public gateway; refresh,
	// the device link start and claim on the namespace gateway, and the
	// device-bound session's two refreshes on a skewed clock, each refused
	// once for its time and sent again, and its refresh before logging out
	// everywhere.
	mainCredentialCalls = 4
	nsCredentialCalls   = 9
)

// tools finds node and pnpm; without them the suite cannot run here.
func tools(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		harness.SkipNotApplicable(t, "node is not installed on the runner: install Node 20+ to run the TypeScript SDK suite")
	}
	pnpm, err := exec.LookPath("pnpm")
	if err != nil {
		harness.SkipNotApplicable(t, "pnpm is not installed on the runner: install pnpm to run the TypeScript SDK suite")
	}
	return pnpm
}

// TestSDKTypeScript_suiteAgainstFleet runs the TypeScript SDK's e2e suite
// (sdk/tests/e2e) and this feature's additions (auth sessions, device link,
// storage getBinary, db Repository and dropTable, functions.invoke, the
// chain read proxy, key profiles, the workload client, WebSocket revocation)
// through vitest against a fresh namespace, and reports each vitest test as
// a Go subtest.
func TestSDKTypeScript_suiteAgainstFleet(t *testing.T) {
	t.Parallel()
	pnpm := tools(t)
	root := cliconf.RepoRoot(t)
	sdkDir := filepath.Join(root, "sdk")
	if _, err := os.Stat(filepath.Join(sdkDir, "node_modules", "vitest")); err != nil {
		harness.SkipNotApplicable(t, "sdk/node_modules has no vitest: run `pnpm install --frozen-lockfile` in sdk/ before the run")
	}
	f := harness.Fleet(t)
	n := tenancy.Namespace(t, f, ns.Options{})
	env := suiteEnv(t, f, n)
	preCharge(t, f, n)
	dir := t.TempDir()
	cfg := writeVitestConfig(t, dir, sdkDir, filepath.Join(root, featureTestsDir))
	report := filepath.Join(dir, "vitest-report.json")
	exit := runVitest(t, f, pnpm, sdkDir, cfg, report, env)
	r := readReport(t, report)
	requireEveryFile(t, r, []string{filepath.Join(sdkDir, sdkTestsDir), filepath.Join(root, featureTestsDir)})
	if replay(t, r, f.Redact) == 0 && exit != 0 {
		t.Errorf("vitest exited %d with no failed assertion (a hook, a load or a teardown failed): see the recorded vitest output", exit)
	}
}

// runVitest runs the suite in a process group of its own, killed as a whole
// (pnpm, node and any worker) when vitestBudget passes, with its output
// recorded as evidence. It returns vitest's exit code and fails when vitest
// could not run or wrote no report.
func runVitest(t *testing.T, f *fleet.Fleet, pnpm, sdkDir, cfg, report string, env []string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), vitestBudget)
	defer cancel()
	// The default reporter writes to the recorded output what the JSON report
	// leaves out: an unhandled error or rejection, which fails the run with no
	// failed assertion to point at.
	cmd := exec.CommandContext(ctx, pnpm, "--dir", sdkDir, "exec", "vitest", "run",
		"--config", cfg, "--reporter=default", "--reporter=json", "--outputFile.json="+report)
	cmd.Env, cmd.Dir = env, filepath.Dir(report)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("failed to kill vitest's process group %d: %w", cmd.Process.Pid, err)
		}
		return nil
	}
	cmd.WaitDelay = vitestWaitDelay
	res, err := evidence.RunRecorded(t, f.Recorder(), "vitest", cmd)
	if err != nil {
		t.Fatalf("failed to run vitest: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("vitest did not finish within %v and was killed", vitestBudget)
	}
	if _, statErr := os.Stat(report); statErr != nil {
		t.Fatalf("vitest exited %d without a report:\n%s%s", res.Exit, f.Redact(res.Stdout), f.Redact(res.Stderr))
	}
	return res.Exit
}

// suiteEnv is the whole environment of the vitest process: nothing of the
// runner's leaks into it but PATH and the locale.
func suiteEnv(t *testing.T, f *fleet.Fleet, n *ns.Namespace) []string {
	t.Helper()
	key := tenancy.APIKey(t, n, "app-runtime")
	if err := n.Client.Protect(key); err != nil {
		t.Fatal(err)
	}
	member, wsUser, endedUser := tenancy.Member(t, n, "admin"), tenancy.Member(t, n, "admin"), tenancy.Member(t, n, "admin")
	deviceUser := tenancy.DeviceMember(t, n, "admin")
	deviceJWK, err := deviceUser.Device.PrivateJWK()
	if err != nil {
		t.Fatal(err)
	}
	w, msg, sig := lobbySignIn(t)
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "LANG=C.UTF-8", "CI=1",
		"NODE_EXTRA_CA_CERTS=" + f.State.CAFile, "E2E_FLEET=1",
		"GATEWAY_BASE_URL=" + n.URL, "GATEWAY_API_KEY=" + key, "GATEWAY_JWT=" + n.Owner.Token(),
		"E2E_MAIN_GATEWAY_URL=" + f.State.GatewayURL, "E2E_NAMESPACE=" + n.Name,
		"E2E_SIWE_WALLET=" + w, "E2E_SIWE_MESSAGE=" + msg, "E2E_SIWE_SIGNATURE=" + sig,
		"E2E_MEMBER_REFRESH=" + member.Session.RefreshToken,
		"E2E_WS_JWT=" + wsUser.Token(), "E2E_WS_REFRESH=" + wsUser.Session.RefreshToken,
		// The SDK's own auth test logs a session out: a session of its own, or
		// it would revoke the owner's GATEWAY_JWT every later test uses.
		"E2E_LOGOUT_JWT=" + endedUser.Token(), "E2E_LOGOUT_REFRESH=" + endedUser.Session.RefreshToken,
		// A session bound to a device, and the device's private key, for the
		// SDK's device proofs on a skewed clock and its logout everywhere.
		"E2E_DEVICE_REFRESH=" + deviceUser.Session.RefreshToken, "E2E_DEVICE_ID=" + deviceUser.Device.ID(),
		"E2E_DEVICE_JWK=" + string(deviceJWK),
	}
	if f.State.ChainID != "" {
		env = append(env, "E2E_CHAIN=1")
	}
	return env
}

// lobbySignIn asks the public gateway for a lobby challenge for a fresh
// wallet and signs it, so the TypeScript side can call verify with a real
// signature (it has no secp256k1/keccak of its own). The TypeScript test
// logs the session out.
func lobbySignIn(t *testing.T) (address, message, signature string) {
	t.Helper()
	w, err := wallet.NewEVM()
	if err != nil {
		t.Fatal(err)
	}
	ch, _, err := harness.GW(t).Challenge(t.Context(), gw.ChallengeRequest{Wallet: w.Address()})
	if err != nil {
		t.Fatalf("lobby challenge: %v", err)
	}
	sig, err := w.Sign(ch.Message)
	if err != nil {
		t.Fatal(err)
	}
	return w.Address(), ch.Message, sig
}

// preCharge draws the TypeScript tests' credential tokens from the run's
// buckets before they run (see mainCredentialCalls).
func preCharge(t *testing.T, f *fleet.Fleet, n *ns.Namespace) {
	t.Helper()
	p, err := pace.FromEnv(os.LookupEnv)
	if err != nil {
		t.Fatalf("the run's pacer: %v", err)
	}
	for range mainCredentialCalls + nsCredentialCalls {
		if err := p.Wait(t.Context(), pace.BucketCred); err != nil {
			t.Fatalf("pacing the TypeScript tests' credential calls: %v", err)
		}
	}
}
