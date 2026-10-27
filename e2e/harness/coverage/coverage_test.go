package coverage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Orama-Network/orama/e2e/harness/manifest"
)

const (
	cliDoc = "# CLI reference\n\n## orama app\n\n```\norama app\n```\n\n## orama app env set\n\n## orama version\n\n## not a command\n"
	apiDoc = "| Route | Owner | Notes |\n|---|---|---|\n| `/health` | SDK | x |\n| `/v1/rqlite/query` | SDK | y |\n| `/v1/internal/ping` | internal | z |\n"
	txDoc  = "syntax = \"proto3\";\npackage orama.token.v1;\nservice Msg {\n  option (cosmos.msg.v1.service) = true;\n  rpc CreateToken(MsgCreateToken) returns (MsgCreateTokenResponse);\n  rpc Mint(orama.token.v1.MsgMint) returns (MsgMintResponse);\n}\nmessage MsgCreateToken {\n}\n"
	qDoc   = "package orama.token.v1;\nservice Query {\n  rpc Params(QueryParamsRequest) returns (QueryParamsResponse);\n  rpc Watch(stream QueryWatchRequest) returns (stream QueryWatchResponse);\n}\n"
)

func fakeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		CLIReferencePath:                              cliDoc,
		APISurfacePath:                                apiDoc,
		"chain/proto/orama/token/v1/tx.proto":         txDoc,
		"chain/proto/orama/token/v1/query.proto":      qDoc,
		"chain/proto/orama/token/v1/token.proto":      "service Msg {\n rpc Ignored(MsgX) returns (Y);\n}\n",
		"core/systemd/orama-turn.service":             "[Unit]\n",
		"core/systemd/orama-namespace-ipfs-gc@.timer": "[Timer]\n",
		"core/systemd/README":                         "not a unit\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func ids(items []Item) string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return strings.Join(out, ",")
}

func TestUniverse_fakeRepo(t *testing.T) {
	u, err := Universe(fakeRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"cli:orama app", "cli:orama app env set", "cli:orama version",
		"msg:orama.token.v1.MsgCreateToken", "msg:orama.token.v1.MsgMint",
		"query:orama.token.v1.Params", "query:orama.token.v1.Watch",
		"route:/health", "route:/v1/internal/ping", "route:/v1/rqlite/query",
		"unit:orama-namespace-ipfs-gc@.timer", "unit:orama-turn.service",
	}, ",")
	if got := ids(u); got != want {
		t.Fatalf("universe\n got %s\nwant %s", got, want)
	}
}

func TestUniverse_missingSourceFails(t *testing.T) {
	root := fakeRepo(t)
	if err := os.Remove(filepath.Join(root, APISurfacePath)); err != nil {
		t.Fatal(err)
	}
	if _, err := Universe(root); err == nil {
		t.Fatal("missing API surface accepted")
	}
}

