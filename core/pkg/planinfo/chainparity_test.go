package planinfo

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The chain validates an upgrade plan's URLs with chain/x/houses/types/upgrade_info.go; a node stages
// by what this package reads from the same URLs. If the two read one URL differently, a proposal can
// pass the chain's check naming one digest while the node stages a binary against another. core
// cannot import the chain, so this file keeps the two honest: the chain's test data is read here, and
// the chain's function is compared with this package's copy.
const (
	chainUpgradeInfoSource = "../../testdata/repo/chain/x/houses/types/upgrade_info.go"
	chainURLVectors        = "../../testdata/repo/chain/x/houses/types/testdata/upgrade_url_vectors.json"
)

type urlVector struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Checksum string `json:"checksum"`
}

// skipWithoutChain skips a test that compares this package with the chain's source when the checkout
// has no chain/ directory (a core-only checkout): there is nothing to compare with.
func skipWithoutChain(t testing.TB) {
	t.Helper()
	if _, err := os.Stat(filepath.FromSlash(chainUpgradeInfoSource)); err != nil {
		t.Skipf("the chain source is not in this checkout (%s): nothing to compare the upgrade-info reader with; "+
			"run this test in the full monorepo before changing planinfo or chain/x/houses/types/upgrade_info.go", chainUpgradeInfoSource)
	}
}

func readURLVectors(t testing.TB) []urlVector {
	t.Helper()
	skipWithoutChain(t)
	raw, err := os.ReadFile(filepath.FromSlash(chainURLVectors))
	if err != nil {
		t.Fatalf("read the chain's URL vectors: %v", err)
	}
	var vectors []urlVector
	if err := json.Unmarshal(raw, &vectors); err != nil || len(vectors) == 0 {
		t.Fatalf("the chain's URL vectors: %d rows, %v", len(vectors), err)
	}
	return vectors
}

func TestURLChecksum_agreesWithTheChainOnEveryVector(t *testing.T) {
	for _, v := range readURLVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			if got := urlChecksum(v.URL); got != v.Checksum {
				t.Fatalf("urlChecksum(%q) = %q, the chain reads %q", v.URL, got, v.Checksum)
			}
		})
	}
}

// TestURLChecksum_craftedURLsNameTheChainsDigest: the two URLs that fooled the regular expression this
// package used to read them with. Each carries a decoy digest (aaaa...) ahead of the real query value
// (bbbb...) that url.ParseQuery finds, which is the one the chain validated and the one cosmovisor
// downloads against.
func TestURLChecksum_craftedURLsNameTheChainsDigest(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, raw := range []string{
		"https://h/p/checksum=sha256:" + a + "&x/oramad?checksum=sha256:" + b,
		"https://h/oramad?CHECKSUM=sha256:" + a + "&checksum=sha256:" + b,
	} {
		if got := urlChecksum(raw); got != b {
			t.Errorf("urlChecksum(%q) = %q, want the query value %q", raw, got, b)
		}
	}
}

// printedDecl prints the node with every identifier renamed through rename, so two declarations
// that differ only in what the chain and this package call things print the same.
func printedDecl(t *testing.T, node ast.Node, fset *token.FileSet, rename map[string]string) string {
	t.Helper()
	ast.Inspect(node, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			if to, ok := rename[id.Name]; ok {
				id.Name = to
			}
		}
		return true
	})
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, node); err != nil {
		t.Fatalf("print: %v", err)
	}
	return buf.String()
}

// declNamed finds the function or the value spec called name in the file at path.
func declNamed(t *testing.T, path, name string, rename map[string]string) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.FromSlash(path), nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var found ast.Node
	ast.Inspect(file, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.FuncDecl:
			if d.Name.Name == name {
				found = d
			}
		case *ast.ValueSpec:
			for _, id := range d.Names {
				if id.Name == name {
					found = d
				}
			}
		}
		return found == nil
	})
	if found == nil {
		t.Fatalf("%s declares no %s", path, name)
	}
	return printedDecl(t, found, fset, rename)
}

func TestURLChecksum_isTheChainsFunction(t *testing.T) {
	skipWithoutChain(t)
	rename := map[string]string{
		"upgradeURLChecksum":    "urlChecksum",
		"upgradeChecksumParam":  "checksumParam",
		"upgradeChecksumValue":  "checksumValue",
		"UpgradeChecksumPrefix": "checksumPrefix",
	}
	pairs := [][2]string{
		{"upgradeURLChecksum", "urlChecksum"},
		{"upgradeChecksumParam", "checksumParam"},
		{"UpgradeChecksumPrefix", "checksumPrefix"},
		{"upgradeChecksumValue", "checksumValue"},
	}
	for _, p := range pairs {
		chain := declNamed(t, chainUpgradeInfoSource, p[0], rename)
		mine := declNamed(t, "planinfo.go", p[1], map[string]string{})
		if chain != mine {
			t.Errorf("%s differs from the chain's %s; copy the chain's again.\nchain:\n%s\nhere:\n%s", p[1], p[0], chain, mine)
		}
	}
}

// FuzzURLChecksum: for any URL the digest is "" or the one lowercase checksum value
// url.ParseQuery finds. With the sync test above, this holds for the chain's reader too.
func FuzzURLChecksum(f *testing.F) {
	for _, v := range readURLVectors(f) {
		f.Add(v.URL)
	}
	hex := regexp.MustCompile(`^[0-9a-f]{64}$`)
	f.Fuzz(func(t *testing.T, raw string) {
		sum := urlChecksum(raw)
		if sum == "" {
			return
		}
		if !hex.MatchString(sum) {
			t.Fatalf("urlChecksum(%q) = %q, not 64 lowercase hex digits", raw, sum)
		}
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("urlChecksum(%q) = %q for a URL that does not parse: %v", raw, sum, err)
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || len(query["checksum"]) != 1 || query["checksum"][0] != "sha256:"+sum {
			t.Fatalf("urlChecksum(%q) = %q but the query says %v (%v)", raw, sum, query["checksum"], err)
		}
	})
}
