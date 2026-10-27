package constants_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/Orama-Network/orama/core/pkg/constants"
)

func TestLocalChainRPCURL_loopbackRPCPort(t *testing.T) {
	if got, want := constants.LocalChainRPCURL(), "http://127.0.0.1:31001"; got != want {
		t.Errorf("LocalChainRPCURL() = %q, want %q", got, want)
	}
	if got, want := constants.LocalChainAPIURL(), "http://127.0.0.1:31003"; got != want {
		t.Errorf("LocalChainAPIURL() = %q, want %q", got, want)
	}
}

func TestTestnetDeployScript_usesTheNamespaceAddress(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "repo", "chain", "scripts", "testnet", "deploy.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^NS_ADDR="` + regexp.QuoteMeta(constants.GlobalNetnsAddr) + `"$`).Match(data) {
		t.Errorf("deploy.sh NS_ADDR is not %s", constants.GlobalNetnsAddr)
	}
}

// The chain module cannot import core, so provider/kubo.go repeats the namespace address it
// trusts for the bearer token. A change on either side must fail here.
func TestProviderKubo_colocatedHostIsTheNamespaceAddress(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "repo", "chain", "provider", "kubo.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^const colocatedKuboHost = "([^"]*)"$`).FindSubmatch(data)
	if m == nil {
		t.Fatal("chain/provider/kubo.go has no colocatedKuboHost constant")
	}
	if got := string(m[1]); got != constants.GlobalNetnsAddr {
		t.Errorf("colocatedKuboHost is %q, constants.GlobalNetnsAddr is %q", got, constants.GlobalNetnsAddr)
	}
}

func TestColocatedChainURLs_areTheNamespaceAddress(t *testing.T) {
	for got, want := range map[string]string{
		constants.ColocatedChainRPCURL():      "http://198.18.0.2:31001",
		constants.ColocatedChainAPIURL():      "http://198.18.0.2:31003",
		constants.ColocatedGlobalIndexerURL(): "http://198.18.0.2:31015",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// The chain block must not overlap the index or tenant blocks, whose
// allocators hand ports out without knowing about the chain.
func TestChainPorts_outsideIndexAndTenantBlocks(t *testing.T) {
	for _, p := range []int{constants.ChainP2PPort, constants.ChainRPCPort, constants.ChainGRPCPort,
		constants.ChainAPIPort, constants.ChainPrometheusPort} {
		if p >= 10000 && p <= constants.IndexPortEnd {
			t.Errorf("chain port %d lies in the tenant/index range", p)
		}
	}
}

// reset-node.sh puts net.ipv4.ip_forward back from the record the installer writes; the two must
// name the same file.
func TestTestnetResetScript_restoresForwardingFromTheInstallersRecord(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "repo", "chain", "scripts", "testnet", "remote", "reset-node.sh"))
	if err != nil {
		t.Fatal(err)
	}
	want := `PRIOR_FORWARD=` + constants.GlobalStateRoot + `/` + constants.GlobalNetnsPriorForwardFile
	if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(want) + `$`).Match(data) {
		t.Errorf("reset-node.sh does not read %s", want)
	}
	if !regexp.MustCompile(`sysctl -q -w "net\.ipv4\.ip_forward=\$prior"`).Match(data) {
		t.Errorf("reset-node.sh does not restore net.ipv4.ip_forward from the record")
	}
}
