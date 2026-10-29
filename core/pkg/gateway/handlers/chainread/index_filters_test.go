package chainread

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// messageKindsFile is the indexer's table of message kinds (chain/indexer/kinds.go embeds it), the
// one source of the filters a transaction list takes besides "all" and "failed".
var messageKindsFile = filepath.Join("..", "..", "..", "..", "testdata", "repo", "chain", "indexer", "message_kinds.json")

// The gateway refuses a filter the indexer would not answer and passes every one it would: its
// list must be the indexer's.
func TestTxListFilters_areTheIndexersFilters(t *testing.T) {
	raw, err := os.ReadFile(messageKindsFile)
	if err != nil {
		t.Fatalf("read the indexer's message kinds: %v", err)
	}
	var kinds map[string]string
	if err := json.Unmarshal(raw, &kinds); err != nil {
		t.Fatalf("the indexer's message kinds are not JSON: %v", err)
	}
	want := map[string]bool{"all": true, "failed": true}
	for _, kind := range kinds {
		want[kind] = true
	}
	got := slices.Sorted(maps.Keys(txListFilters))
	if exp := slices.Sorted(maps.Keys(want)); !slices.Equal(got, exp) {
		t.Fatalf("the gateway's filters %v, the indexer's %v", got, exp)
	}
}
