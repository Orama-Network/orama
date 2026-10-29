package install

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Orama-Network/orama/core/pkg/constants"
)

func TestGlobalUnits_usersPortsAndNoClusterSecret(t *testing.T) {
	chain := RenderGlobalChainUnit("")
	ipfs := RenderGlobalIPFSUnit()
	relay := RenderGlobalTorRelayUnit()

	users := []string{}
	for _, unit := range []string{chain, ipfs, relay} {
		user := mustDirective(t, unit, "User")
		group := mustDirective(t, unit, "Group")
		// The public Kubo's group is the RPC group, so the provider can read its token.
		if user != group && !(user == globalIPFSUser && group == globalIPFSRPCGroup) {
			t.Errorf("User=%s Group=%s", user, group)
		}
		if user == "orama" || user == "" {
			t.Errorf("global unit runs as %q", user)
		}
		users = append(users, user)
		if strings.Contains(unit, "cluster-secret") || strings.Contains(unit, "/opt/orama/.orama/secrets") {
			t.Errorf("unit contains a cluster secret path:\n%s", unit)
		}
		if strings.Contains(unit, "PartOf=") {
			t.Errorf("global unit is PartOf another unit:\n%s", unit)
		}
		for _, port := range []string{":10100", ":10102", ":10103", ":10104", ":10107"} {
			if strings.Contains(unit, port) {
				t.Errorf("unit contains cluster port %s", port)
			}
		}
	}
	if users[0] == users[1] || users[1] == users[2] || users[0] == users[2] {
		t.Fatalf("global units share users %v", users)
	}
	if users[0] != globalChainUser || users[1] != globalIPFSUser || users[2] != globalTorRelayUser {
		t.Fatalf("users = %v, want %s %s %s", users, globalChainUser, globalIPFSUser, globalTorRelayUser)
	}

	for _, p := range []int{
		constants.ChainP2PPort, constants.ChainRPCPort, constants.ChainGRPCPort,
		constants.ChainAPIPort, constants.ChainPrometheusPort,
	} {
		if !strings.Contains(chain, strconv.Itoa(p)) {
			t.Errorf("chain unit missing port %d\n%s", p, chain)
		}
	}
	for _, want := range []string{
		"tcp://0.0.0.0:31000",
		"tcp://127.0.0.1:31001",
		"127.0.0.1:31002",
		// bugboard 2939: the REST API is off in a fresh app.toml; the unit turns it on and names its address.
		"--api.enable=true --api.address tcp://127.0.0.1:31003",
		"127.0.0.1:31004",
	} {
		if !strings.Contains(chain, want) {
			t.Errorf("chain unit missing %s", want)
		}
	}
	// The unit only runs the daemon on the repo; the installer's config carries the loopback API.
	if exec := mustDirective(t, ipfs, "ExecStart"); exec != "/usr/lib/orama-global/bin/ipfs daemon --repo-dir="+constants.GlobalIPFSHome {
		t.Errorf("ipfs ExecStart = %q", exec)
	}
	if constants.GlobalIPFSAPIPort != 31011 {
		t.Fatalf("GlobalIPFSAPIPort = %d", constants.GlobalIPFSAPIPort)
	}
}

func TestGlobalChainUnit_userMatchesTestnetDeploy(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller")
	}
	script := filepath.Join(filepath.Dir(file), "..", "..", "testdata", "repo", "chain", "scripts", "testnet", "deploy.sh")
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("read %s: %v", script, err)
	}
	m := regexp.MustCompile(`(?m)^SVC_USER="([^"]+)"$`).FindSubmatch(data)
	if m == nil {
		t.Fatalf("%s does not set SVC_USER", script)
	}
	if string(m[1]) != globalChainUser {
		t.Fatalf("deploy.sh user %q, template user %q", m[1], globalChainUser)
	}
	unit := RenderGlobalChainUnit("")
	if !strings.Contains(unit, "User="+globalChainUser) {
		t.Fatalf("chain unit user is not %s", globalChainUser)
	}
}

