package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Orama-Network/orama/core/cmd/orama/internal/cmdmeta"
)

// The CLI reference is whitepaper appendix D, rendered from the cobra tree by
// book_reference_test.go; the test there fails when the file and the tree
// disagree. This file holds what the renderer shares with the other tests.
//
// Hand-written command documentation drifts the moment a flag is added: the
// deployment guide's flag tables were missing --environment, --ssh-user,
// --ca-fingerprint and --leader-raft-addr, and `orama maint push` and `orama maint rollout`
// existed without being mentioned anywhere. A reference nobody writes cannot go
// stale that way.
//
// Regenerate with:
//
//	make -C core docs
var updateReference = flag.Bool("update-cli-reference", false, "rewrite the whitepaper CLI reference (appendix D) from the command tree")

// repoRoot is the repository as core/testdata/repo links it in. The docs these
// tests read live above the module; reading them through a path inside it is
// what makes the go test cache treat them as inputs.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// .../core/cmd/orama -> core/testdata/repo
	return filepath.Clean(filepath.Join(wd, "..", "..", "testdata", "repo"))
}

// firstDifference reports the first differing line, which is all anyone needs
// to see what moved.
func firstDifference(have, want string) string {
	haveLines := strings.Split(have, "\n")
	wantLines := strings.Split(want, "\n")
	for i := 0; i < len(haveLines) && i < len(wantLines); i++ {
		if haveLines[i] != wantLines[i] {
			return fmt.Sprintf("\nfirst difference at line %d:\n  committed: %q\n  generated: %q", i+1, haveLines[i], wantLines[i])
		}
	}
	return fmt.Sprintf("\nthe files differ in length: committed %d lines, generated %d", len(haveLines), len(wantLines))
}

// collectCommands returns every runnable or group command, depth-first and
// alphabetical, excluding the root and cobra's generated help commands. A
// command hidden from `orama --help` is left out too, unless it was declared
// listed (cmdmeta.MarkListed): the maintainer group and the operator groups that
// are being replaced work and are documented, while a deprecated alias such as
// `orama network` is documented where its replacement is.
func collectCommands(root *cobra.Command) []*cobra.Command {
	var out []*cobra.Command

	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		children := append([]*cobra.Command(nil), cmd.Commands()...)
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			if (child.Hidden && !cmdmeta.IsListed(child)) || child.Name() == "help" || child.Name() == "completion" {
				continue
			}
			out = append(out, child)
			walk(child)
		}
	}
	walk(root)
	return out
}

// usageLine is the command path plus whatever argument shape its Use string
// declares, with a [flags] marker when it defines flags of its own.
func usageLine(cmd *cobra.Command) string {
	line := cmd.CommandPath()
	if parts := strings.Fields(cmd.Use); len(parts) > 1 {
		args := strings.Join(parts[1:], " ")
		args = strings.TrimSpace(strings.ReplaceAll(args, "[flags]", ""))
		if args != "" {
			line += " " + args
		}
	}
	if hasOwnFlags(cmd) {
		line += " [flags]"
	}
	return line
}

func hasOwnFlags(cmd *cobra.Command) bool {
	return len(ownFlags(cmd)) > 0
}

// ownFlags returns the flags a command declares itself, excluding every flag it
// inherits from an ancestor.
//
// It cannot ask cobra for this. Running a command merges its inherited flags
// into its own set and leaves them there, so LocalNonPersistentFlags answers
// differently depending on whether something earlier in the process executed
// that command — and a reference whose content depends on test ordering is not
// a reference. Subtracting the ancestors' persistent flags gives the same
// answer either way.
//
// A flag is inherited when an ancestor has a persistent flag of the same name
// and usage. It is compared by name and usage, not by flag object: every
// newRootCmd() builds its own root flags, and the commands are shared, so a
// command that ran under an earlier root holds that root's copy. A command that
// declares a flag named like an ancestor's persistent one with a meaning of its
// own shadows it (orama chain faucet --node is an SSH host, orama chain's
// persistent --node a REST URL), and the shadowing flag is the command's own
// and must be documented.
func ownFlags(cmd *cobra.Command) []*pflag.Flag {
	// cobra injects --help into a command's own flag set the first time that
	// command runs, so it appears or not depending on what else the process did.
	inherited := map[string]string{}
	for parent := cmd.Parent(); parent != nil; parent = parent.Parent() {
		parent.PersistentFlags().VisitAll(func(f *pflag.Flag) { inherited[f.Name] = f.Usage })
	}

	// A command's own persistent flags belong to it, but cobra only merges them
	// into Flags() when the command runs, so both sets are read and deduplicated.
	seen := map[string]bool{}
	var own []*pflag.Flag
	collect := func(f *pflag.Flag) {
		if usage, ok := inherited[f.Name]; f.Hidden || f.Name == "help" || (ok && usage == f.Usage) || seen[f.Name] {
			return
		}
		seen[f.Name] = true
		own = append(own, f)
	}
	cmd.PersistentFlags().VisitAll(collect)
	cmd.Flags().VisitAll(collect)
	return own
}

func renderFlags(flags []*pflag.Flag) string {
	var rows []string
	for _, f := range flags {
		name := "`--" + f.Name + "`"
		if f.Shorthand != "" {
			name = "`-" + f.Shorthand + "`, " + name
		}
		def := f.DefValue
		if def == "" || def == "[]" {
			def = "—"
		} else {
			def = "`" + def + "`"
		}
		usage := strings.ReplaceAll(f.Usage, "|", "\\|")
		usage = strings.ReplaceAll(usage, "\n", " ")
		rows = append(rows, fmt.Sprintf("| %s | %s | %s |\n", name, def, usage))
	}
	sort.Strings(rows)
	return strings.Join(rows, "")
}

// TestOwnFlags_shadowingFlagIsTheCommandsOwn: a flag named like an ancestor's
// persistent flag belongs to the command that declares it, an inherited one
// does not, and running the command (which merges the inherited flags into its
// set) changes neither answer.
func TestOwnFlags_shadowingFlagIsTheCommandsOwn(t *testing.T) {
	root := &cobra.Command{Use: "root"}
	root.PersistentFlags().String("node", "", "REST URL")
	root.PersistentFlags().String("rpc", "", "RPC")
	child := &cobra.Command{Use: "child", Run: func(*cobra.Command, []string) {}}
	child.Flags().String("node", "", "SSH host")
	child.Flags().String("amount", "", "amount")
	root.AddCommand(child)

	names := func() string {
		var out []string
		for _, f := range ownFlags(child) {
			out = append(out, f.Name+"="+f.Usage)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	const want = "amount=amount,node=SSH host"
	if got := names(); got != want {
		t.Fatalf("before running: own flags %q, want %q", got, want)
	}
	root.SetArgs([]string{"child"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := names(); got != want {
		t.Fatalf("after running: own flags %q, want %q", got, want)
	}
}
