package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"

	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/server"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"

	"github.com/Orama-Network/orama/chain/app"
	"github.com/Orama-Network/orama/chain/app/params"
)

// A node that serves the public chain query route must not run a query for unbounded gas: the
// app.toml oramad init writes carries a query-gas-limit.
func TestInitAppConfig_setsAQueryGasLimit(t *testing.T) {
	tmpl, cfg := initAppConfig()
	srvCfg, ok := cfg.(*serverconfig.Config)
	if !ok {
		t.Fatalf("app config is %T, want server config", cfg)
	}
	if srvCfg.QueryGasLimit == 0 || srvCfg.QueryGasLimit != defaultQueryGasLimit {
		t.Fatalf("QueryGasLimit = %d, want %d", srvCfg.QueryGasLimit, defaultQueryGasLimit)
	}
	tpl, err := template.New("app.toml").Parse(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tpl.Execute(&out, srvCfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "query-gas-limit = \"2000000\"") && !strings.Contains(out.String(), "query-gas-limit = 2000000") {
		t.Fatalf("the rendered app.toml carries no query-gas-limit of %d:\n%s", defaultQueryGasLimit, grep(out.String(), "query-gas-limit"))
	}
}

func grep(s, needle string) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, needle) {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

func TestCheckQueryGasLimit_refusesUnboundedOffLocalnet(t *testing.T) {
	cases := []struct {
		name    string
		chainID string
		limit   uint64
		refused bool
	}{
		{"bounded public chain", "orama-1", 2_000_000, false},
		{"unbounded public chain", "orama-1", 0, true},
		{"unbounded devnet", "orama-devnet-1", 0, true},
		{"unbounded testnet", "orama-testnet-1", 0, true},
		{"unbounded localnet", "orama-localnet-1", 0, false},
		{"bounded localnet", "orama-localnet-1", 5, false},
	}
	for _, tc := range cases {
		err := checkQueryGasLimit(tc.chainID, tc.limit)
		if (err != nil) != tc.refused {
			t.Errorf("%s: err = %v, want refused = %v", tc.name, err, tc.refused)
		}
		if err != nil && !strings.Contains(err.Error(), "query-gas-limit") {
			t.Errorf("%s: the error does not name the setting to fix: %v", tc.name, err)
		}
	}
}

// oramad start must not reach the node when the query gas limit is unbounded off localnet, or when a
// node of a public network cannot verify shielded bundles; it must reach it otherwise.
func TestGuardStart_refusesUnsafeNodesBeforeStart(t *testing.T) {
	run := func(chainID string, limit uint64) (called bool, err error) {
		v := viper.New()
		v.Set(flags.FlagChainID, chainID)
		v.Set(server.FlagQueryGasLimit, limit)
		v.Set(flags.FlagHome, t.TempDir())
		start := &cobra.Command{Use: "start", RunE: func(*cobra.Command, []string) error { called = true; return nil }}
		root := &cobra.Command{Use: "oramad"}
		root.AddCommand(start)
		guardStart(root)
		ctx := server.NewDefaultContext()
		ctx.Viper = v
		start.SetContext(context.WithValue(context.Background(), server.ServerContextKey, ctx))
		err = start.RunE(start, nil)
		return called, err
	}
	if called, err := run("orama-1", 0); err == nil || called {
		t.Fatalf("an unbounded public node started: called=%v err=%v", called, err)
	}
	if called, err := run("orama-devnet-1", 2_000_000); err != nil || !called {
		t.Fatalf("a bounded devnet node was refused: called=%v err=%v", called, err)
	}
	if called, err := run("orama-localnet-1", 0); err != nil || !called {
		t.Fatalf("a localnet was refused: called=%v err=%v", called, err)
	}
	called, err := run("orama-testnet-1", 2_000_000)
	if err == nil || called || !strings.Contains(err.Error(), app.ShieldedVerifierBinary) {
		t.Fatalf("a testnet node with no shielded verifier started: called=%v err=%v", called, err)
	}
}

func TestStartChainID_fallsBackToTheGenesisFile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config", "genesis.json"), []byte(`{"chain_id":"orama-devnet-9","app_state":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	v := viper.New()
	v.Set(flags.FlagHome, home)
	got, err := startChainID(v)
	if err != nil || got != "orama-devnet-9" {
		t.Fatalf("startChainID = %q, %v; want the genesis chain id", got, err)
	}
	v.Set(flags.FlagChainID, "override-1")
	if got, err := startChainID(v); err != nil || got != "override-1" {
		t.Fatalf("startChainID = %q, %v; want the chain-id setting", got, err)
	}
	if _, err := startChainID(viper.New()); err == nil {
		t.Fatal("a node with no genesis file and no chain-id must not resolve one")
	}
}

