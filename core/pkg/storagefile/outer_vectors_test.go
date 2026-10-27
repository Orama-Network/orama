package storagefile

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The chain's repair delegate applies the same slot layer
// (chain/storagekey). Both sides are locked to that package's vectors.
func TestApplyOuter_matchesTheChainVectors(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "testdata", "repo", "chain", "storagekey", "testdata", "outer_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []struct {
			RepairSeed string `json:"repair_seed"`
			DealNonce  string `json:"deal_nonce"`
			Slot       uint32 `json:"slot"`
			Input      string `json:"input"`
			Output     string `json:"output"`
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Cases) == 0 {
		t.Fatalf("vectors: %v", err)
	}
	for i, c := range doc.Cases {
		seed, _ := hex.DecodeString(c.RepairSeed)
		nonce, _ := hex.DecodeString(c.DealNonce)
		in, _ := hex.DecodeString(c.Input)
		want, _ := hex.DecodeString(c.Output)
		got, err := applyOuter(seed, nonce, c.Slot, in)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
}

// A slot keystream taken straight from HKDF stopped at 8160 bytes; a file
// past that could not be sealed.
func TestPrepare_sealsAFileLargerThanOneHKDFOutput(t *testing.T) {
	seed := bytes.Repeat([]byte{1}, 32)
	repair := bytes.Repeat([]byte{2}, 32)
	nonce := bytes.Repeat([]byte{3}, DealNonceLen)
	for _, n := range []int{8161, 1 << 20, 1024 * 16} {
		plain := bytes.Repeat([]byte{0x5a}, n)
		slots, err := Prepare(seed, repair, nonce, 3, plain)
		if err != nil {
			t.Fatalf("%d bytes: %v", n, err)
		}
		got, err := Open(seed, repair, nonce, 2, slots[2].Bytes)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("%d bytes: open %v", n, err)
		}
	}
}
