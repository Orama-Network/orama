package chainfaucet

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestIsTestNetwork(t *testing.T) {
	for chainID, want := range map[string]bool{
		"orama-testnet-6":  true,
		"orama-devnet-1":   true,
		"orama-localnet-2": true,
		"orama-1":          false,
		"orama-stagenet-3": false,
		"orama-mainnet-1":  false,
		"testnet":          false,
		"orama-testnet":    false,
		"":                 false,
		"orama-staging-1":  false,
	} {
		if got := IsTestNetwork(chainID); got != want {
			t.Errorf("IsTestNetwork(%q) = %v, want %v", chainID, got, want)
		}
	}
}

const (
	chainErrorsFile = "../../testdata/repo/chain/x/emission/types/errors.go"
)

// classify finds the chain's refusals by their text, so the texts here must be the chain's.
func TestChainReasons_matchTheChain(t *testing.T) {
	src, err := os.ReadFile(chainErrorsFile)
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	for _, m := range regexp.MustCompile(`errors\.Register\(ModuleName, \d+, "([^"]+)"\)`).FindAllSubmatch(src, -1) {
		registered[string(m[1])] = true
	}
	if len(registered) == 0 {
		t.Fatalf("read no registered errors from %s", chainErrorsFile)
	}
	for _, r := range chainReasons {
		found := false
		for text := range registered {
			found = found || strings.HasPrefix(text, r.text)
		}
		if !found {
			t.Errorf("the faucet looks for %q and the chain registers no such error", r.text)
		}
	}
	for text := range registered {
		covered := false
		for _, r := range chainReasons {
			covered = covered || strings.HasPrefix(text, r.text)
		}
		if !covered {
			t.Errorf("the chain registers the faucet refusal %q and the faucet does not classify it", text)
		}
	}
}
