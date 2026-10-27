package install

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Orama-Network/orama/core/pkg/constants"
)

const reporterTestOperator = "orama1tehv5km5e9y706rc2gzk9yyun9dljjjny06dv2"

func (tf *torFixture) reporterOptions(t *testing.T) GlobalInstallOptions {
	t.Helper()
	opts := tf.options(GlobalServiceChain, GlobalServiceDirauth, GlobalServiceReporter)
	opts.Tor = TorOptions{
		Address: torTestAddress, Contact: torTestContact, ReporterOperator: reporterTestOperator,
		DirauthKeysDir: tf.bundle(t, tf.identityPEM, tf.network.Authorities[0].V3Ident),
	}
	return opts
}

func TestInstallGlobal_reporterPreparesItsHomeFromTheNetworkFile(t *testing.T) {
	tf := newTorFixture(t)
	if err := InstallGlobal(tf.reporterOptions(t), tf.host); err != nil {
		t.Fatal(err)
	}
	home := tf.state("reporter")
	if got := strings.TrimSpace(readFile(t, filepath.Join(home, "authority-id"))); got != tf.network.Authorities[0].V3Ident {
		t.Errorf("authority-id = %q, want the v3_ident of the authority at --tor-address, %s", got, tf.network.Authorities[0].V3Ident)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(home, "operator"))); got != reporterTestOperator {
		t.Errorf("operator = %q", got)
	}
	if got, want := strings.TrimSpace(readFile(t, filepath.Join(home, "vote-interval"))), fmt.Sprintf("%dm", tf.network.VotingIntervalMinutes); got != want {
		t.Errorf("vote-interval = %q, want the network file's voting interval %q", got, want)
	}
	for _, name := range []string{"authority-id", "operator", "vote-interval"} {
		info, err := os.Stat(filepath.Join(home, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v, want mode 0600", name, info, err)
		}
		if !slices.Contains(tf.chowns, chownCall{filepath.Join(home, name), 990, 991}) {
			t.Errorf("%s was not given to the reporter's account", name)
		}
	}
	info, err := os.Stat(home)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("%s: %v %v, want mode 0700", home, info, err)
	}
	if !slices.Contains(tf.chowns, chownCall{home, 990, 991}) {
		t.Errorf("%s was not given to the reporter's account", home)
	}
	if _, err := os.Stat(filepath.Join(home, "votes")); !os.IsNotExist(err) {
		t.Error("the reporter's home holds a votes directory: it reads the authority's from the shared one")
	}
	if _, err := os.Stat(filepath.Join(home, "hot-key")); !os.IsNotExist(err) {
		t.Error("the install made a hot key: the reporter creates its own on first start")
	}
	if got := readFile(t, filepath.Join(tf.host.UnitDir, constants.GlobalReporterUnit)); got != RenderGlobalReporterUnit() {
		t.Errorf("the reporter unit is not the global one:\n%s", got)
	}
	if !slices.Contains(tf.node.named("systemctl"), "enable "+constants.GlobalReporterUnit) {
		t.Errorf("the reporter unit was not enabled: %v", tf.node.named("systemctl"))
	}
	if !tf.node.users["orama-reporter"] {
		t.Errorf("accounts = %v: the reporter has no account", tf.node.users)
	}
	if _, err := os.Stat(filepath.Join(tf.host.BinDir, "orama-global")); err != nil {
		t.Errorf("the reporter's binary was not installed: %v", err)
	}
}

// A second install rewrites the operator and the authority identity and leaves
// what the reporter wrote itself: the hot key, its state and its reports.
func TestInstallGlobal_reporterReinstallKeepsTheHotKeyAndState(t *testing.T) {
	tf := newTorFixture(t)
	opts := tf.reporterOptions(t)
	if err := InstallGlobal(opts, tf.host); err != nil {
		t.Fatal(err)
	}
	home := tf.state("reporter")
	owned := map[string]string{"hot-key": "the signing key", "state.json": `{"seen_epoch":4}`, "report-3.json": "{}"}
	for name, body := range owned {
		if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const other = "orama19rl4cm2hmr8afy4kldpxz3fka4jguq0a5tup0s"
	opts.Tor.ReporterOperator = other
	if err := InstallGlobal(opts, tf.host); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(home, "operator"))); got != other {
		t.Errorf("operator = %q, want the new %s", got, other)
	}
	for name, body := range owned {
		if got := readFile(t, filepath.Join(home, name)); got != body {
			t.Errorf("%s = %q after a re-install, want %q", name, got, body)
		}
	}
}

