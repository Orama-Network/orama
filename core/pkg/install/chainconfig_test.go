package install

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// cometConfig is the shape of the config.toml `oramad init` writes, cut to the
// sections the installer touches. "enable" and "pex" appear in more than one
// section on purpose: only the right one may change.
const cometConfig = `# CometBFT config
proxy_app = "tcp://127.0.0.1:26658"

[rpc]
laddr = "tcp://127.0.0.1:26657"
pex = "rpc-has-no-pex"

[p2p]
laddr = "tcp://0.0.0.0:26656"
external_address = ""
persistent_peers = ""
addr_book_strict = true
# pex = commented
pex = true

[statesync]
enable = false
rpc_servers = ""
trust_height = 0
trust_hash = ""
trust_period = "168h0m0s"

[blocksync]
enable = true

[instrumentation]
prometheus = false
prometheus_listen_addr = ":26660"
`

const sdkApp = `# app config
minimum-gas-prices = ""
pruning = "default"
pruning-keep-recent = "0"
pruning-interval = "0"
min-retain-blocks = 0
iavl-cache-size = 781250
query-gas-limit = "0"
app-db-backend = "goleveldb"

[state-sync]
snapshot-interval = 0
snapshot-keep-recent = 2

[telemetry]
enabled = false
`

func joinConfig() ChainConfig {
	return ChainConfig{
		ExternalAddress: "203.0.113.7:31000",
		StateSync: &StateSyncJoin{
			RPCServers:  []string{"https://seed1.net.example/v1/chain/light", "https://seed2.net.example/v1/chain/light"},
			TrustHeight: 123400,
			TrustHash:   strings.Repeat("AB", 32),
		},
	}
}

func TestRenderChainConfig_writesTheDeploySettings(t *testing.T) {
	cfg, app, err := RenderChainConfig(joinConfig(), cometConfig, sdkApp)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`external_address = "203.0.113.7:31000"`, "pex = false", "addr_book_strict = false",
		"prometheus = true", `prometheus_listen_addr = "127.0.0.1:31004"`,
		"enable = true\nrpc_servers = \"https://seed1.net.example/v1/chain/light,https://seed2.net.example/v1/chain/light\"",
		"trust_height = 123400", `trust_hash = "` + strings.Repeat("ab", 32) + `"`, `trust_period = "168h0m0s"`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config.toml lacks %q:\n%s", want, cfg)
		}
	}
	for _, want := range []string{
		`pruning = "custom"`, `pruning-keep-recent = "100"`, `pruning-interval = "10"`, "min-retain-blocks = 241920",
		`app-db-backend = "pebbledb"`, "iavl-cache-size = 100000", `query-gas-limit = "2000000"`,
		"snapshot-interval = 1000", "snapshot-keep-recent = 2",
	} {
		if !strings.Contains(app, want) {
			t.Errorf("app.toml lacks %q:\n%s", want, app)
		}
	}
}

func TestRenderChainConfig_leavesOtherSectionsAlone(t *testing.T) {
	cfg, _, err := RenderChainConfig(joinConfig(), cometConfig, sdkApp)
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{`pex = "rpc-has-no-pex"`, "# pex = commented", "[blocksync]\nenable = true", `persistent_peers = ""`, `laddr = "tcp://0.0.0.0:26656"`} {
		if !strings.Contains(cfg, kept) {
			t.Errorf("config.toml lost %q:\n%s", kept, cfg)
		}
	}
}

func TestRenderChainConfig_isIdempotent(t *testing.T) {
	cfg, app, err := RenderChainConfig(joinConfig(), cometConfig, sdkApp)
	if err != nil {
		t.Fatal(err)
	}
	cfg2, app2, err := RenderChainConfig(joinConfig(), cfg, app)
	if err != nil {
		t.Fatal(err)
	}
	if cfg != cfg2 || app != app2 {
		t.Fatal("rendering twice changed the files")
	}
}

func TestRenderChainConfig_servingNodeLeavesStateSyncOff(t *testing.T) {
	c := joinConfig()
	c.StateSync = nil
	cfg, _, err := RenderChainConfig(c, cometConfig, sdkApp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg, "[statesync]\nenable = false") {
		t.Fatalf("a node that is not joining must not enable state sync:\n%s", cfg)
	}
}

func TestRenderChainConfig_aMissingKeyIsAnError(t *testing.T) {
	broken := strings.Replace(sdkApp, "min-retain-blocks = 0\n", "", 1)
	_, _, err := RenderChainConfig(joinConfig(), cometConfig, broken)
	if err == nil || !strings.Contains(err.Error(), "min-retain-blocks") || !strings.Contains(err.Error(), "template changed") {
		t.Fatalf("got %v, want an error naming the missing key", err)
	}
	_, _, err = RenderChainConfig(joinConfig(), strings.Replace(cometConfig, "[statesync]", "[state_sync]", 1), sdkApp)
	if err == nil || !strings.Contains(err.Error(), "statesync") {
		t.Fatalf("got %v, want an error naming the missing section", err)
	}
}

