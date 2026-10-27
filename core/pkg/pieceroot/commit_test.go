package pieceroot

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRootsMatchTheChainVectors(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "testdata", "repo", "chain", "piece", "testdata", "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		LeafSize int `json:"leaf_size"`
		Cases    []struct {
			Name            string `json:"name"`
			DataHex         string `json:"data_hex"`
			RealLeafCount   uint64 `json:"real_leaf_count"`
			PaddedLeafCount uint64 `json:"padded_leaf_count"`
			RootHex         string `json:"root_hex"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.LeafSize != LeafSize || len(doc.Cases) == 0 {
		t.Fatalf("vectors leaf %d cases %d", doc.LeafSize, len(doc.Cases))
	}
	for _, tc := range doc.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			data, err := hex.DecodeString(tc.DataHex)
			if err != nil {
				t.Fatal(err)
			}
			c, err := Commit(data)
			if err != nil {
				t.Fatal(err)
			}
			if c.RealLeafCount != tc.RealLeafCount || c.PaddedLeafCount != tc.PaddedLeafCount {
				t.Fatalf("counts real %d padded %d", c.RealLeafCount, c.PaddedLeafCount)
			}
			if hex.EncodeToString(c.Root) != tc.RootHex {
				t.Fatalf("root %x", c.Root)
			}
		})
	}
}
