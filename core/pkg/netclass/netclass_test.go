package netclass

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestIsProduction_classifiesByMarker(t *testing.T) {
	cases := map[string]bool{
		"orama-testnet-6": false, "orama-devnet-1": false, "orama-localnet-3": false,
		"orama-1": true, "orama-mainnet": true, "testnet": true, "orama-testnet": true, "": true,
		"orama-pubnet-testnet-1": false, "orama-x-devnet-1": false, "orama-devnet-e2e-ab12cd": false,
		"orama-1-testnet-x": true, "orama-1-devnet-x": true, "mainnet-localnet-1": true, "orama-testnet-": true,
		"orama-a-b-testnet-1": true, "orama-stagenet-11": true, "orama-x-stagenet-1": true,
	}
	for id, want := range cases {
		if got := IsProduction(id); got != want {
			t.Errorf("IsProduction(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestIsLocalnet(t *testing.T) {
	for id, want := range map[string]bool{
		"orama-localnet-1": true, "orama-x-localnet-1": true,
		"orama-testnet-1": false, "orama-1-localnet-x": false, "": false,
	} {
		if got := IsLocalnet(id); got != want {
			t.Errorf("IsLocalnet(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestIsDevnet(t *testing.T) {
	for id, want := range map[string]bool{
		"orama-devnet-e2e-ab12": true, "orama-devnet-9": true, "orama-x-devnet-1": true,
		"orama-testnet-4": false, "orama-localnet-1": false, "orama-1-devnet-x": false,
		"foo-devnet-bar": false, "orama-devnet-localnet-1": false, "": false,
	} {
		if got := IsDevnet(id); got != want {
			t.Errorf("IsDevnet(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestValidChainID(t *testing.T) {
	for id, want := range map[string]bool{
		"orama-testnet-5": true, "a": true, "orama-1": true, strings.Repeat("a", MaxChainIDLen): true,
		"": false, strings.Repeat("a", MaxChainIDLen+1): false, "a b": false, "a\x1b": false, "é": false,
		"Orama-1": false, "orama_1": false, "orama.1": false, "-orama": false, "orama-": false, "orama-1\n": false,
	} {
		if got := ValidChainID(id); got != want {
			t.Errorf("ValidChainID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestIsTestNetwork_isTheOppositeOfProduction(t *testing.T) {
	for _, id := range []string{"orama-testnet-6", "orama-devnet-1", "orama-localnet-2", "orama-1", "orama-stagenet-3", "orama-testnet", ""} {
		if IsTestNetwork(id) == IsProduction(id) {
			t.Errorf("%q is both or neither", id)
		}
	}
}

// The suggested id is a test network's for any name, including the public testnet's own.
func TestExampleTestChainID_isATestNetworksID(t *testing.T) {
	for name, want := range map[string]string{"testnet": "orama-testnet-1", "devnet": "orama-devnet-1", "pubnet": "orama-pubnet-testnet-1"} {
		got := ExampleTestChainID(name)
		if got != want || IsProduction(got) {
			t.Errorf("ExampleTestChainID(%q) = %q (production %v), want the test id %q", name, got, IsProduction(got), want)
		}
	}
}

func TestCheckCommittee_productionFloor(t *testing.T) {
	if err := CheckCommittee("orama-1", ProductionMinCommittee-1); err == nil {
		t.Fatal("a production chain id with 29 validators was accepted")
	} else if !strings.Contains(err.Error(), "testnet") || !strings.Contains(err.Error(), "30") {
		t.Errorf("the refusal does not say what to do instead: %v", err)
	}
	if err := CheckCommittee("orama-1", ProductionMinCommittee); err != nil {
		t.Errorf("a production chain id with 30 validators was refused: %v", err)
	}
}

func TestCheckCommittee_testNetworkAnySize(t *testing.T) {
	for _, n := range []int{1, 5, 29} {
		if err := CheckCommittee("orama-testnet-1", n); err != nil {
			t.Errorf("a testnet with %d validators was refused: %v", n, err)
		}
	}
}

func TestCheckCommittee_empty(t *testing.T) {
	if err := CheckCommittee("orama-testnet-1", 0); err == nil {
		t.Fatal("an empty committee was accepted")
	}
}

// chainFile reads a file of the chain module, which sits beside core/.
func chainFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile("../../testdata/repo/chain/" + rel)
	if err != nil {
		t.Skipf("the chain sources are not reachable from here: %v", err)
	}
	return string(data)
}

func sourceOf(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A class word is never the optional name word: "orama-devnet-localnet-1" would otherwise be a
// devnet called localnet and a localnet called devnet at once.
func TestIsProduction_classWordAsNameIsProduction(t *testing.T) {
	for _, id := range []string{
		"orama-devnet-localnet-1", "orama-localnet-devnet-1", "orama-testnet-testnet-1",
		"orama-devnet-devnet-1", "orama-testnet-localnet-x", "orama-localnet-testnet-e2e",
	} {
		if !IsProduction(id) || IsLocalnet(id) {
			t.Errorf("%q must be production (a class word as the name), IsProduction=%v IsLocalnet=%v", id, IsProduction(id), IsLocalnet(id))
		}
	}
	for _, id := range []string{"orama-devnet-e2e-1", "orama-localnet-devnet2-1", "orama-pubnet-localnet-1"} {
		if IsProduction(id) {
			t.Errorf("%q must be a test network", id)
		}
	}
}

func TestNetclass_matchesChain(t *testing.T) {
	pattern := regexp.MustCompile("testChainIDPattern = regexp.MustCompile\\(`([^`]*)`\\)")
	m := pattern.FindStringSubmatch(chainFile(t, "app/params/chainid.go"))
	if m == nil {
		t.Fatal("chain/app/params/chainid.go no longer declares testChainIDPattern: update pkg/netclass")
	}
	if m[1] != testChainIDPattern.String() {
		t.Errorf("the chain's test chain id pattern is %s, pkg/netclass has %s", m[1], testChainIDPattern)
	}
	words := regexp.MustCompile(`classWords = \[\]string\{([^}]*)\}`).FindStringSubmatch(chainFile(t, "app/params/chainid.go"))
	if words == nil {
		t.Fatal("chain/app/params/chainid.go no longer declares classWords: update pkg/netclass")
	}
	own := regexp.MustCompile(`classWords = \[\]string\{([^}]*)\}`).FindStringSubmatch(sourceOf(t, "netclass.go"))
	if own == nil || own[1] != words[1] {
		t.Errorf("the chain's class words are {%s}, pkg/netclass has %v", words[1], own)
	}
	form := regexp.MustCompile(`TestChainIDForm = "([^"]*)"`).FindStringSubmatch(chainFile(t, "app/params/chainid.go"))
	if form == nil || form[1] != TestChainIDForm {
		t.Errorf("the chain's TestChainIDForm is %v, pkg/netclass has %q", form, TestChainIDForm)
	}
	floor := regexp.MustCompile(`ProductionMinCommitteeSize uint64 = (\d+)`).FindStringSubmatch(chainFile(t, "x/power/types/params.go"))
	if floor == nil || floor[1] != strconv.Itoa(ProductionMinCommittee) {
		t.Errorf("the chain's production committee floor is %v, pkg/netclass has %d", floor, ProductionMinCommittee)
	}
}
