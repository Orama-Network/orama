package shieldedaddr

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// vectorFile is the one file the Rust, Go and TypeScript codecs all read.
var vectorFile = filepath.Join("..", "..", "testdata", "repo", "chain", "x", "shielded", "wallet", "testdata", "addresses", "addresses.json")

type pair struct {
	Raw     string `json:"raw"`
	Address string `json:"address"`
}

type vectors struct {
	HRP           string `json:"hrp"`
	RawLength     int    `json:"raw_length"`
	EncodedLength int    `json:"encoded_length"`
	Valid         []pair `json:"valid"`
	Accept        []pair `json:"accept"`
	Invalid       []struct {
		Why     string `json:"why"`
		Address string `json:"address"`
		Error   string `json:"error"`
	} `json:"invalid"`
}

func load(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile(vectorFile)
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func rawOf(t *testing.T, h string) [RawLen]byte {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != RawLen {
		t.Fatalf("bad vector %q: %v", h, err)
	}
	var out [RawLen]byte
	copy(out[:], b)
	return out
}

func TestConstantsAreTheVectorFiles(t *testing.T) {
	v := load(t)
	if v.HRP != HRP || v.RawLength != RawLen || v.EncodedLength != EncodedLen {
		t.Fatalf("constants %q %d %d, vector file %q %d %d", HRP, RawLen, EncodedLen, v.HRP, v.RawLength, v.EncodedLength)
	}
}

func TestEncodeDecode_validVectors(t *testing.T) {
	v := load(t)
	if len(v.Valid) < 8 {
		t.Fatalf("only %d valid vectors", len(v.Valid))
	}
	for _, c := range v.Valid {
		raw := rawOf(t, c.Raw)
		if got := Encode(raw); got != c.Address {
			t.Errorf("Encode(%s) = %s, want %s", c.Raw, got, c.Address)
		}
		got, err := Decode(c.Address)
		if err != nil || got != raw {
			t.Errorf("Decode(%s) = %x, %v", c.Address, got, err)
		}
		if !IsAddress(c.Address) || len(c.Address) != EncodedLen {
			t.Errorf("%s: IsAddress or length", c.Address)
		}
	}
}

func TestDecode_acceptsCapitals(t *testing.T) {
	for _, c := range load(t).Accept {
		got, err := Decode(c.Address)
		if err != nil || got != rawOf(t, c.Raw) {
			t.Errorf("Decode(%s) = %x, %v", c.Address, got, err)
		}
		if c.Address == strings.ToLower(c.Address) {
			t.Errorf("%s is not in capitals", c.Address)
		}
	}
}

func TestDecode_invalidVectorsFailWithTheirCode(t *testing.T) {
	v := load(t)
	if len(v.Invalid) < 15 {
		t.Fatalf("only %d invalid vectors", len(v.Invalid))
	}
	for _, c := range v.Invalid {
		_, err := Decode(c.Address)
		if err == nil {
			t.Errorf("%s (%q) was accepted", c.Why, c.Address)
			continue
		}
		if got := CodeOf(err); string(got) != c.Error {
			t.Errorf("%s: code %q, want %q (%v)", c.Why, got, c.Error, err)
		}
		if !strings.HasPrefix(err.Error(), "not a shielded address: it ") {
			t.Errorf("%s: message %q", c.Why, err)
		}
	}
}

// The checksum is BIP-350's: its own valid Bech32m strings satisfy the polymod and the original
// Bech32 and a broken string do not.
func TestPolymod_bip350(t *testing.T) {
	check := func(s string) uint32 {
		s = strings.ToLower(s)
		sep := strings.LastIndexByte(s, '1')
		var data []byte
		for i := sep + 1; i < len(s); i++ {
			data = append(data, byte(strings.IndexByte(charset, s[i])))
		}
		return polymod(append(hrpExpand(s[:sep]), data...))
	}
	for _, s := range []string{
		"A1LQFN3A", "a1lqfn3a",
		"an83characterlonghumanreadablepartthatcontainsthetheexcludedcharactersbioandnumber11sg7hg6",
		"abcdef1l7aum6echk45nj3s0wdvt2fg8x9yrzpqzd3ryx",
		"split1checkupstagehandshakeupstreamerranterredcaperredlc445v",
		"?1v759aa",
	} {
		if check(s) != bech32mConst {
			t.Errorf("%s does not satisfy the Bech32m checksum", s)
		}
	}
	for _, s := range []string{"A1G7SGD8", "abcdef1qpzry9x8gf2tvdw0s3jn54khce6mua7lmqqqxw"} {
		if check(s) == bech32mConst {
			t.Errorf("%s satisfies the Bech32m checksum", s)
		}
	}
}

func TestDecode_oddInputNeverPanics(t *testing.T) {
	for _, s := range []string{"", "1", "oramas1", "oramas1q", "ORAMAS1", "é1é", "oramas1\U0001F600", strings.Repeat("1", 200), strings.Repeat("q", 500), "oramas1" + strings.Repeat("q", 75)} {
		if _, err := Decode(s); err == nil {
			t.Errorf("%q was accepted", s)
		}
	}
	if CodeOf(nil) != "" {
		t.Error("CodeOf(nil)")
	}
}

func FuzzDecode(f *testing.F) {
	f.Add("")
	f.Add("oramas1u0rsk23eql6q3l5qyaclkpuzfmrwsf4l8478tph7vtewqnxm45ex0ranw9c69lguh7czufckn7s")
	f.Add("ORAMAS1U0RSK23EQL6Q3L5QYACLKPUZFMRWSF4L8478TPH7VTEWQNXM45EX0RANW9C69LGUH7CZUFCKN7S")
	f.Fuzz(func(t *testing.T, s string) {
		raw, err := Decode(s)
		if err != nil {
			if CodeOf(err) == "" {
				t.Fatalf("a refusal with no code: %v", err)
			}
			return
		}
		if again := Encode(raw); again != strings.ToLower(s) {
			t.Fatalf("%q decoded to %x which encodes to %q", s, raw, again)
		}
	})
}