func TestGlobalUnits_hideTheClusterTreeAndDenyPrivateNets(t *testing.T) {
	units := map[string]string{
		"chain":    RenderGlobalChainUnit(""),
		"ipfs":     RenderGlobalIPFSUnit(),
		"gc":       RenderGlobalIPFSGCUnit("127.0.0.1"),
		"provider": RenderGlobalProviderUnit("127.0.0.1"),
		"tor":      RenderGlobalTorRelayUnit(),
		"dirauth":  RenderGlobalTorDirauthUnit(),
		"onion":    RenderGlobalTorOnionUnit(),
		"txgate":   RenderGlobalTxGateUnit(),
		"archive":  RenderGlobalTorArchiveUnit(false),
		"monitor":  RenderGlobalTorMonitorUnit(),
		"reporter": RenderGlobalReporterUnit(),
		"archiver": RenderGlobalArchiverUnit(),
		"repair":   RenderGlobalRepairUnit(),
		"indexer":  RenderGlobalIndexerUnit(),
	}
	deny := "IPAddressDeny=10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 169.254.0.0/16 100.64.0.0/10 fc00::/7 fe80::/10"
	for name, unit := range units {
		if strings.Contains(unit, "PartOf=") {
			t.Errorf("%s is PartOf another unit", name)
		}
		if !strings.Contains(unit, "TemporaryFileSystem=/opt/orama:ro") {
			t.Errorf("%s does not hide /opt/orama", name)
		}
		if !strings.Contains(unit, "InaccessiblePaths=/var/lib/orama-unit-env /etc/wireguard /etc/orama") {
			t.Errorf("%s can still see cluster paths", name)
		}
		if !strings.Contains(unit, deny) {
			t.Errorf("%s does not deny private ranges", name)
		}
		if !strings.Contains(unit, "PrivatePIDs=yes") {
			t.Errorf("%s does not set PrivatePIDs", name)
		}
		if !strings.Contains(unit, "CapabilityBoundingSet=") {
			t.Errorf("%s keeps capabilities", name)
		}
		exec := mustDirective(t, unit, "ExecStart")
		if !strings.HasPrefix(exec, "/usr/lib/orama-global/bin/") && !strings.HasPrefix(exec, "/usr/bin/") {
			t.Errorf("%s ExecStart %q is not a global or distro binary", name, exec)
		}
		if strings.Contains(exec, "/opt/orama") {
			t.Errorf("%s runs a binary under /opt/orama, which the tmpfs hides", name)
		}
	}
	if !strings.Contains(RenderGlobalTorRelayUnit(), "MemoryDenyWriteExecute=yes") {
		t.Error("tor relay does not set MemoryDenyWriteExecute")
	}
	if strings.Contains(RenderGlobalChainUnit(""), "MemoryDenyWriteExecute=yes") {
		t.Error("the Go chain unit sets MemoryDenyWriteExecute; the runtime cannot start under it")
	}
	provider := RenderGlobalProviderUnit("127.0.0.1")
	if !strings.Contains(provider, "SupplementaryGroups=orama-ipfs-pub-rpc") {
		t.Error("provider cannot read the Kubo RPC token group")
	}
	if strings.Contains(RenderGlobalArchiverUnit(), "SupplementaryGroups=") {
		t.Error("archiver reads blocks over RPC and needs no group on the chain home")
	}
	timer := RenderGlobalIPFSGCTimer()
	if strings.Contains(timer, "PartOf=") || !strings.Contains(timer, "orama-global-ipfs-gc.service") {
		t.Fatalf("gc timer:\n%s", timer)
	}
}

func mustDirective(t *testing.T, unit, key string) string {
	t.Helper()
	var found string
	n := 0
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, key+"=") {
			found = strings.TrimPrefix(line, key+"=")
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%s appears %d times in\n%s", key, n, unit)
	}
	return found
}