// The CometBFT RPC is reachable by every account allowed to reach the chain's host-only ports, so its
// unsafe routes (dial_seeds, dial_peers, unsafe_flush_mempool) must stay off in the config oramad
// init renders. Nothing in oramad's defaults or in the chain unit turns them on.
func TestInitCometBFTConfig_rpcUnsafeIsOff(t *testing.T) {
	cfg := initCometBFTConfig()
	if cfg.RPC.Unsafe {
		t.Fatal("the CometBFT RPC's unsafe routes are on in oramad's default config")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	cmtcfg.WriteConfigFile(path, cfg)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "unsafe = false") || strings.Contains(string(body), "unsafe = true") {
		t.Fatalf("the rendered config.toml does not keep rpc unsafe off:\n%s", body)
	}
}

// The IAVL cache oramad init writes is sized for a host that shares its memory: the SDK default
// took a 4 GB stagenet node to 98% in a day.
func TestInitAppConfig_sizesTheIAVLCacheForASharedHost(t *testing.T) {
	tmpl, cfg := initAppConfig()
	srvCfg := cfg.(*serverconfig.Config)
	if srvCfg.IAVLCacheSize != defaultIAVLCacheSize || defaultIAVLCacheSize >= serverconfig.DefaultConfig().IAVLCacheSize {
		t.Fatalf("IAVLCacheSize = %d, want %d, below the SDK's %d", srvCfg.IAVLCacheSize, defaultIAVLCacheSize, serverconfig.DefaultConfig().IAVLCacheSize)
	}
	tpl, err := template.New("app.toml").Parse(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tpl.Execute(&out, srvCfg); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("iavl-cache-size = %d", defaultIAVLCacheSize); !strings.Contains(out.String(), want) {
		t.Fatalf("the rendered app.toml carries no %q:\n%s", want, grep(out.String(), "iavl-cache-size"))
	}
}

// `orama global install --external-address` writes the same values into an app.toml that predates
// them (core/pkg/install/chainconfig.go); a value changed in one place only would leave existing
// nodes on the old one.
func TestGlobalInstall_setsTheValuesOramadInitWrites(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "repo", "core", "pkg", "install", "chainconfig.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for _, want := range []string{
		fmt.Sprintf("chainQueryGasLimit = %d\n", defaultQueryGasLimit),
		fmt.Sprintf("chainIAVLCacheSize = %d\n", defaultIAVLCacheSize),
	} {
		// gofmt aligns the constants of a block, so compare on the spaced-out form.
		if !strings.Contains(strings.Join(strings.Fields(src), " "), strings.Join(strings.Fields(want), " ")) {
			t.Errorf("core/pkg/install/chainconfig.go does not set %q", strings.TrimSpace(want))
		}
	}
}

// The block interval every module counts its time windows in is the timeout_commit oramad writes,
// not a default of the SDK or CometBFT that could change under it.
func TestInitCometBFTConfig_writesTheChainsBlockInterval(t *testing.T) {
	cfg := initCometBFTConfig()
	if want := params.BlockIntervalSeconds * time.Second; cfg.Consensus.TimeoutCommit != want {
		t.Fatalf("timeout_commit = %s, want %s (params.BlockIntervalSeconds)", cfg.Consensus.TimeoutCommit, want)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	cmtcfg.WriteConfigFile(path, cfg)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `timeout_commit = "5s"`) {
		t.Fatalf("the rendered config.toml does not set timeout_commit to 5s:\n%s", body)
	}
}