func TestInstallGlobal_reporterRefusalsChangeNothing(t *testing.T) {
	for name, edit := range map[string]func(*GlobalInstallOptions){
		"no operator":      func(o *GlobalInstallOptions) { o.Tor.ReporterOperator = "" },
		"operator newline": func(o *GlobalInstallOptions) { o.Tor.ReporterOperator = reporterTestOperator + "\nroot" },
		"operator prefix": func(o *GlobalInstallOptions) {
			o.Tor.ReporterOperator = "cosmos1tehv5km5e9y706rc2gzk9yyun9dljjjny06dv2"
		},
		"operator short": func(o *GlobalInstallOptions) { o.Tor.ReporterOperator = "orama1abc" },
		"no dirauth": func(o *GlobalInstallOptions) {
			o.Services = []GlobalService{GlobalServiceChain, GlobalServiceReporter}
			o.Tor.DirauthKeysDir = ""
		},
		"address of no authority": func(o *GlobalInstallOptions) { o.Tor.Address = "203.0.113.9" },
	} {
		t.Run(name, func(t *testing.T) {
			tf := newTorFixture(t)
			opts := tf.reporterOptions(t)
			edit(&opts)
			if err := InstallGlobal(opts, tf.host); err == nil {
				t.Fatal("the install was not refused")
			}
			assertNothingChanged(t, tf)
			if _, err := os.Stat(tf.state("reporter")); !os.IsNotExist(err) {
				t.Error("the reporter's home was made by a refused install")
			}
			if _, err := os.Stat(filepath.Join(tf.host.UnitDir, constants.GlobalReporterUnit)); !os.IsNotExist(err) {
				t.Error("the reporter unit was written by a refused install")
			}
		})
	}
}

func TestTorOptionsValidate_reporter(t *testing.T) {
	dirauth := TorOptions{Address: torTestAddress, Contact: "c", DirauthKeysDir: "/k"}
	withOperator := dirauth
	withOperator.ReporterOperator = reporterTestOperator
	cases := map[string]struct {
		services []GlobalService
		opts     TorOptions
		ok       bool
	}{
		"dirauth and reporter":       {[]GlobalService{GlobalServiceChain, GlobalServiceDirauth, GlobalServiceReporter}, withOperator, true},
		"reporter without operator":  {[]GlobalService{GlobalServiceChain, GlobalServiceDirauth, GlobalServiceReporter}, dirauth, false},
		"operator without reporter":  {[]GlobalService{GlobalServiceDirauth}, withOperator, false},
		"reporter without dirauth":   {[]GlobalService{GlobalServiceChain, GlobalServiceReporter}, TorOptions{ReporterOperator: reporterTestOperator}, false},
		"reporter beside a relay":    {[]GlobalService{GlobalServiceChain, GlobalServiceRelay, GlobalServiceReporter}, TorOptions{Address: torTestAddress, Contact: "c", NodeID: "n", ReporterOperator: reporterTestOperator}, false},
		"dirauth alone, no operator": {[]GlobalService{GlobalServiceDirauth}, dirauth, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := c.opts.validate(c.services); (err == nil) != c.ok {
				t.Fatalf("validate = %v, want ok=%v", err, c.ok)
			}
		})
	}
}

