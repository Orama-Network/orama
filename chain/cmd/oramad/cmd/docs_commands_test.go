package cmd

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// docsDir is the repository's documentation, from this package, through the
// chain/testdata/repo link: the docs live above this module, and reading them
// through a path inside it is what makes the go test cache treat them as
// inputs.
const docsDir = "../../../testdata/repo/docs"

// txCommandMention finds `oramad tx <group> [<verb>]` in prose. Whitespace
// between the words may be a line break.
var txCommandMention = regexp.MustCompile(`oramad tx\s+([a-z][a-z-]*)(?:\s+([a-z][a-z-]*))?`)

func subcommand(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name || c.HasAlias(name) {
			return c
		}
	}
	return nil
}

// docs/whitepaper/technical-reference/vol2/39-chain-architecture.md names `oramad tx houses submit-proposal`, and
// x/houses has no tx command of its own (client/cli/query.go only). The
// command exists all the same: AutoCLI generates `oramad tx houses` from the
// module's Msg service. Every `oramad tx` command a document names is checked
// against the command tree the binary builds, wherever it comes from.
func TestDocs_oramadTxCommandsExist(t *testing.T) {
	tx := subcommand(NewRootCmd(), "tx")
	if tx == nil {
		t.Fatal("oramad has no tx command")
	}
	// docsDir is a link; os.DirFS follows it where filepath.WalkDir would stop at it.
	docs := os.DirFS(docsDir)
	err := fs.WalkDir(docs, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(name, ".md") {
			return err
		}
		body, err := fs.ReadFile(docs, name)
		if err != nil {
			return err
		}
		path := filepath.Join("docs", name)
		for _, m := range txCommandMention.FindAllStringSubmatch(string(body), -1) {
			group := subcommand(tx, m[1])
			if group == nil {
				t.Errorf("%s names `oramad tx %s`, which oramad does not have", path, m[1])
				continue
			}
			// A word after a command with no subcommands is prose.
			if m[2] != "" && group.HasSubCommands() && subcommand(group, m[2]) == nil {
				t.Errorf("%s names `oramad tx %s %s`, which oramad does not have", path, m[1], m[2])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSubcommand_findsByNameAndAlias(t *testing.T) {
	root := &cobra.Command{Use: "root"}
	root.AddCommand(&cobra.Command{Use: "query", Aliases: []string{"q"}})
	if subcommand(root, "query") == nil || subcommand(root, "q") == nil {
		t.Error("a command is found by its name and by its alias")
	}
	if subcommand(root, "tx") != nil {
		t.Error("a command that is not there was found")
	}
}
