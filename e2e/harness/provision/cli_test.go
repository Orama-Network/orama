package provision

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecCommander_exactEnvironmentAndLog(t *testing.T) {
	t.Setenv("E2E_LEAK_CHECK", "should-not-pass")
	log := filepath.Join(t.TempDir(), "cmd.log")
	out, err := execCommander{}.Run(context.Background(), command{
		name: "sh", args: []string{"-c", `echo "out:${E2E_LEAK_CHECK:-none}:$ONLY"; echo err >&2`},
		env: []string{"ONLY=this", "PATH=" + os.Getenv("PATH")}, log: log,
	})
	if err != nil || out != "out:none:this\n" {
		t.Fatalf("stdout %q err %v", out, err)
	}
	raw, _ := os.ReadFile(log)
	if !strings.Contains(string(raw), "out:none:this") || !strings.Contains(string(raw), "err") {
		t.Fatalf("log %q", raw)
	}
}

func TestExecCommander_failureCarriesExitAndTail(t *testing.T) {
	_, err := execCommander{}.Run(context.Background(), command{
		name: "sh", args: []string{"-c", "echo broken >&2; exit 4"}, env: []string{"PATH=" + os.Getenv("PATH")},
	})
	if exitCode(err) != cliExitNotFound || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("err %v exit %d", err, exitCode(err))
	}
}

func TestExecCommander_stdinAndMissingBinary(t *testing.T) {
	out, err := execCommander{}.Run(context.Background(), command{name: "cat", stdin: []byte("piped"), env: []string{"PATH=" + os.Getenv("PATH")}})
	if err != nil || out != "piped" {
		t.Fatalf("stdin: %q %v", out, err)
	}
	_, err = execCommander{}.Run(context.Background(), command{name: filepath.Join(t.TempDir(), "absent")})
	if err == nil || exitCode(err) != -1 {
		t.Fatalf("missing binary: %v", err)
	}
}

func TestHealthReport_problem(t *testing.T) {
	ok := healthReport{}
	ok.Meta.NodeCount, ok.Meta.HealthyCount = 3, 3
	ok.Summary.RQLiteLeader, ok.Summary.RQLiteQuorum, ok.Summary.WGMeshStatus = "203.0.113.1", summaryOK, summaryOK
	if p := ok.problem(3); p != "" {
		t.Fatalf("healthy report: %s", p)
	}
	cases := map[string]func(*healthReport){
		"of 3 nodes":         func(h *healthReport) { h.Meta.HealthyCount = 2 },
		"rqlite leader":      func(h *healthReport) { h.Summary.RQLiteLeader = noLeader },
		"quorum":             func(h *healthReport) { h.Summary.RQLiteQuorum = "lost" },
		"WireGuard":          func(h *healthReport) { h.Summary.WGMeshStatus = "degraded" },
		"critical alerts":    func(h *healthReport) { h.Summary.CriticalAlerts = 1 },
		"healthy (want 3)":   func(h *healthReport) { h.Meta.NodeCount, h.Meta.HealthyCount = 2, 2 },
		"rqlite leader \"\"": func(h *healthReport) { h.Summary.RQLiteLeader = "" },
	}
	for want, mutate := range cases {
		h := ok
		mutate(&h)
		if p := h.problem(3); !strings.Contains(p, want) {
			t.Errorf("%s: problem %q", want, p)
		}
	}
}

func TestReadWireGuardIPs_refusesAddressOutsideTheOverlay(t *testing.T) {
	e, st := upForTest(t)
	e.remote.wgIP = func(string) string { return "10.9.9.9" }
	r := &run{cfg: e.cfg, d: e.d, st: st, log: &testLogger{}}
	if err := r.readWireGuardIPs(context.Background()); err == nil || !strings.Contains(err.Error(), wgSubnet) {
		t.Fatalf("wg0 outside the overlay: %v", err)
	}
}

// chainScriptPath is the real script, run here only on paths that stop
// before any network access, or with a fake ssh.
func chainScriptPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "scripts", "chain-deploy.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// bash reads the script in a process of its own, which the go test
	// cache does not see; reading it here makes it an input of the test.
	if _, err := os.ReadFile(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func runChainScript(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{chainScriptPath(t)}, args...)...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func chainEnv(t *testing.T, chainID, nodes string) []string {
	t.Helper()
	dir := t.TempDir()
	key, kh := filepath.Join(dir, "k"), filepath.Join(dir, "kh")
	for _, f := range []string{key, kh} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return []string{"CHAIN_ID=" + chainID, "E2E_CHAIN_NODES=" + nodes, "E2E_SSH_KEY=" + key, "E2E_KNOWN_HOSTS=" + kh}
}

func TestChainScript_refusesUnsafeInput(t *testing.T) {
	good := "node-1:203.0.113.1:10.0.0.1"
	cases := map[string][]string{
		"orama-[<name>-]devnet-<id> exactly": chainEnv(t, "orama-mainnet-1", good),
		"invalid CHAIN_ID":                   chainEnv(t, "Orama;rm", good),
		"WireGuard IP":                       chainEnv(t, "orama-devnet-e2e-x", "node-1:203.0.113.1:192.168.1.1"),
		"public IP":                          chainEnv(t, "orama-devnet-e2e-x", "node-1:203.0.113.999:10.0.0.1"),
		"node name":                          chainEnv(t, "orama-devnet-e2e-x", "Node$(id):203.0.113.1:10.0.0.1"),
		"is empty":                           chainEnv(t, "orama-devnet-e2e-x", ""),
		"want name:public-ip":                chainEnv(t, "orama-devnet-e2e-x", good+":extra"),
		"E2E_SSH_KEY":                        {"CHAIN_ID=orama-devnet-e2e-x", "E2E_CHAIN_NODES=" + good},
	}
	for want, env := range cases {
		out, err := runChainScript(t, env, "status")
		if err == nil || !strings.Contains(out, want) {
			t.Errorf("%s: err %v output %q", want, err, out)
		}
	}
}

// The chain-id gate is the chain's exact rule, not a substring: an id that only contains -devnet-, is of
// another class, or has a class word as its name word is refused before any machine is touched.
func TestChainScript_refusesAChainIDThatOnlyContainsDevnet(t *testing.T) {
	good := "node-1:203.0.113.1:10.0.0.1"
	for _, id := range []string{"orama-1-devnet-x", "foo-devnet-bar", "orama-testnet-1", "orama-localnet-1", "orama-devnet-localnet-1", "orama-devnet-"} {
		out, err := runChainScript(t, chainEnv(t, id, good), "status")
		if err == nil || !strings.Contains(out, "devnet-<id> exactly") {
			t.Errorf("%s: err %v output %q", id, err, out)
		}
	}
}

func TestChainScript_statusThroughSSH(t *testing.T) {
	bin := t.TempDir()
	fake := "#!/bin/sh\ncase \"$*\" in *'-o StrictHostKeyChecking=yes'*) echo 42 ;; *) exit 9 ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	env := chainEnv(t, "orama-devnet-e2e-x", "node-1:203.0.113.1:10.0.0.1 node-2:203.0.113.2:10.0.0.2")
	cmd := exec.Command("bash", chainScriptPath(t), "status")
	cmd.Env = append([]string{"PATH=" + bin + ":" + os.Getenv("PATH")}, env...)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.Count(string(out), "height 42") != 2 {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if out, err := runChainScript(t, env, "bogus"); err == nil || !strings.Contains(out, "usage") {
		t.Fatalf("unknown subcommand: %v %s", err, out)
	}
}