func TestParseGlobalServices_reporterNeedsTheChain(t *testing.T) {
	got, err := ParseGlobalServices([]string{"reporter", "dirauth", "chain"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []GlobalService{GlobalServiceChain, GlobalServiceDirauth, GlobalServiceReporter}; !slices.Equal(got, want) {
		t.Errorf("services = %v, want %v", got, want)
	}
	if _, err := ParseGlobalServices([]string{"dirauth", "reporter"}); err == nil || !strings.Contains(err.Error(), "add chain to --services") {
		t.Errorf("a reporter with no chain: %v", err)
	}
	if !GlobalServiceNeedsChain(GlobalServiceReporter) {
		t.Error("the reporter does not need the chain: it reports through the chain's RPC")
	}
}

// The reporter takes the identity of the authority published at the host's
// address, not the first authority of the file.
func TestPlanGlobalReporter_picksTheAuthorityAtTheAddress(t *testing.T) {
	tf := newTorFixture(t)
	plan := &torPlan{network: tf.network}
	opts := GlobalInstallOptions{
		Services: []GlobalService{GlobalServiceChain, GlobalServiceDirauth, GlobalServiceReporter},
		Tor:      TorOptions{Address: tf.network.Authorities[1].Address, ReporterOperator: reporterTestOperator},
	}
	got, err := planGlobalReporter(opts, plan)
	if err != nil || got == nil {
		t.Fatalf("plan = %v, %v", got, err)
	}
	if got.authorityID != tf.network.Authorities[1].V3Ident || got.authorityID == tf.network.Authorities[0].V3Ident {
		t.Errorf("authority-id = %s, want %s", got.authorityID, tf.network.Authorities[1].V3Ident)
	}
	opts.Services = []GlobalService{GlobalServiceChain, GlobalServiceDirauth}
	if got, err := planGlobalReporter(opts, plan); got != nil || err != nil {
		t.Errorf("an install with no reporter planned one: %v %v", got, err)
	}
	opts.Services = append(opts.Services, GlobalServiceReporter)
	opts.Tor.Address = "203.0.113.9"
	if _, err := planGlobalReporter(opts, plan); err == nil {
		t.Error("an address that is no authority's gave a reporter plan")
	}
}

func TestInstallGlobal_colocatedReporterReachesTheChainOnTheNamespaceAddress(t *testing.T) {
	opts := GlobalInstallOptions{Services: []GlobalService{GlobalServiceChain, GlobalServiceDirauth, GlobalServiceReporter}, Colocated: true}
	files, err := opts.unitFilesFor(GlobalServiceReporter)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("the reporter has %d unit files, want its one service", len(files))
	}
	exec := mustDirective(t, files[0].body, "ExecStart")
	if !strings.Contains(exec, "--rpc tcp://"+constants.GlobalNetnsAddr+":31001 ") || strings.Contains(exec, "127.0.0.1") {
		t.Errorf("the reporter does not call the chain on the namespace address: %s", exec)
	}
}

// The reporter command (chain module) reads these files from its home; the
// installer, in this module, writes them. A rename on one side must fail here.
func TestReporterFileNamesMatchTheReporterCommand(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller")
	}
	src := filepath.Join(filepath.Dir(file), "..", "..", "testdata", "repo", "chain", "cmd", "orama-global", "reporter.go")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	for _, name := range []string{reporterOperatorFile, reporterAuthorityFile, reporterVoteIntervalFile} {
		if !strings.Contains(string(data), `filepath.Join(fl.home, "`+name+`")`) {
			t.Errorf("reporter.go does not read %q from its home", name)
		}
	}
	if !strings.Contains(string(data), `"votes-dir"`) {
		t.Error("reporter.go has no --votes-dir flag, which the reporter unit passes")
	}
}

