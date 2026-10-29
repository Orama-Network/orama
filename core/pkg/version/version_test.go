package version

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoVersionPath is the repository's VERSION file, relative to this package.
// The module root is core/, so the file sits one level above it.
const repoVersionPath = "../../testdata/repo/VERSION"

// The embedded copy exists so every build path — go build, go install, go run,
// an IDE — reports the real version, which a -ldflags-only scheme could not.
// Two files holding the same number drift unless something checks, and the
// consequence of drift is a version gate that passes on a mismatch.
func TestEmbeddedVersionMatchesTheRepository(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(repoVersionPath))
	if err != nil {
		t.Skipf("repository VERSION not reachable from here: %v", err)
	}
	want := strings.TrimSpace(string(raw))

	if Current != want {
		t.Fatalf("pkg/version/version.txt is %q but VERSION is %q.\n"+
			"Run `make bump VER=%s`, which writes both.", Current, want, want)
	}
}

func TestCurrentIsASemanticVersion(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+`).MatchString(Current) {
		t.Errorf("Current = %q, want a semantic version", Current)
	}
}

// A trailing newline in the file would end up in every version string and in
// the middle of a comparison against a node's reported version.
func TestCurrentIsTrimmed(t *testing.T) {
	if Current != strings.TrimSpace(Current) {
		t.Errorf("Current = %q has surrounding whitespace", Current)
	}
}
