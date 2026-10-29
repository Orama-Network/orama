package process

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/deployments"
	"go.uber.org/zap"
)

// recordingStager is a Stager that keeps what it was handed.
type recordingStager struct {
	env     map[string]string
	token   map[string]string
	ports   map[string]string // instance -> "<runtime>:<port>"
	cleared []string
	purged  []string
	state   []string // what StateInstances lists
	// log is every call in order, shared with a test's systemctl seam.
	log []string
}

func newRecordingStager() *recordingStager {
	return &recordingStager{env: map[string]string{}, token: map[string]string{}, ports: map[string]string{}}
}

func (r *recordingStager) SetEnv(instance, contents string) error {
	r.env[instance] = contents
	r.log = append(r.log, "set-env "+instance)
	return nil
}

func (r *recordingStager) AllowPort(instance string, runtime Runtime, port int) error {
	r.ports[instance] = fmt.Sprintf("%s:%d", runtime, port)
	r.log = append(r.log, fmt.Sprintf("bind-port %s %s %d", instance, runtime, port))
	return nil
}

func (r *recordingStager) BuildUser(instance string) error {
	r.log = append(r.log, "build-user "+instance)
	return nil
}

func (r *recordingStager) PurgeState(instance string) error {
	r.log = append(r.log, "purge "+instance)
	r.purged = append(r.purged, instance)
	return nil
}

func (r *recordingStager) StateInstances() ([]string, error) { return r.state, nil }

func (r *recordingStager) SetToken(instance, token string) error {
	r.token[instance] = token
	return nil
}

func (r *recordingStager) Clear(instance string) error {
	r.cleared = append(r.cleared, instance)
	r.log = append(r.log, "clear "+instance)
	delete(r.env, instance)
	delete(r.token, instance)
	delete(r.ports, instance)
	return nil
}

func testManager(t *testing.T) (*Manager, *recordingStager) {
	t.Helper()
	st := newRecordingStager()
	return NewManager(zap.NewNop(), Config{Stager: st, BaseDomain: "dbrs.space"}), st
}

func testDeployment() *deployments.Deployment {
	return &deployments.Deployment{
		Namespace: "acme",
		Name:      "api",
		Port:      8080,
		Environment: map[string]string{
			"DATABASE_URL": "postgres://u:p@h/db",
			"NOTE":         "he said \"hi\"\nand left",
		},
	}
}

// The environment is handed to the stager under the unit instance (%i) the
// template names its files by: orama-deploy-acme-api -> acme-api.
func TestWriteEnvFile_writesTheTenantsValuesAndThePlatforms(t *testing.T) {
	m, st := testManager(t)

	if err := m.writeEnvFile(testDeployment(), "orama-deploy-acme-api"); err != nil {
		t.Fatalf("writeEnvFile: %v", err)
	}
	contents, ok := st.env["acme-api"]
	if !ok {
		t.Fatalf("nothing staged for instance acme-api: %v", st.env)
	}
	got := systemdReadEnvFile(contents)

	for key, want := range map[string]string{
		"DATABASE_URL":      "postgres://u:p@h/db",
		"NOTE":              "he said \"hi\"\nand left",
		"PORT":              "8080",
		"ORAMA_NAMESPACE":   "acme",
		"ORAMA_GATEWAY_URL": "https://ns-acme.dbrs.space",
		"ORAMA_STATE_DIR":   "/var/lib/orama-deploy-acme-api",
		"ORAMA_CACHE_DIR":   "/var/cache/orama-deploy-acme-api",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q\nfile:\n%s", key, got[key], want, contents)
		}
	}
}

func TestWriteEnvFile_refusesWithNowhereSafeToPutIt(t *testing.T) {
	m := NewManager(zap.NewNop(), Config{BaseDomain: "dbrs.space"})
	err := m.writeEnvFile(testDeployment(), "orama-deploy-acme-api")
	if err == nil {
		t.Fatal("the environment was written with no stager configured")
	}
	if !strings.Contains(err.Error(), "no deployment stager is configured") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}

func TestWriteEnvFile_refusesAValueItCouldNotDeliver(t *testing.T) {
	m, st := testManager(t)
	d := testDeployment()
	d.Environment["BROKEN"] = "\xff"

	if err := m.writeEnvFile(d, "orama-deploy-acme-api"); err == nil {
		t.Fatal("a value systemd would discard was written anyway")
	}
	if _, ok := st.env["acme-api"]; ok {
		t.Error("a rejected environment was still staged")
	}
}

// The token goes to the stager under the same instance as the environment.
func TestWriteWorkloadToken_stagesTheMintedToken(t *testing.T) {
	m, st := testManager(t)
	m.SetWorkloadTokenMinter(func(_ context.Context, ns, name string) (string, error) { return "tok-" + ns + "-" + name, nil })
	if err := m.writeWorkloadToken(context.Background(), testDeployment(), "orama-deploy-acme-api"); err != nil {
		t.Fatal(err)
	}
	if st.token["acme-api"] != "tok-acme-api" {
		t.Errorf("staged token = %q", st.token["acme-api"])
	}
}

