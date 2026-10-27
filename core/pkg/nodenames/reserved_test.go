package nodenames

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

const chainNameFile = "../../testdata/repo/chain/x/nodes/types/name.go"

// TestReservedNames_matchTheChain reads the chain's source: the list here must be the chain's list,
// and the length bounds and the numbered families must be the chain's too.
func TestReservedNames_matchTheChain(t *testing.T) {
	src, err := os.ReadFile(chainNameFile)
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)var ReservedNames = map\[string\]struct\{\}\{(.*?)\n\}`).FindSubmatch(src)
	if block == nil {
		t.Fatalf("%s has no ReservedNames map: update this test with the chain's new shape", chainNameFile)
	}
	chain := map[string]bool{}
	for _, m := range regexp.MustCompile(`"([a-z0-9-]+)": \{\}`).FindAllSubmatch(block[1], -1) {
		chain[string(m[1])] = true
	}
	if len(chain) == 0 {
		t.Fatal("read no reserved names from the chain")
	}
	for name := range chain {
		if !reservedNames[name] {
			t.Errorf("the chain reserves %q and nodenames does not: add it to reservedNames", name)
		}
	}
	for name := range reservedNames {
		if !chain[name] {
			t.Errorf("nodenames reserves %q and the chain does not: remove it, or the chain's list changed", name)
		}
	}
}

func TestChainNameRules_matchTheBoundsHere(t *testing.T) {
	src, err := os.ReadFile(chainNameFile)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]int{"MinNameLen": MinNameLen, "MaxNameLen": MaxNameLen} {
		m := regexp.MustCompile(name + `\s*=\s*(\d+)`).FindSubmatch(src)
		if m == nil {
			t.Fatalf("%s no longer defines %s", chainNameFile, name)
		}
		if got, _ := strconv.Atoi(string(m[1])); got != want {
			t.Errorf("%s is %d on the chain and %d here", name, got, want)
		}
	}
	if m := regexp.MustCompile("numberedReserved\\s*=\\s*regexp.MustCompile\\(`([^`]*)`\\)").FindSubmatch(src); m == nil || string(m[1]) != zoneInfra.String() {
		t.Errorf("the chain's numbered reserved families are %q, here %q", m, zoneInfra.String())
	}
}

func TestValidateName_refusesEveryReservedName(t *testing.T) {
	for name := range reservedNames {
		if len(name) < MinNameLen {
			continue
		}
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) accepted a reserved name", name)
		}
	}
}
