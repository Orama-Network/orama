package enroll

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// sealVector is contracts/enrollment/seal.json.
type sealVector struct {
	Code          string `json:"code"`
	DerivedKeyHex string `json:"derivedKeyHex"`
	Plaintext     string `json:"plaintext"`
}

func loadSealVector(t *testing.T) sealVector {
	t.Helper()
	// .../core/pkg/gateway/handlers/enroll -> repo root
	path := filepath.Join("..", "..", "..", "..", "testdata", "repo", "contracts", "enrollment", "seal.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var v sealVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return v
}

// The gateway and the OramaOS agent are separate Go modules, so each has its
// own copy of the seal. This vector is the only thing stopping them drifting:
// an agent that derives a different key silently fails every enrollment, and
// the failure looks like a network problem.
func TestSeal_matchesTheSharedVector(t *testing.T) {
	v := loadSealVector(t)

	key, err := sealKey(v.Code)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if got := hex.EncodeToString(key); got != v.DerivedKeyHex {
		t.Fatalf("derived key %s, want %s from contracts/enrollment/seal.json — the two "+
			"sides of the enrollment exchange no longer agree", got, v.DerivedKeyHex)
	}
}

func TestSeal_roundTripsTheSharedVector(t *testing.T) {
	v := loadSealVector(t)

	sealed, err := Seal(v.Code, []byte(v.Plaintext))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	opened, err := Open(v.Code, sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(opened) != v.Plaintext {
		t.Errorf("round trip returned %q", opened)
	}
}