func TestRoutes_emptyDocFails(t *testing.T) {
	root := fakeRepo(t)
	if err := os.WriteFile(filepath.Join(root, APISurfacePath), []byte("no tables\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Routes(root); err == nil {
		t.Fatal("a doc with no routes produced an empty universe")
	}
}

func TestUniverse_realRepo(t *testing.T) {
	// e2e/testdata/repo links in the sources Universe reads from above this
	// module, so the go test cache sees them as inputs.
	u, err := Universe(filepath.Join("..", "..", "testdata", "repo"))
	if err != nil {
		t.Fatalf("the real repository's sources do not enumerate: %v", err)
	}
	counts := map[string]int{}
	for _, it := range u {
		counts[it.Kind]++
	}
	for kind, min := range map[string]int{KindCLI: 100, KindRoute: 100, KindMsg: 10, KindQuery: 10, KindUnit: 10} {
		if counts[kind] < min {
			t.Errorf("%s: %d items, expected at least %d — did a source's format change?", kind, counts[kind], min)
		}
	}
}

func m(id string, c manifest.Covers) manifest.Manifest {
	return manifest.Manifest{ID: id, Covers: c}
}

func TestEvaluate_passesWhenCoveredOrWaived(t *testing.T) {
	u := []Item{{ID: "cli:orama version", Kind: KindCLI}, {ID: "route:/health", Kind: KindRoute}}
	res := Evaluate(u,
		[]manifest.Manifest{m("smoke", manifest.Covers{Routes: []string{"/health"}, Claims: []string{"docs/whitepaper/technical-reference/vol1/14-authorization.md: x"}})},
		[]Waiver{{ID: "cli:orama version", Reason: "r", Trigger: "t"}})
	if !res.OK() || res.Format() != "" {
		t.Fatalf("gate failed:\n%s", res.Format())
	}
	if res.Rows[0].Status != StatusWaived || res.Rows[1].Status != StatusCovered || res.Rows[1].Features[0] != "smoke" {
		t.Fatalf("rows %+v", res.Rows)
	}
	if len(res.Claims["docs/whitepaper/technical-reference/vol1/14-authorization.md: x"]) != 1 {
		t.Fatalf("claims %+v", res.Claims)
	}
	if !strings.Contains(res.Summary(), "cli 0 covered, 1 waived of 1") {
		t.Fatalf("summary %q", res.Summary())
	}
}

func TestEvaluate_failures(t *testing.T) {
	u := []Item{{ID: "route:/a", Kind: KindRoute}, {ID: "route:/b", Kind: KindRoute}, {ID: "route:/c", Kind: KindRoute}}
	res := Evaluate(u,
		[]manifest.Manifest{m("f1", manifest.Covers{Routes: []string{"/b", "/typo"}})},
		[]Waiver{
			{ID: "route:/b", Reason: "r", Trigger: "t"},    // stale: covered
			{ID: "route:/gone", Reason: "r", Trigger: "t"}, // stale: not shipped
			{ID: "route:/c", Reason: "", Trigger: "t"},     // invalid
			{ID: "", Reason: "r", Trigger: "t"},            // invalid
		})
	if res.OK() {
		t.Fatal("gate passed")
	}
	if strings.Join(res.Uncovered, ",") != "route:/a" {
		t.Errorf("uncovered %v", res.Uncovered)
	}
	if len(res.StaleWaivers) != 2 || len(res.InvalidWaivers) != 2 || len(res.UnknownCovers) != 1 {
		t.Errorf("stale=%v invalid=%v unknown=%v", res.StaleWaivers, res.InvalidWaivers, res.UnknownCovers)
	}
	out := res.Format()
	for _, want := range []string{"UNCOVERED (1)", "UNKNOWN COVERS (1)", "STALE WAIVERS (2)", "INVALID WAIVERS (2)", "route:/typo (in f1)"} {
		if !strings.Contains(out, want) {
			t.Errorf("format lacks %q:\n%s", want, out)
		}
	}
}

func TestEvaluate_duplicateWaiverAndSharedCoverage(t *testing.T) {
	u := []Item{{ID: "unit:orama-turn.service", Kind: KindUnit}, {ID: "cli:orama x", Kind: KindCLI}}
	res := Evaluate(u,
		[]manifest.Manifest{
			m("b", manifest.Covers{Units: []string{"orama-turn.service"}}),
			m("a", manifest.Covers{Units: []string{"orama-turn.service"}}),
		},
		[]Waiver{{ID: "cli:orama x", Reason: "r", Trigger: "t"}, {ID: "cli:orama x", Reason: "r", Trigger: "t"}})
	if len(res.InvalidWaivers) != 1 || !strings.Contains(res.InvalidWaivers[0], "twice") {
		t.Fatalf("invalid %v", res.InvalidWaivers)
	}
	if got := strings.Join(res.Rows[1].Features, ","); got != "a,b" {
		t.Fatalf("features %s", got)
	}
}

func TestEvaluate_emptyInputs(t *testing.T) {
	res := Evaluate(nil, nil, nil)
	if !res.OK() || len(res.Rows) != 0 {
		t.Fatalf("empty evaluation: %+v", res)
	}
}

func TestLoadWaivers_strict(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "w.yaml")
	if err := os.WriteFile(good, []byte("waivers:\n  - id: cli:orama x\n    reason: r\n    trigger: t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := LoadWaivers(good)
	if err != nil || len(ws) != 1 || ws[0].Trigger != "t" {
		t.Fatalf("ws=%v err=%v", ws, err)
	}
	bad := filepath.Join(dir, "b.yaml")
	if err := os.WriteFile(bad, []byte("waivers:\n  - id: x\n    because: r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWaivers(bad); err == nil {
		t.Fatal("unknown waiver key accepted")
	}
	empty := filepath.Join(dir, "e.yaml")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if ws, err := LoadWaivers(empty); err != nil || len(ws) != 0 {
		t.Fatalf("empty file: ws=%v err=%v", ws, err)
	}
	if _, err := LoadWaivers(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("missing waivers file accepted")
	}
}
