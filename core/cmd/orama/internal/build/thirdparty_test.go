package build

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Orama-Network/orama/core/pkg/constants"
)

// moduleVersion is the version go.mod at path requires module at.
func moduleVersion(t *testing.T, goMod, module string) string {
	t.Helper()
	data, err := os.ReadFile(goMod)
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`(?m)^\s*(?:require\s+)?` + regexp.QuoteMeta(module) + `\s+(v\S+)`)
	m := pattern.FindSubmatch(data)
	if m == nil {
		t.Fatalf("%s does not require %s", goMod, module)
	}
	return string(m[1])
}

// The archive's third-party programs are built from the modules checked in at
// these paths. Each must require exactly the version constants/versions.go
// names, or the archive would hold a program no constant describes.
func TestThirdPartyModules_requireTheVersionsTheConstantsName(t *testing.T) {
	core := filepath.Join("..", "..", "..", "..")
	for _, c := range []struct {
		goMod, module, want string
	}{
		{filepath.Join(core, thirdPartyDir, "olric", "go.mod"), "github.com/olric-data/olric", constants.OlricVersion},
		{filepath.Join(core, thirdPartyDir, "ipfs-cluster", "go.mod"), "github.com/ipfs-cluster/ipfs-cluster", constants.IPFSClusterVersion},
		{filepath.Join(core, "testdata", "repo", "caddy", "go.mod"), "github.com/caddyserver/caddy/v2", "v" + constants.CaddyVersion},
	} {
		if got := moduleVersion(t, c.goMod, c.module); got != c.want {
			t.Errorf("%s requires %s %s, constants say %s", c.goMod, c.module, got, c.want)
		}
	}
}

func TestThirdPartyModules_everyOneIsPinnedByAGoSum(t *testing.T) {
	core := filepath.Join("..", "..", "..", "..")
	for _, dir := range []string{
		filepath.Join(core, thirdPartyDir, "olric"),
		filepath.Join(core, thirdPartyDir, "ipfs-cluster"),
		filepath.Join(core, "testdata", "repo", "caddy"),
	} {
		sum, err := os.ReadFile(filepath.Join(dir, "go.sum"))
		if err != nil {
			t.Errorf("%s has no go.sum: %v", dir, err)
			continue
		}
		if !strings.Contains(string(sum), " h1:") {
			t.Errorf("%s/go.sum lists no module hash", dir)
		}
	}
}

func TestThirdPartyModules_noBuildStepSwitchesTheChecksumDatabaseOff(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"GONOSUMDB=", "GOFLAGS=-mod=mod", "xcaddy"} {
			if strings.Contains(string(src), banned) {
				t.Errorf("%s contains %q: a build must verify every module against its checksum", e.Name(), banned)
			}
		}
	}
}

func TestCoreDNSCommit_isAFullCommitHash(t *testing.T) {
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(constants.CoreDNSCommit) {
		t.Fatalf("CoreDNSCommit %q is not a 40-hex commit hash", constants.CoreDNSCommit)
	}
}
