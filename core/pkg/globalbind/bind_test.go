package globalbind

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/hex"
	"os"
	"testing"

	"github.com/Orama-Network/orama/core/pkg/clusterreg"
)

const (
	vectorOperator = "orama19rl4cm2hmr8afy4kldpxz3fka4jguq0a5tup0s"
	vectorChain    = "orama-testnet-2"
	// vectorSecpSig is SignSecp256k1 of secret 0x11*32. The chain verifier
	// test accepts this same signature.
	vectorSecpSig = "aa659956e92943f40a10a6239f18a666171e3add0d4e99ad8146f5e79541f6cf6089cbd369b45f415d4ca404d6fda34faa0a96c032485fedcb3863ea675885cf"
)

func TestSignBytesMatchesChainSource(t *testing.T) {
	data, err := os.ReadFile("../../testdata/repo/chain/x/nodes/types/binding.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !bytes.Contains(data, []byte(`BindingPrefix = "orama-global-bind-v1"`)) {
		t.Fatal("chain binding prefix changed")
	}
	if !bytes.Contains(data, []byte(`fmt.Sprintf("%s|%s|%s|%s|%s", BindingPrefix, chainID, operator, service, hex.EncodeToString(pubkey))`)) {
		t.Fatal("chain binding statement format changed")
	}
	_ = text
	pub := bytes.Repeat([]byte{0x02}, 33)
	got := string(SignBytes(vectorChain, vectorOperator, "provider", pub))
	want := Prefix + "|" + vectorChain + "|" + vectorOperator + "|provider|" + hex.EncodeToString(pub)
	if got != want {
		t.Fatalf("statement %s", got)
	}
}

func TestSignSecp256k1_vector(t *testing.T) {
	secret := bytes.Repeat([]byte{0x11}, 32)
	b, err := SignSecp256k1(secret, vectorChain, vectorOperator, "provider")
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(b.Signature) != vectorSecpSig {
		t.Fatalf("sig %x", b.Signature)
	}
	if len(b.Pubkey) != 33 {
		t.Fatalf("pub %d", len(b.Pubkey))
	}
	if err := Verify(b, vectorChain, vectorOperator); err != nil {
		t.Fatal(err)
	}
}

func TestSignExpandedEd25519_matchesStandardSign(t *testing.T) {
	seed := bytes.Repeat([]byte{0x42}, 32)
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	msg := []byte("orama-global-bind-v1|chain|op|tor|" + hex.EncodeToString(pub))
	sum := sha512.Sum512(seed)
	got, err := signExpanded(sum[:], pub, msg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, ed25519.Sign(priv, msg)) {
		t.Fatal("expanded ed25519 sign does not match crypto/ed25519")
	}
	if !ed25519.Verify(pub, msg, got) {
		t.Fatal("expanded signature did not verify")
	}
	clamped := append([]byte(nil), sum[:]...)
	clamped[0] &= 248
	clamped[31] &= 127
	clamped[31] |= 64
	again, err := signExpanded(clamped, pub, msg)
	if err != nil || !bytes.Equal(again, got) {
		t.Fatal("a pre-clamped Tor secret signed differently")
	}
}

func TestSignEd25519_refusesABadOperator(t *testing.T) {
	seed := bytes.Repeat([]byte{0x07}, 32)
	if _, err := SignEd25519(seed, vectorChain, "ORAMA1ABC", "tor"); err == nil {
		t.Fatal("an uppercase operator was accepted")
	}
	if err := CanonicalCheck(vectorOperator); err != nil {
		t.Fatal(err)
	}
}

// CanonicalCheck exposes the operator check the tests and the chain share.
func CanonicalCheck(operator string) error {
	if _, err := clusterreg.CanonicalAccount(operator); err != nil {
		return err
	}
	return validateNames(vectorChain, operator, "tor")
}
