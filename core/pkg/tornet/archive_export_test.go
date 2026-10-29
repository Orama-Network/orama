package tornet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	exportAuth1 = "00000000000000000000000000000000000000A0"
	exportAuth2 = "00000000000000000000000000000000000000A1"
	exportStamp = "20261008T120000Z"
)

// authorityDir is an authority's DataDirectory with the fixture consensus and
// votes and the certificate of the authority with v3 identity ident.
func authorityDir(t *testing.T, ident string) string {
	t.Helper()
	dir := dataDir(t)
	if err := os.MkdirAll(filepath.Join(dir, ceremonyKeysDir), 0o700); err != nil {
		t.Fatal(err)
	}
	cert := "dir-key-certificate-version 3\nfingerprint " + ident + "\ndir-key-expires 2099-10-08 00:00:00\n"
	write(t, filepath.Join(dir, ceremonyKeysDir, KeyAuthorityCert), []byte(cert))
	return dir
}

func TestExportOwnVote_writesOnlyTheAuthoritysOwnVote(t *testing.T) {
	for ident, other := range map[string]string{exportAuth1: "OramaAuth2", exportAuth2: "OramaAuth1"} {
		dir, out := authorityDir(t, ident), t.TempDir()
		wrote, err := ExportOwnVote(dir, out)
		if err != nil || !wrote {
			t.Fatalf("%s: wrote %v err %v", ident, wrote, err)
		}
		entries, _ := os.ReadDir(out)
		if len(entries) != 1 || entries[0].Name() != exportStamp+VoteExportSuffix {
			t.Fatalf("%s: the votes directory holds %v, want only %s%s", ident, entries, exportStamp, VoteExportSuffix)
		}
		path := filepath.Join(out, entries[0].Name())
		body, _ := os.ReadFile(path)
		if !strings.Contains(string(body), ident) || strings.Contains(string(body), "dir-source "+other+" ") {
			t.Errorf("%s: the exported vote is not the authority's own alone:\n%s", ident, body)
		}
		if strings.Count(string(body), "network-status-version") != 1 || !strings.Contains(string(body), "\ndirectory-footer\n") {
			t.Errorf("%s: the exported file is not one complete vote:\n%s", ident, body)
		}
		if info, _ := os.Stat(path); info.Mode().Perm() != exportFileMode {
			t.Errorf("%s: mode %v, want %v for the reporter's group", ident, info.Mode().Perm(), os.FileMode(exportFileMode))
		}
		entries, _ = os.ReadDir(out)
		if len(entries) != 1 {
			t.Errorf("%s: a temporary file was left behind: %v", ident, entries)
		}
	}
}

func TestExportOwnVote_isIdempotentAndNeverFollowsALink(t *testing.T) {
	dir, out := authorityDir(t, exportAuth1), t.TempDir()
	if _, err := ExportOwnVote(dir, out); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(out, exportStamp+VoteExportSuffix)
	first, _ := os.ReadFile(path)
	if wrote, err := ExportOwnVote(dir, out); err != nil || wrote {
		t.Fatalf("a second run: wrote %v err %v", wrote, err)
	}
	if again, _ := os.ReadFile(path); string(again) != string(first) {
		t.Error("an exported period was rewritten")
	}

	// A link where the file would go is left as it is: the export never reads or
	// replaces what it points at.
	out2, secret := t.TempDir(), filepath.Join(t.TempDir(), "secret")
	write(t, secret, []byte("not a vote"))
	if err := os.Symlink(secret, filepath.Join(out2, exportStamp+VoteExportSuffix)); err != nil {
		t.Fatal(err)
	}
	if wrote, err := ExportOwnVote(dir, out2); err != nil || wrote {
		t.Fatalf("over a link: wrote %v err %v", wrote, err)
	}
	if got, _ := os.ReadFile(secret); string(got) != "not a vote" {
		t.Errorf("the file behind the link changed: %q", got)
	}
}

func TestExportOwnVote_refusals(t *testing.T) {
	cases := map[string]func(t *testing.T) (dataDir, out string){
		"no certificate": func(t *testing.T) (string, string) {
			return dataDir(t), t.TempDir()
		},
		"a certificate for an authority that did not vote": func(t *testing.T) (string, string) {
			return authorityDir(t, "00000000000000000000000000000000000000FF"), t.TempDir()
		},
		"an incomplete own vote": func(t *testing.T) (string, string) {
			dir := authorityDir(t, exportAuth1)
			votes := strings.Replace(string(readFixture(t, "votes.txt")), "directory-footer\n", "", 1)
			write(t, filepath.Join(dir, DataDirVotes), []byte(votes))
			return dir, t.TempDir()
		},
		"no votes directory": func(t *testing.T) (string, string) {
			return authorityDir(t, exportAuth1), filepath.Join(t.TempDir(), "absent")
		},
		"no consensus": func(t *testing.T) (string, string) {
			dir := authorityDir(t, exportAuth1)
			if err := os.Remove(filepath.Join(dir, DataDirConsensus)); err != nil {
				t.Fatal(err)
			}
			return dir, t.TempDir()
		},
		"a malformed certificate": func(t *testing.T) (string, string) {
			dir := authorityDir(t, exportAuth1)
			write(t, filepath.Join(dir, ceremonyKeysDir, KeyAuthorityCert), []byte("fingerprint nonsense\n"))
			return dir, t.TempDir()
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			dir, out := setup(t)
			if wrote, err := ExportOwnVote(dir, out); err == nil || wrote {
				t.Fatalf("wrote %v err %v, want a refusal", wrote, err)
			}
			if entries, err := os.ReadDir(out); err == nil && len(entries) != 0 {
				t.Errorf("a refused export left %v", entries)
			}
		})
	}
}

// A period whose votes the authority no longer holds exports nothing and is not
// an error (the archive's manifest says votes_missing).
func TestExportOwnVote_noVotesOfThePeriod(t *testing.T) {
	dir, out := authorityDir(t, exportAuth1), t.TempDir()
	if err := os.Remove(filepath.Join(dir, DataDirVotes)); err != nil {
		t.Fatal(err)
	}
	if wrote, err := ExportOwnVote(dir, out); err != nil || wrote {
		t.Fatalf("wrote %v err %v", wrote, err)
	}
}

// The reporter (chain module, which core cannot import) reads the files named
// with this suffix in its votes directory; a change on either side must fail here.
func TestVoteExportSuffixIsTheReportersVoteSuffix(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "repo", "chain", "reporter", "load.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `VoteSuffix = "`+VoteExportSuffix+`"`) {
		t.Errorf("chain/reporter/load.go does not read %q files", VoteExportSuffix)
	}
}
