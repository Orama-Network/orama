package provision

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/e2e/harness/fleet"
	"github.com/Orama-Network/orama/e2e/harness/hetzner"
)

// linkedRepo is the repository as these tests read it: e2e/testdata/repo links
// in the files above this module they use, so the go test cache sees them as
// inputs.
var linkedRepo = filepath.Join("..", "..", "testdata", "repo")

func writeRepoRoots(t *testing.T, repo string) {
	t.Helper()
	real, err := os.ReadFile(filepath.Join(linkedRepo, stagingRootsSource))
	if err != nil {
		t.Fatalf("read the repository's staging roots: %v", err)
	}
	dest := filepath.Join(repo, stagingRootsSource)
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, real, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUp_happyPath(t *testing.T) {
	e := newTestEnv(t)
	st, err := up(context.Background(), e.cfg, &testLogger{}, e.d)
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if len(st.Nodes) != nodeCount || st.BaseDomain != "e2e-testrun1."+testZone || st.ChainID != "orama-devnet-e2e-testrun1" {
		t.Fatalf("state %+v", st)
	}
	for _, n := range st.Nodes {
		if n.PublicIP == "" || n.WGIP != wgFromPublic(n.PublicIP) || n.Role != fleet.RoleNameserver || n.ServerID == 0 {
			t.Fatalf("node %+v", n)
		}
	}
	saved, err := fleet.Load(StatePath(e.cfg.WorkDir))
	if err != nil || saved.GatewayURL != "https://e2e-testrun1."+testZone || saved.CAFile == "" {
		t.Fatalf("saved state %+v: %v", saved, err)
	}
	if _, err := os.Stat(st.CAFile); err != nil {
		t.Fatalf("CA file: %v", err)
	}
	if len(e.dns.records) != 2*nodeCount {
		t.Fatalf("DNS records %+v, want NS and glue for 3 nameservers", e.dns.records)
	}
}

func TestUp_drivesTheCLIInOrder(t *testing.T) {
	e := newTestEnv(t)
	if _, err := up(context.Background(), e.cfg, &testLogger{}, e.d); err != nil {
		t.Fatal(err)
	}
	want := []string{"go env", "go build", "orama maint build --output", "orama network add e2e-testrun1", "orama network use",
		"--genesis --acme-ca letsencrypt-staging", "node dns delegation", "--join-via root@", "--join-via root@",
		"orama auth login", "status report", "chain-deploy.sh up"}
	lines := e.cmd.lines()
	i := 0
	for _, l := range lines {
		if i < len(want) && strings.Contains(l, strings.TrimPrefix(want[i], "orama ")) {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("CLI calls missing %q after the first %d; calls:\n%s", want[i], i, strings.Join(lines, "\n"))
	}
}

func TestUp_childrenNeverSeeTokens(t *testing.T) {
	e := newTestEnv(t)
	if _, err := up(context.Background(), e.cfg, &testLogger{}, e.d); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.cmd.calls {
		all := c.String() + " " + strings.Join(c.env, " ")
		if strings.Contains(all, e.cfg.HetznerToken) || strings.Contains(all, e.cfg.CFToken) {
			t.Fatalf("a token reached `%s`", c.String())
		}
		if filepath.Base(c.name) == oramaBinName && !containsAll(c.env, e2eFlag, "RW_AGENT_SOCK=") {
			t.Fatalf("`%s` ran without %s and an explicit RW_AGENT_SOCK: %v", c.String(), e2eFlag, c.env)
		}
	}
}

func containsAll(env []string, wants ...string) bool {
	for _, w := range wants {
		found := false
		for _, kv := range env {
			if strings.HasPrefix(kv, w) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func assertTornDown(t *testing.T, e *testEnv) {
	t.Helper()
	if s, k, f := e.cloud.counts(); s+k+f != 0 {
		t.Fatalf("left behind: %d servers, %d keys, %d firewalls", s, k, f)
	}
	if len(e.dns.records) != 0 {
		t.Fatalf("left DNS records: %+v", e.dns.records)
	}
	matches, _ := filepath.Glob(filepath.Join(e.cfg.WorkDir, "e2e-rw-*"))
	if len(matches) != 0 {
		t.Fatalf("left the agent directory: %v", matches)
	}
}

// assertUpCleanedUp is assertTornDown plus the state file Up removes after a
// complete teardown.
func assertUpCleanedUp(t *testing.T, e *testEnv) {
	t.Helper()
	assertTornDown(t, e)
	if _, err := os.Stat(StatePath(e.cfg.WorkDir)); !os.IsNotExist(err) {
		t.Fatalf("the torn-down run's state file is still there: %v", err)
	}
}

func TestUp_failedJoinTearsEverythingDown(t *testing.T) {
	e := newTestEnv(t)
	e.cmd.fail["--join-via"] = 1
	_, err := up(context.Background(), e.cfg, &testLogger{}, e.d)
	if err == nil || !strings.Contains(err.Error(), "phase joins") || !strings.Contains(err.Error(), "node-2 did not join") {
		t.Fatalf("up with a failing join: %v", err)
	}
	assertUpCleanedUp(t, e)
	if e.agentStops != 1 {
		t.Fatalf("agent stopped %d times, want 1", e.agentStops)
	}
}

func TestUp_certificateNeverIssued(t *testing.T) {
	e := newTestEnv(t)
	e.certErr = context.DeadlineExceeded
	if _, err := up(context.Background(), e.cfg, &testLogger{}, e.d); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("up without a certificate: %v", err)
	}
	assertUpCleanedUp(t, e)
}

func TestUp_serverCreationFailureCleansCreatedServers(t *testing.T) {
	e := newTestEnv(t)
	e.cloud.failCreateAfter = 3
	if _, err := up(context.Background(), e.cfg, &testLogger{}, e.d); err == nil || !strings.Contains(err.Error(), "resource_limit") {
		t.Fatalf("up with a failing create: %v", err)
	}
	assertUpCleanedUp(t, e)
}

func TestUp_refusesARunIDInUse(t *testing.T) {
	e := newTestEnv(t)
	e.cloud.addServer("e2e-testrun1-n1", map[string]string{hetzner.LabelRun: "testrun1"}, time.Now())
	_, err := up(context.Background(), e.cfg, &testLogger{}, e.d)
	if err == nil || !strings.Contains(err.Error(), "already has 1 servers, 0 SSH keys") {
		t.Fatalf("up with a used run id: %v", err)
	}
	if calls := e.cmd.lines(); len(calls) != 0 {
		t.Fatalf("something ran before preflight refused: %v", calls)
	}
	if s, _, _ := e.cloud.counts(); s != 1 {
		t.Fatal("the failed preflight tore down the other run's server")
	}
}

func TestUp_capacityRefusedBeforeBuilding(t *testing.T) {
	e := newTestEnv(t)
	e.cloud.capacityErr = errString("the Hetzner project holds 9 servers")
	if _, err := up(context.Background(), e.cfg, &testLogger{}, e.d); err == nil || !strings.Contains(err.Error(), "holds 9") {
		t.Fatalf("up over capacity: %v", err)
	}
	if calls := e.cmd.lines(); len(calls) != 0 {
		t.Fatalf("built before the quota check: %v", calls)
	}
}

func TestUp_slotOutsideTheFleetIsRefused(t *testing.T) {
	e := newTestEnv(t)
	e.cmd.slotIP = "198.51.100.7"
	_, err := up(context.Background(), e.cfg, &testLogger{}, e.d)
	if err == nil || !strings.Contains(err.Error(), "not a node of this run") {
		t.Fatalf("up with a foreign slot: %v", err)
	}
	assertUpCleanedUp(t, e)
}

func TestUp_unhealthyClusterFails(t *testing.T) {
	e := newTestEnv(t)
	e.cmd.healthy = false
	_, err := up(context.Background(), e.cfg, &testLogger{}, e.d)
	if err == nil || !strings.Contains(err.Error(), "2 of 3 nodes healthy") {
		t.Fatalf("up with an unhealthy cluster: %v", err)
	}
	assertUpCleanedUp(t, e)
}

func TestUp_workDirWithStateIsRefused(t *testing.T) {
	e := newTestEnv(t)
	if err := os.WriteFile(StatePath(e.cfg.WorkDir), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := up(context.Background(), e.cfg, &testLogger{}, e.d); err == nil || !strings.Contains(err.Error(), "already holds") {
		t.Fatalf("up over an earlier state: %v", err)
	}
	if raw, err := os.ReadFile(StatePath(e.cfg.WorkDir)); err != nil || string(raw) != "{}" {
		t.Fatalf("the refused Up removed or changed the other run's state: %q %v", raw, err)
	}
}

func TestUp_previousArchiveNotSignedByTheTestWallet(t *testing.T) {
	e := newTestEnv(t)
	e.cfg.PreviousArchive = filepath.Join(t.TempDir(), "old.tar.gz")
	_, err := up(context.Background(), e.cfg, &testLogger{}, e.d)
	if err == nil || !strings.Contains(err.Error(), previousRefPrefix+"<git ref>") {
		t.Fatalf("up with a foreign previous archive: %v", err)
	}
	assertUpCleanedUp(t, e)
}

func TestUp_previousReleaseFromRef(t *testing.T) {
	e := newTestEnv(t)
	e.cfg.PreviousArchive = previousRefPrefix + "v0.200.0"
	st, err := up(context.Background(), e.cfg, &testLogger{}, e.d)
	if err != nil {
		t.Fatalf("up with a previous ref: %v", err)
	}
	if st.PreviousOramaBin == "" || st.PreviousArchivePath == "" {
		t.Fatalf("previous release missing from %+v", st)
	}
	if !strings.Contains(strings.Join(e.cmd.lines(), "\n"), "archive --format=tar") {
		t.Fatal("the previous ref was not exported with git archive")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// TestUp_reportsOwnership: a run id in use fails without ownership (the
// caller must not tear down by label); a failure after the run registered
// its access reports ownership.
func TestUp_reportsOwnership(t *testing.T) {
	e := newTestEnv(t)
	e.cloud.addServer("e2e-testrun1-n1", map[string]string{hetzner.LabelRun: "testrun1"}, time.Now())
	if _, err := up(context.Background(), e.cfg, &testLogger{}, e.d); err == nil || OwnsResources(err) {
		t.Fatalf("a colliding run id reported ownership: %v", err)
	}
	e = newTestEnv(t)
	e.cmd.fail["--join-via"] = 1
	if _, err := up(context.Background(), e.cfg, &testLogger{}, e.d); err == nil || !OwnsResources(err) {
		t.Fatalf("a failed join did not report ownership: %v", err)
	}
	if OwnsResources(errString("plain")) || OwnsResources(nil) {
		t.Fatal("a plain error reported ownership")
	}
}

// The features that serve a release repository from a node's loopback (release-install,
// autoupdate-agent) need a CLI that honours ORAMA_ALLOW_LOCAL_RELEASE_REPO, and release builds do
// not (core/pkg/releaseverify): the run builds its operator CLI with the localrepo tag and the
// HEAD archive with --test-local-release-repo.
func TestUp_buildsTheCLIsThatMayFetchFromLoopback(t *testing.T) {
	e := newTestEnv(t)
	if _, err := up(context.Background(), e.cfg, &testLogger{}, e.d); err != nil {
		t.Fatal(err)
	}
	var tagged, archive bool
	for _, l := range e.cmd.lines() {
		tagged = tagged || (strings.Contains(l, "go build") && strings.Contains(l, "-tags localrepo"))
		archive = archive || (strings.Contains(l, "maint build --output") && strings.Contains(l, "--test-local-release-repo"))
	}
	if !tagged || !archive {
		t.Errorf("tagged operator CLI %v, tagged HEAD archive %v; calls:\n%s", tagged, archive, strings.Join(e.cmd.lines(), "\n"))
	}
}