// The secrets must not outlive the deployment.
func TestRemoveSecrets_takesTheSecretsOffTheNode(t *testing.T) {
	m, st := testManager(t)
	if err := m.writeEnvFile(testDeployment(), "orama-deploy-acme-api"); err != nil {
		t.Fatalf("writeEnvFile: %v", err)
	}
	if err := m.removeSecrets("orama-deploy-acme-api"); err != nil {
		t.Fatalf("removeSecrets: %v", err)
	}
	if len(st.cleared) != 1 || st.cleared[0] != "acme-api" {
		t.Fatalf("cleared %v, want [acme-api]", st.cleared)
	}
}

func TestGatewayURL(t *testing.T) {
	m, _ := testManager(t)
	if got := m.gatewayURL("acme"); got != "https://ns-acme.dbrs.space" {
		t.Errorf("gatewayURL = %q", got)
	}
	if got := m.gatewayURL(""); got != "" {
		t.Errorf("gatewayURL with no namespace = %q, want empty", got)
	}

	noDomain := NewManager(zap.NewNop(), Config{Stager: newRecordingStager()})
	if got := noDomain.gatewayURL("acme"); got != "" {
		t.Errorf("gatewayURL with no base domain = %q, want empty", got)
	}
}

// systemdReadEnvFile is the same transcription of systemd's environment-file
// parser the encoder is tested against, reduced to what these tests need: it
// reads back what systemd would read, not what the writer meant.
func systemdReadEnvFile(contents string) map[string]string {
	out := map[string]string{}
	for _, assignment := range splitAssignments(contents) {
		eq := strings.IndexByte(assignment, '=')
		if eq < 0 {
			continue
		}
		key, raw := assignment[:eq], assignment[eq+1:]
		out[key] = unquoteEnvValue(raw)
	}
	return out
}

// splitAssignments splits on the newlines that are not inside a quoted value.
func splitAssignments(contents string) []string {
	var out []string
	var current strings.Builder
	inQuotes, escaped := false, false
	for i := 0; i < len(contents); i++ {
		c := contents[i]
		switch {
		case escaped:
			escaped = false
			current.WriteByte(c)
		case c == '\\' && inQuotes:
			escaped = true
			current.WriteByte(c)
		case c == '"':
			inQuotes = !inQuotes
			current.WriteByte(c)
		case c == '\n' && !inQuotes:
			if current.Len() > 0 {
				out = append(out, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(c)
		}
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}

func unquoteEnvValue(raw string) string {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return raw
	}
	inner := raw[1 : len(raw)-1]
	var b strings.Builder
	for i := 0; i < len(inner); i++ {
		if inner[i] == '\\' && i+1 < len(inner) && strings.IndexByte("\"\\`$", inner[i+1]) >= 0 {
			i++
			b.WriteByte(inner[i])
			continue
		}
		b.WriteByte(inner[i])
	}
	return b.String()
}

// The direct runner is what runs a deployment off systemd. It has to hand the
// app the same environment the unit would, or an app behaves differently
// depending on which runner started it.
func TestStartDirect_givesTheAppTheSameEnvironmentTheUnitWould(t *testing.T) {
	// The direct runner logs under $HOME; a test's run belongs in its own.
	t.Setenv("HOME", t.TempDir())
	m, _ := testManager(t)
	workDir := t.TempDir()
	dumped := filepath.Join(workDir, "env.txt")

	script := "#!/bin/sh\nenv > " + dumped + "\n"
	if err := os.WriteFile(filepath.Join(workDir, "app"), []byte(script), 0755); err != nil {
		t.Fatalf("write app: %v", err)
	}

	d := testDeployment()
	d.Type = deployments.DeploymentTypeGoBackend
	if err := m.startDirect(context.Background(), d, workDir); err != nil {
		t.Fatalf("startDirect: %v", err)
	}

	// The runner forgets a process once it has exited, so the environment is
	// read whole, not while the shell is still writing it, and the runner's
	// goroutine waiting on the process is done before the test ends.
	serviceName := m.getServiceName(d)
	deadline := time.Now().Add(5 * time.Second)
	for running(m, serviceName) {
		if time.Now().After(deadline) {
			t.Fatal("the spawned process did not exit")
		}
		time.Sleep(20 * time.Millisecond)
	}
	contents, err := os.ReadFile(dumped)
	if err != nil || len(contents) == 0 {
		t.Fatalf("the spawned process never wrote its environment: %v", err)
	}

	env := string(contents)
	for _, want := range []string{
		"PORT=8080",
		"ORAMA_NAMESPACE=acme",
		"ORAMA_GATEWAY_URL=https://ns-acme.dbrs.space",
		"ORAMA_STATE_DIR=/var/lib/orama-deploy-acme-api",
		"ORAMA_CACHE_DIR=/var/cache/orama-deploy-acme-api",
		"DATABASE_URL=postgres://u:p@h/db",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("the process did not get %s\nenvironment:\n%s", want, env)
		}
	}
}

// running reports whether the direct runner still tracks serviceName's
// process: it stops once the process has exited and been waited for.
func running(m *Manager, serviceName string) bool {
	m.processesMu.RLock()
	defer m.processesMu.RUnlock()
	_, ok := m.processes[serviceName]
	return ok
}
