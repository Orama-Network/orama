package clusterguide

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseGuide_commandsSectionsAndContinuations(t *testing.T) {
	md := "# Title\n\n## Install\n\n```bash\nrw vault add 1.2.3.4\n# a comment\norama node setup --ip 1.2.3.4 \\\n  --genesis --role nameserver\n```\n\nprose\n\n## Use it\n\n```bash\norama auth login --namespace 'my app'\n```\n"
	cmds, err := ParseGuide(md)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 3 {
		t.Fatalf("parsed %d commands: %+v", len(cmds), cmds)
	}
	if cmds[0].Section != "Install" || !reflect.DeepEqual(cmds[0].Argv, []string{"rw", "vault", "add", "1.2.3.4"}) {
		t.Errorf("first command = %+v", cmds[0])
	}
	want := []string{"orama", "node", "setup", "--ip", "1.2.3.4", "--genesis", "--role", "nameserver"}
	if !reflect.DeepEqual(cmds[1].Argv, want) {
		t.Errorf("continued command = %v, want %v", cmds[1].Argv, want)
	}
	if cmds[2].Section != "Use it" || cmds[2].Argv[len(cmds[2].Argv)-1] != "my app" {
		t.Errorf("quoted command = %+v", cmds[2])
	}
}

func TestParseGuide_ignoresNonShellFencesAndProse(t *testing.T) {
	md := "## Install\n\n```yaml\norama: not a command\n```\n\nRun `orama maint build` first.\n\n```\norama also not\n```\n"
	cmds, err := ParseGuide(md)
	if err != nil || len(cmds) != 0 {
		t.Fatalf("cmds = %+v, err = %v", cmds, err)
	}
}

func TestParseGuide_refusals(t *testing.T) {
	tests := map[string]string{
		"unknown program":       "## A\n```bash\ncurl https://x\n```\n",
		"pipe":                  "## A\n```bash\norama status | jq .\n```\n",
		"unterminated quote":    "## A\n```bash\norama x 'y\n```\n",
		"dangling continuation": "## A\n```bash\norama x \\\n```\n",
		"variable":              "## A\n```bash\norama x $HOME\n```\n",
	}
	for name, md := range tests {
		if _, err := ParseGuide(md); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
}

func TestParseGuide_dropsTrailingComments(t *testing.T) {
	cmds, err := ParseGuide("## A\n```bash\norama restore-key   # the key's # sign\norama x 'a # b'\n```\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cmds[0].Argv, " "); got != "orama restore-key" {
		t.Errorf("command = %q", got)
	}
	if got := cmds[1].Argv[2]; got != "a # b" {
		t.Errorf("a quoted # was treated as a comment: %q", got)
	}
}

func TestFlags(t *testing.T) {
	got := Flags([]string{"orama", "node", "setup", "--ip", "1", "--env=x", "-h", "--genesis"})
	if want := []string{"--ip", "--env", "--genesis"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Flags = %v, want %v", got, want)
	}
}

// guidePath finds website/src/docs/operator/run-your-own-cluster.mdx through
// the module's testdata/repo link, walking up from the package directory. The
// guide lives above the module; reading it through a path inside the module is
// what makes the go test cache treat it as an input.
func guidePath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for ; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		p := filepath.Join(dir, "testdata", "repo", "website", "src", "docs", "operator", "run-your-own-cluster.mdx")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Fatal("website/src/docs/operator/run-your-own-cluster.mdx not found above " + dir)
	return ""
}

func readGuide(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(guidePath(t))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// The e2e plan and the guide on disk must agree. This is the check that keeps
// the executed steps and the page from drifting; it needs no machines.
func TestPlanMatchesTheGuideOnDisk(t *testing.T) {
	cmds, err := ParseGuide(readGuide(t))
	if err != nil {
		t.Fatal(err)
	}
	r := Runner{Fx: testFixture()}
	if err := r.Verify(CoveredCommands(cmds), Plan()); err != nil {
		t.Fatalf("website/src/docs/operator/run-your-own-cluster.mdx and core/e2e/clusterguide/plan.go disagree: %v", err)
	}
}

func TestGuideHasNoCommandInAnUnknownSection(t *testing.T) {
	cmds, err := ParseGuide(readGuide(t))
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{SectionInstall: true, SectionUse: true, SectionCheck: true}
	for _, c := range cmds {
		if _, unc := uncoveredSections[c.Section]; !known[c.Section] && !unc {
			t.Errorf("%q is under %q, which the plan neither runs nor lists as uncovered", c.Line, c.Section)
		}
	}
	for section := range uncoveredSections {
		if !strings.Contains(readGuide(t), "## "+section+"\n") {
			t.Errorf("uncoveredSections names %q, which the guide no longer has", section)
		}
	}
}
