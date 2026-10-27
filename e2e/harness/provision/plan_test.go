package provision

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlan_followsThePhasesWithoutSideEffects(t *testing.T) {
	e := newTestEnv(t)
	e.cfg.WorkDir = filepath.Join(t.TempDir(), "never-created")
	e.cfg.ProbeLocation = "hel1"
	actions, err := Plan(e.cfg)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var order []string
	for _, a := range actions {
		if len(order) == 0 || order[len(order)-1] != a.Phase {
			order = append(order, a.Phase)
		}
	}
	var want []string
	for _, p := range phases() {
		want = append(want, p.name)
	}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("plan phases %v, want %v", order, want)
	}
	all, _ := json.Marshal(actions)
	for _, s := range []string{"e2e-testrun1-n3", "e2e-testrun1-probe-1", "--genesis", "orama-devnet-e2e-testrun1"} {
		if !strings.Contains(string(all), s) {
			t.Errorf("the plan does not mention %s", s)
		}
	}
	if strings.Contains(string(all), e.cfg.HetznerToken) {
		t.Fatal("the plan prints a token")
	}
	if _, err := os.Stat(e.cfg.WorkDir); !os.IsNotExist(err) {
		t.Fatal("Plan created the work dir")
	}
}

func TestPlan_previousRelease(t *testing.T) {
	e := newTestEnv(t)
	e.cfg.PreviousArchive = previousRefPrefix + "v0.200.0"
	actions, err := Plan(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	all, _ := json.Marshal(actions)
	if !strings.Contains(string(all), "git archive v0.200.0") || !strings.Contains(string(all), prevArchive) {
		t.Fatalf("plan for a previous ref: %s", all)
	}
}

func TestPlan_invalidConfig(t *testing.T) {
	e := newTestEnv(t)
	e.cfg.RunID = ""
	if _, err := Plan(e.cfg); err == nil {
		t.Fatal("Plan accepted an empty run id")
	}
}

func TestCheckStagingRoots(t *testing.T) {
	real, err := os.ReadFile(filepath.Join(linkedRepo, stagingRootsSource))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkStagingRoots(real); err != nil {
		t.Fatalf("the shipped roots: %v", err)
	}
	first, _ := pem.Decode(real)
	if err := checkStagingRoots(pem.EncodeToMemory(first)); err == nil {
		t.Fatal("a bundle missing a root was accepted")
	}
	if err := checkStagingRoots(nil); err == nil {
		t.Fatal("an empty bundle was accepted")
	}
}

func TestWaitCertificate_verifiesAgainstTheCAFileOnly(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // refused handshakes are the point
	srv.StartTLS()
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")
	own := filepath.Join(t.TempDir(), "own.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(own, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := waitCertificate(context.Background(), addr, "example.com", own, 10*time.Millisecond); err != nil {
		t.Fatalf("a certificate the CA file trusts: %v", err)
	}
	staging := filepath.Join(linkedRepo, stagingRootsSource)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := waitCertificate(ctx, addr, "example.com", staging, 20*time.Millisecond); err == nil {
		t.Fatal("a certificate outside the staging roots was accepted")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	if err := waitCertificate(ctx2, addr, "wrong.example.org", own, 20*time.Millisecond); err == nil {
		t.Fatal("a certificate for another name was accepted")
	}
}