func TestRenderChainConfig_aKeyTwiceInOneSectionIsAnError(t *testing.T) {
	doubled := strings.Replace(sdkApp, "pruning-interval = \"0\"\n", "pruning-interval = \"0\"\npruning-interval = \"5\"\n", 1)
	if _, _, err := RenderChainConfig(joinConfig(), cometConfig, doubled); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("got %v, want a duplicate-key error", err)
	}
}

func TestChainConfigValidate_refusals(t *testing.T) {
	for name, mutate := range map[string]func(*ChainConfig){
		"no external address":         func(c *ChainConfig) { c.ExternalAddress = "" },
		"a name as the host":          func(c *ChainConfig) { c.ExternalAddress = "seed.example:31000" },
		"the rpc port":                func(c *ChainConfig) { c.ExternalAddress = "203.0.113.7:31001" },
		"one state-sync server":       func(c *ChainConfig) { c.StateSync.RPCServers = c.StateSync.RPCServers[:1] },
		"two servers on one host":     func(c *ChainConfig) { c.StateSync.RPCServers[1] = "https://seed1.net.example/other" },
		"http server":                 func(c *ChainConfig) { c.StateSync.RPCServers[0] = "http://seed1.net.example/v1/chain/light" },
		"a quote in the server":       func(c *ChainConfig) { c.StateSync.RPCServers[0] = `https://seed1.net.example/"x` },
		"trust height zero":           func(c *ChainConfig) { c.StateSync.TrustHeight = 0 },
		"a short trust hash":          func(c *ChainConfig) { c.StateSync.TrustHash = "abcd" },
		"a trust hash that is no hex": func(c *ChainConfig) { c.StateSync.TrustHash = strings.Repeat("zz", 32) },
	} {
		c := joinConfig()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: want an error, got none", name)
		}
	}
}

func TestInstallGlobal_writesTheChainConfigOwnedByTheChainAccount(t *testing.T) {
	f := newGlobalFixture(t)
	dir := filepath.Join(f.host.ChainHome, "config")
	for name, body := range map[string]string{"config.toml": cometConfig, "app.toml": sdkApp} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	opts := f.options(GlobalServiceChain)
	c := joinConfig()
	opts.ChainConfig = &c
	if err := InstallGlobal(opts, f.host); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil || !strings.Contains(string(got), `external_address = "203.0.113.7:31000"`) {
		t.Fatalf("config.toml = %q, %v", got, err)
	}
	for _, name := range []string{"config.toml", "app.toml"} {
		want := chownCall{filepath.Join(dir, name), 990, 991}
		found := false
		for _, c := range f.chowns {
			found = found || c == want
		}
		if !found {
			t.Errorf("%s was not handed to the chain account (chowns: %v)", name, f.chowns)
		}
	}
}

func TestInstallGlobal_chainConfigWithoutAnInitialisedHomeNamesTheFlag(t *testing.T) {
	f := newGlobalFixture(t)
	opts := f.options(GlobalServiceChain)
	c := joinConfig()
	opts.ChainConfig = &c
	err := InstallGlobal(opts, f.host)
	if err == nil || !strings.Contains(err.Error(), "--init-chain") {
		t.Fatalf("got %v, want an error that says the chain home needs --init-chain", err)
	}
}

func TestInstallGlobal_chainConfigNeedsTheChainService(t *testing.T) {
	f := newGlobalFixture(t)
	opts := f.options(GlobalServiceArchiver)
	c := joinConfig()
	opts.ChainConfig = &c
	if err := InstallGlobal(opts, f.host); err == nil || !strings.Contains(err.Error(), "chain") {
		t.Fatalf("got %v, want a refusal", err)
	}
}

// TestChainMinRetainBlocks_isFourteenDaysOfTheChainsBlocks: the installer's
// min-retain-blocks must equal x/archive's 14-day window, which the chain counts
// at params.BlockIntervalSeconds. The chain module is a separate Go module, so
// the interval is read from its source.
func TestChainMinRetainBlocks_isFourteenDaysOfTheChainsBlocks(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "repo", "chain", "app", "params", "params.go"))
	if err != nil {
		t.Fatalf("read the chain's params: %v", err)
	}
	m := regexp.MustCompile(`(?m)^\s*BlockIntervalSeconds = (\d+)\s*$`).FindSubmatch(src)
	if m == nil {
		t.Fatal("chain/app/params/params.go no longer declares BlockIntervalSeconds: update chainMinRetainBlocks")
	}
	interval, err := strconv.Atoi(string(m[1]))
	if err != nil || interval <= 0 {
		t.Fatalf("BlockIntervalSeconds = %q is not a positive integer", m[1])
	}
	const retentionDays, secondsPerDay = 14, 24 * 60 * 60
	if want := retentionDays * secondsPerDay / interval; chainMinRetainBlocks != want {
		t.Errorf("chainMinRetainBlocks = %d, want %d (14 days of %d s blocks)", chainMinRetainBlocks, want, interval)
	}
}