// The authority's own vote reaches the reporter through a directory only the
// two of them share: the authority's account owns and writes it, the reporter's
// group reads it (setgid so a file made in it has that group), and neither
// account is given the other's home.
func TestInstallGlobal_reporterGetsTheAuthoritysVoteThroughASharedDirectory(t *testing.T) {
	tf := newTorFixture(t)
	if err := InstallGlobal(tf.reporterOptions(t), tf.host); err != nil {
		t.Fatal(err)
	}
	votes := tf.state("tor-votes")
	info, err := os.Stat(votes)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o750 || info.Mode()&os.ModeSetgid == 0 {
		t.Errorf("%s mode = %v, want 0750 and setgid", votes, info.Mode())
	}
	// uid 990 is every fixture account; the group is the fixture's 995.
	if !slices.Contains(tf.chowns, chownCall{votes, 990, 995}) {
		t.Errorf("%s was not given to the authority's account and the reporter's group: %v", votes, tf.chowns)
	}
	archive := readFile(t, filepath.Join(tf.host.UnitDir, constants.GlobalTorArchiveUnit))
	if archive != RenderGlobalTorArchiveUnit(true) {
		t.Errorf("the archive oneshot does not export the vote:\n%s", archive)
	}
	if !strings.Contains(mustDirective(t, archive, "ExecStart"), "--export-votes-dir "+constants.GlobalTorVotesDir) ||
		mustDirective(t, archive, "ReadWritePaths") != constants.GlobalTorVotesDir {
		t.Errorf("archive unit:\n%s", archive)
	}
	if rep := readFile(t, filepath.Join(tf.host.UnitDir, constants.GlobalReporterUnit)); !strings.Contains(mustDirective(t, rep, "ExecStart"), "--votes-dir "+constants.GlobalTorVotesDir) {
		t.Errorf("the reporter does not read the shared directory:\n%s", rep)
	}
}

// An authority with no reporter exports nothing and has no shared directory,
// and re-installing it beside a reporter that is already installed keeps the
// export.
func TestInstallGlobal_voteExportFollowsTheReporter(t *testing.T) {
	tf := newTorFixture(t)
	plain := tf.options(GlobalServiceDirauth)
	plain.Tor = TorOptions{Address: torTestAddress, Contact: torTestContact, DirauthKeysDir: tf.bundle(t, tf.identityPEM, tf.network.Authorities[0].V3Ident)}
	if err := InstallGlobal(plain, tf.host); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(tf.host.UnitDir, constants.GlobalTorArchiveUnit)); got != RenderGlobalTorArchiveUnit(false) {
		t.Errorf("an authority with no reporter exports its vote:\n%s", got)
	}
	if _, err := os.Stat(tf.state("tor-votes")); !os.IsNotExist(err) {
		t.Error("a votes directory was made for an authority with no reporter")
	}
	if err := InstallGlobal(tf.reporterOptions(t), tf.host); err != nil {
		t.Fatal(err)
	}
	if err := InstallGlobal(plain, tf.host); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(tf.host.UnitDir, constants.GlobalTorArchiveUnit)); got != RenderGlobalTorArchiveUnit(true) {
		t.Errorf("re-installing the authority beside its reporter dropped the vote export:\n%s", got)
	}
}

func TestRenderGlobalTorArchiveUnit_exportWritesOnlyTheVotesDirectory(t *testing.T) {
	plain, export := RenderGlobalTorArchiveUnit(false), RenderGlobalTorArchiveUnit(true)
	if strings.Contains(plain, "export-votes-dir") || strings.Contains(plain, "ReadWritePaths") {
		t.Errorf("the plain archive unit exports:\n%s", plain)
	}
	if got := mustDirective(t, export, "ReadWritePaths"); got != constants.GlobalTorVotesDir {
		t.Errorf("ReadWritePaths = %q, want only the votes directory", got)
	}
	for _, unit := range []string{plain, export} {
		if got := mustDirective(t, unit, "User"); got != globalTorDirauthUser {
			t.Errorf("the archive runs as %s", got)
		}
		if !strings.Contains(unit, "IPAddressDeny=any\nIPAddressAllow=localhost\n") || mustDirective(t, unit, "RestrictAddressFamilies") != "AF_UNIX" {
			t.Errorf("the archive may reach the network:\n%s", unit)
		}
	}
}

// A unit directory that cannot be read is an error, not "no reporter yet": the
// archive unit would otherwise be rewritten without its export.
func TestInstallGlobal_unreadableUnitDirectoryRefusesTheInstall(t *testing.T) {
	tf := newTorFixture(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tf.host.UnitDir = filepath.Join(blocker, "units")
	if err := InstallGlobal(tf.reporterOptions(t), tf.host); err == nil || !strings.Contains(err.Error(), constants.GlobalReporterUnit) {
		t.Fatalf("err = %v, want the unreadable unit named", err)
	}
	if tf.torInstalled != 0 || len(tf.node.calls) > 1 {
		t.Errorf("the host changed before the refusal: tor installs %d, commands %v", tf.torInstalled, tf.node.calls)
	}
}