func TestGlobalIndexerUnit_ownUserLoopbackAPIAndOwnHome(t *testing.T) {
	unit := RenderGlobalIndexerUnit()
	if user := mustDirective(t, unit, "User"); user != "orama-indexer" || mustDirective(t, unit, "Group") != user {
		t.Fatalf("indexer runs as %q", user)
	}
	for _, other := range []string{globalChainUser, globalArchiverUser, globalProviderUser, globalRepairUser, globalTorRelayUser, globalIPFSUser} {
		if other == globalIndexerUser {
			t.Fatalf("indexer shares user %s", other)
		}
	}
	exec := mustDirective(t, unit, "ExecStart")
	want := "/usr/lib/orama-global/bin/orama-global indexer --rpc tcp://127.0.0.1:31001 --home /var/lib/orama-global/indexer --listen 127.0.0.1:31015"
	if exec != want {
		t.Fatalf("ExecStart = %q, want %q", exec, want)
	}
	if mustDirective(t, unit, "StateDirectory") != "orama-global/indexer" ||
		mustDirective(t, unit, "ReadWritePaths") != constants.GlobalIndexerHome {
		t.Fatalf("indexer state is not its own home:\n%s", unit)
	}
	if strings.Contains(unit, "SupplementaryGroups=") || strings.Contains(unit, constants.ChainHome) {
		t.Fatalf("indexer can reach the chain home or another group:\n%s", unit)
	}
}

func TestGlobalIndexerUnit_cpuIsBoundedBelowTheChain(t *testing.T) {
	unit := RenderGlobalIndexerUnit()
	quota := mustDirective(t, unit, "CPUQuota")
	if quota != globalIndexerCPUQuota || !strings.HasSuffix(quota, "%") {
		t.Fatalf("CPUQuota = %q, want %q", quota, globalIndexerCPUQuota)
	}
	pct, err := strconv.Atoi(strings.TrimSuffix(quota, "%"))
	if err != nil || pct <= 0 || pct > 100 {
		t.Fatalf("CPUQuota %q is not between 1%% and one core (100%%) of the 4 vCPU a chain host has", quota)
	}
	nice, err := strconv.Atoi(mustDirective(t, unit, "Nice"))
	if err != nil || nice != globalIndexerNice || nice <= 0 {
		t.Fatalf("Nice = %q, want %d, a lower priority than the chain's", mustDirective(t, unit, "Nice"), globalIndexerNice)
	}
	if !strings.Contains(unit, "CPUQuota="+quota+"\nNice="+strconv.Itoa(nice)+"\nReadWritePaths=") {
		t.Fatalf("the limits are not in the [Service] section:\n%s", unit)
	}
	chain := RenderGlobalChainUnit("")
	if strings.Contains(chain, "CPUQuota=") || strings.Contains(chain, "Nice=") {
		t.Fatalf("the chain unit must keep the host's default priority:\n%s", chain)
	}
}

