package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Orama-Network/orama/core/cmd/orama/internal/clierr"
)

// Bug: `build --arch sparc` exited 1 after the project-root and zig lookups;
// a bad value is a usage error, raised before the build looks for anything.
func TestBuild_badArchIsUsageBeforeAnyLookup(t *testing.T) {
	err := NewBuilder(&Flags{Arch: "sparc", Unsigned: true}).Build()
	if got := clierr.CodeOf(err); got != clierr.CodeUsage {
		t.Fatalf("exit code %d (%v), want %d", got, err, clierr.CodeUsage)
	}
	if !strings.Contains(err.Error(), "sparc") {
		t.Errorf("the error does not name the bad value: %v", err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if err := validateArch(arch); err != nil {
			t.Errorf("%s refused: %v", arch, err)
		}
	}
	if err := validateArch(""); clierr.CodeOf(err) != clierr.CodeUsage {
		t.Errorf("an empty arch is not a usage error: %v", err)
	}
}

// A binary that links mattn/go-sqlite3 but is built with CGO_ENABLED=0 gets
// the driver's stub: it compiled until CHG-202 and failed every Open at
// runtime, and since CHG-202 it does not compile. Deriving the requirement
// from the dependency graph keeps the build list honest as imports change.
func TestOramaBinaries_EverySQLiteBinaryIsBuiltWithCGO(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list over every binary")
	}
	_, file, _, _ := runtime.Caller(0)
	projectDir := filepath.Join(filepath.Dir(file), "..", "..", "..", "..")

	declareGoListInputs(t, projectDir)
	for _, bin := range oramaBinaries("") {
		cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}\t{{.Dir}}", bin.Package)
		cmd.Dir = projectDir
		cmd.Env = append(cmd.Environ(), "GOOS=linux", "CGO_ENABLED=1")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list %s: %v", bin.Package, err)
		}
		linksSQLite := false
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			importPath, dir, _ := strings.Cut(line, "\t")
			linksSQLite = linksSQLite || importPath == "github.com/mattn/go-sqlite3"
			declarePackageDir(t, projectDir, dir)
		}
		if linksSQLite && !bin.CGO {
			t.Errorf("%s links github.com/mattn/go-sqlite3 but is built without cgo", bin.Name)
		}
		if !linksSQLite && bin.CGO {
			t.Errorf("%s is built with cgo but does not need it; keep it CGO_ENABLED=0", bin.Name)
		}
	}
}

// The go command reads the import graph go list reports in a process of its
// own, so the files it read are not inputs the go test cache knows of, and
// most of these packages are not linked into this test's binary either. The
// test declares them by opening them itself: the module's go.mod and go.sum
// (they pick the dependencies' versions, and so their imports), and the
// directory of every module package a binary links (an opened directory is
// recorded with each file's size and modification time). Editing any of them
// reruns the test.
func declareGoListInputs(t *testing.T, projectDir string) {
	t.Helper()
	for _, name := range []string{"go.mod", "go.sum"} {
		if _, err := os.ReadFile(filepath.Join(projectDir, name)); err != nil {
			t.Fatal(err)
		}
	}
}

// declarePackageDir opens dir when it is a package of the module at
// projectDir; packages from the module cache are fixed by go.sum.
func declarePackageDir(t *testing.T, projectDir, dir string) {
	t.Helper()
	module, err := filepath.Abs(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dir, module+string(filepath.Separator)) {
		return
	}
	if _, err := os.ReadDir(dir); err != nil {
		t.Fatal(err)
	}
}

func TestGoBuildArgs_CGOBinaryIsStaticWithPureGoNet(t *testing.T) {
	args := goBuildArgs(oramaBinary{Name: "gateway", Package: "./cmd/gateway/", CGO: true}, "-s -w", "/out/gateway")
	joined := strings.Join(args, " ")

	for _, want := range []string{"-tags " + cgoBuildTags, "-linkmode external", "-extldflags -static", "-o /out/gateway", "./cmd/gateway/"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
	if !strings.Contains(cgoBuildTags, "netgo") || !strings.Contains(cgoBuildTags, "osusergo") {
		t.Errorf("a static cgo binary must use the pure-Go resolver and user lookup: %q", cgoBuildTags)
	}
}

func TestGoBuildArgs_PureGoBinaryHasNoCGOFlags(t *testing.T) {
	joined := strings.Join(goBuildArgs(oramaBinary{Name: "orama", Package: "./cmd/orama/"}, "-s -w", "/out/orama"), " ")
	for _, unwanted := range []string{"-tags", "-linkmode", "-extldflags"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("pure-Go build must not carry %q: %q", unwanted, joined)
		}
	}
}

func TestGoBuildArgs_theLocalrepoTagIsOnlyOnTheBinaryThatAsksForIt(t *testing.T) {
	plain := strings.Join(goBuildArgs(oramaBinary{Name: "orama", Package: "./cmd/orama/"}, "-s -w", "/out/orama"), " ")
	if strings.Contains(plain, localRepoTag) {
		t.Errorf("a release build carries the %s tag: %q", localRepoTag, plain)
	}
	tagged := strings.Join(goBuildArgs(oramaBinary{Name: "orama", Package: "./cmd/orama/", Tags: localRepoTag}, "-s -w", "/out/orama"), " ")
	if !strings.Contains(tagged, "-tags "+localRepoTag+" ") {
		t.Errorf("the tag is not on the binary that asked for it: %q", tagged)
	}
	both := strings.Join(goBuildArgs(oramaBinary{Name: "x", Package: "./x/", CGO: true, Tags: localRepoTag}, "-s -w", "/out/x"), " ")
	if !strings.Contains(both, "-tags "+cgoBuildTags+","+localRepoTag+" ") {
		t.Errorf("cgo tags and the extra tag are one -tags value: %q", both)
	}
}

func TestCGOEnv_TargetsMuslThroughZig(t *testing.T) {
	env, err := cgoEnv(nil, "arm64", "/opt/zig/bin/zig")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, want := range []string{"GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=1", "CC=/opt/zig/bin/zig cc -target aarch64-linux-musl"} {
		if !strings.Contains(joined, want) {
			t.Errorf("env missing %q:\n%s", want, joined)
		}
	}
}

func TestZigTargetFor_UnknownArchIsRefused(t *testing.T) {
	if _, err := zigTargetFor("riscv64"); err == nil {
		t.Fatal("riscv64 must be refused, not mapped to a wrong target")
	}
	if got, _ := zigTargetFor("amd64"); got != "x86_64-linux-musl" {
		t.Errorf("amd64 -> %q", got)
	}
}
