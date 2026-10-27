package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestBech32_zeroConsAddress(t *testing.T) {
	const want = "oramavalcons1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqy6czte"
	got, err := bech32Encode(chainConsHRP, bytes.Repeat([]byte{0}, 20))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("encode = %s, want %s", got, want)
	}
	raw, err := decodeConsAddress(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, bytes.Repeat([]byte{0}, 20)) {
		t.Fatalf("decoded %x", raw)
	}
}

func TestBech32_rejectsABadChecksum(t *testing.T) {
	if _, err := decodeConsAddress("oramavalcons1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqy6cztf"); err == nil {
		t.Fatal("a flipped checksum decoded")
	}
}

func TestConsHRPMatchesChainParams(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/repo/chain/app/params/params.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`Bech32Prefix = "orama"`)) || !bytes.Contains(data, []byte(`Bech32Prefix + "valcons"`)) {
		t.Fatal("chain consensus prefix is no longer orama + valcons; chainConsHRP has to follow it")
	}
}

func TestInterpretSigning_ratioJailAndTombstone(t *testing.T) {
	pub := bytes.Repeat([]byte{0x11}, 32)
	sum := sha256.Sum256(pub)
	cons := strings.ToUpper(hex.EncodeToString(sum[:20]))
	addr, err := bech32Encode(chainConsHRP, sum[:20])
	if err != nil {
		t.Fatal(err)
	}
	params := []byte(`{"params":{"signed_blocks_window":"100","min_signed_per_window":"0.900000000000000000"}}`)
	signing := []byte(`{"info":[{"address":"` + addr + `","missed_blocks_counter":"7","tombstoned":true}]}`)
	staking := []byte(`{"validators":[{"jailed":true,"consensus_pubkey":{"@type":"/cosmos.crypto.ed25519.PubKey","key":"` + base64.StdEncoding.EncodeToString(pub) + `"}}]}`)
	view := InterpretSigning(cons, 10, params, [][]byte{signing}, [][]byte{staking})
	if view.Error != "" {
		t.Fatal(view.Error)
	}
	if view.Jailed == nil || !*view.Jailed || view.Tombstoned == nil || !*view.Tombstoned {
		t.Fatalf("jailed=%v tombstoned=%v", view.Jailed, view.Tombstoned)
	}
	if view.MissedBlockRatio == nil || *view.MissedBlockRatio < 0.069 || *view.MissedBlockRatio > 0.071 {
		t.Fatalf("ratio=%v", view.MissedBlockRatio)
	}
	if view.MinSignedPerWindow == nil || *view.MinSignedPerWindow != 0.9 {
		t.Fatalf("min signed=%v", view.MinSignedPerWindow)
	}
}

func TestInterpretSigning_fullNodeIsNotAnError(t *testing.T) {
	params := []byte(`{"params":{"signed_blocks_window":"100","min_signed_per_window":"0.500000000000000000"}}`)
	empty := []byte(`{"info":[],"validators":[]}`)
	view := InterpretSigning("5D6A0C7E9B00000000000000000000000000AA01", 0, params, [][]byte{empty}, [][]byte{empty})
	if view.Error != "" || view.Jailed != nil || view.MissedBlockRatio != nil {
		t.Fatalf("a full node with no signing row must stay quiet, got %+v", view)
	}
}

func TestInterpretSigning_validatorMissingFromTheSet(t *testing.T) {
	params := []byte(`{"params":{"signed_blocks_window":"100","min_signed_per_window":"0.5"}}`)
	empty := []byte(`{"info":[]}`)
	stake := []byte(`{"validators":[]}`)
	view := InterpretSigning("5D6A0C7E9B00000000000000000000000000AA01", 10, params, [][]byte{empty}, [][]byte{stake})
	if view.Error == "" || view.Jailed != nil {
		t.Fatalf("got %+v", view)
	}
}

func TestInterpretSigning_refusesAQueryError(t *testing.T) {
	params := []byte(`{"code":2,"message":"unavailable"}`)
	view := InterpretSigning("5D6A0C7E9B00000000000000000000000000AA01", 10, params, nil, nil)
	if view.Error == "" || view.MissedBlockRatio != nil {
		t.Fatalf("got %+v", view)
	}
}