func TestGlobalChainUnit_runsOramadUnderCosmovisorWithoutDownloads(t *testing.T) {
	unit := RenderGlobalChainUnit("")
	exec := mustDirective(t, unit, "ExecStart")
	if !strings.HasPrefix(exec, "/usr/lib/orama-global/bin/cosmovisor run start --home "+constants.ChainHome+" ") {
		t.Fatalf("ExecStart %q does not run oramad through cosmovisor", exec)
	}
	for _, want := range []string{
		"Environment=DAEMON_NAME=oramad\n",
		"Environment=DAEMON_HOME=" + constants.ChainHome + "\n",
		"Environment=DAEMON_ALLOW_DOWNLOAD_BINARIES=false\n",
		"Environment=DAEMON_RESTART_AFTER_UPGRADE=true\n",
		"ReadWritePaths=" + constants.ChainHome + "\n",
		"ReadOnlyPaths=" + constants.ChainHome + "/cosmovisor/genesis " + constants.ChainHome + "/cosmovisor/upgrades\n",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("chain unit is missing %q\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "DAEMON_ALLOW_DOWNLOAD_BINARIES=true") {
		t.Error("cosmovisor may download binaries")
	}
	if strings.Contains(unit, "rpc.unsafe") {
		t.Errorf("the chain unit turns on the CometBFT RPC's unsafe routes:\n%s", unit)
	}
}

// Every global account has no home directory and ProtectHome hides /home, so a
// unit whose HOME stays at the account's /home path breaks any program that
// resolves files under it: Kubo refused to start on stagenet because its
// denylist directory was "permission denied" under /home/orama-ipfs-pub.
func TestGlobalUnits_homeIsTheUnitsOwnStateDirectory(t *testing.T) {
	units := map[string]string{
		"chain":    RenderGlobalChainUnit(""),
		"ipfs":     RenderGlobalIPFSUnit(),
		"gc":       RenderGlobalIPFSGCUnit("127.0.0.1"),
		"provider": RenderGlobalProviderUnit("127.0.0.1"),
		"tor":      RenderGlobalTorRelayUnit(),
		"dirauth":  RenderGlobalTorDirauthUnit(),
		"onion":    RenderGlobalTorOnionUnit(),
		"reporter": RenderGlobalReporterUnit(),
		"archiver": RenderGlobalArchiverUnit(),
		"repair":   RenderGlobalRepairUnit(),
		"indexer":  RenderGlobalIndexerUnit(),
	}
	for name, unit := range units {
		wd := mustDirective(t, unit, "WorkingDirectory")
		if !strings.Contains(unit, "\nEnvironment=HOME="+wd+"\n") {
			t.Errorf("%s: HOME is not its WorkingDirectory %s:\n%s", name, wd, unit)
		}
		if strings.HasPrefix(wd, "/home/") {
			t.Errorf("%s: WorkingDirectory %s is under /home, which ProtectHome hides", name, wd)
		}
	}
}

// The reporter reads the epoch and signs through the local oramad, so it starts
// after the chain, and it runs in its own state directory with the RPC address
// the chain listens on.
func TestRenderGlobalReporterUnit_startsAfterTheChainAndUsesItsHome(t *testing.T) {
	unit := RenderGlobalReporterUnit()
	if !strings.Contains(unit, "After=network-online.target "+constants.ChainServiceUnit+"\n") {
		t.Errorf("the reporter does not start after the chain:\n%s", unit)
	}
	exec := mustDirective(t, unit, "ExecStart")
	want := fmt.Sprintf("%s/orama-global reporter --rpc tcp://127.0.0.1:%d --home %s --votes-dir %s", globalBinDir, constants.ChainRPCPort, constants.GlobalReporterHome, constants.GlobalTorVotesDir)
	if exec != want {
		t.Errorf("ExecStart = %q, want %q", exec, want)
	}
	if wd := mustDirective(t, unit, "WorkingDirectory"); wd != constants.GlobalReporterHome {
		t.Errorf("WorkingDirectory = %q", wd)
	}
	if !strings.Contains(unit, "User="+globalReporterUser+"\n") {
		t.Errorf("the reporter does not run as %s", globalReporterUser)
	}
}

func TestGlobalUnits_kuboRPCHostIsThreadedThrough(t *testing.T) {
	gc := RenderGlobalIPFSGCUnit("198.18.0.2")
	if !strings.Contains(gc, "Environment=IPFS_API=/ip4/198.18.0.2/tcp/31011\n") {
		t.Errorf("GC unit does not name the daemon's RPC address:\n%s", gc)
	}
	provider := mustDirective(t, RenderGlobalProviderUnit("198.18.0.2"), "ExecStart")
	if !strings.Contains(provider, "--ipfs-api http://198.18.0.2:31011 ") {
		t.Errorf("provider ExecStart = %q", provider)
	}
	loop := mustDirective(t, RenderGlobalProviderUnit("127.0.0.1"), "ExecStart")
	if !strings.Contains(loop, "--ipfs-api http://127.0.0.1:31011 ") {
		t.Errorf("provider ExecStart = %q", loop)
	}
}

// The chain unit gives oramad a soft memory limit: without one Go's collector
// let its heap reach twice what was live, and on a 4 GB stagenet node that
// shares its memory with IPFS and every namespace, the node reached 98%.
func TestRenderGlobalChainUnit_setsASoftMemoryLimit(t *testing.T) {
	unit := RenderGlobalChainUnit("")
	if !strings.Contains(unit, "\nEnvironment=GOMEMLIMIT="+constants.ChainGoMemLimit+"\n") {
		t.Fatalf("the chain unit sets no GOMEMLIMIT:\n%s", unit)
	}
}

// The GC unit passed the RPC bearer to ipfs as --api-auth=${IPFS_API_AUTH}, so
// it was on the process's command line, readable by every local user, for the
// length of a collection. The orama command reads it from the environment.
func TestRenderGlobalIPFSGCUnit_theBearerIsNeverOnACommandLine(t *testing.T) {
	unit := RenderGlobalIPFSGCUnit("127.0.0.1")
	exec := mustDirective(t, unit, "ExecStart")
	if exec != globalBinDir+"/orama node ipfs-gc" {
		t.Errorf("ExecStart = %q, want the orama command that reads its environment", exec)
	}
	if strings.Contains(exec, "IPFS_API_AUTH") || strings.Contains(exec, "api-auth") {
		t.Errorf("the bearer is on the command line: %q", exec)
	}
	if !strings.Contains(unit, "EnvironmentFile="+globalIPFSHome+"/"+globalIPFSGCEnvFile+"\n") ||
		!strings.Contains(unit, "Environment=IPFS_API=/ip4/127.0.0.1/tcp/31011\n") {
		t.Errorf("the unit does not give the command its address and its bearer:\n%s", unit)
	}
}

// The unit runs the orama CLI from the global bin directory, so the public
// Kubo's role installs it.
func TestGlobalServiceSpecs_theIPFSRoleInstallsTheCLIItsGCUnitRuns(t *testing.T) {
	if !slices.Contains(globalServiceSpecs[GlobalServiceIPFS].binaries, globalOramaCLI) {
		t.Error("the IPFS role does not install the orama CLI its GC unit runs")
	}
}

// oramad ends its process with status 87 when its shielded verifier fails in a block
// (chain/app ExitVerifierHalt), so that the block is executed again after a restart. The chain unit
// must restart it on that status: Restart=always, and no setting that exempts a status or limits
// the restarts.
func TestRenderGlobalChainUnit_restartsOnAnyExitIncludingTheVerifierHalt(t *testing.T) {
	unit := RenderGlobalChainUnit("")
	if !strings.Contains(unit, "\nRestart=always\n") {
		t.Errorf("the chain unit does not restart on every exit:\n%s", unit)
	}
	for _, forbidden := range []string{"RestartPreventExitStatus", "SuccessExitStatus", "RestartForceExitStatus"} {
		if strings.Contains(unit, forbidden) {
			t.Errorf("the chain unit sets %s, which could keep a halted node down", forbidden)
		}
	}
	if !strings.Contains(unit, "StartLimitIntervalSec=0") {
		t.Errorf("the chain unit limits its restarts:\n%s", unit)
	}
	if !strings.Contains(unit, "\nRestartSec=5\n") {
		t.Errorf("the chain unit restarts at another pace than every 5 s:\n%s", unit)
	}
	// systemd's RestartSteps/RestartMaxDelaySec are deliberately not used: they need systemd 254, may
	// never reset, and a node that stops on the same block is a crash loop for monitoring to flag.
	for _, forbidden := range []string{"RestartSteps", "RestartMaxDelaySec"} {
		if strings.Contains(unit, forbidden) {
			t.Errorf("the chain unit sets %s", forbidden)
		}
	}
}
