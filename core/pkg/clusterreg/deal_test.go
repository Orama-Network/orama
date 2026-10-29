package clusterreg

import (
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func sampleDeal() Deal {
	grantee, err := bech32Encode(accountHRP, bytes.Repeat([]byte{0x01}, 20))
	if err != nil {
		panic(err)
	}
	piece := func(b byte, n uint64) Piece {
		p, err := NewPiece(bytes.Repeat([]byte{b}, 32), n)
		if err != nil {
			panic(err)
		}
		return p
	}
	return Deal{
		Signer:         "orama19rl4cm2hmr8afy4kldpxz3fka4jguq0a5tup0s",
		Granter:        grantee,
		Class:          DealClassPrivate,
		Nonce:          bytes.Repeat([]byte{0x11}, 32),
		RepairDelegate: "repair-1",
		Replicas:       3,
		PricePerEpoch:  "1000",
		DurationEpochs: 30,
		Pieces:         []Piece{piece(0x01, 1024), piece(0x02, 2048), piece(0x03, 1025)},
	}
}

func TestEncodeDeal_matchesChainMarshal(t *testing.T) {
	const want = "0a2c6f72616d613139726c34636d32686d7238616679346b6c6470787a33666b61346a6775713061357475703073122c6f72616d6131717971737a716770717971737a716770717971737a716770717971737a7167703663737a61651801222011111111111111111111111111111111111111111111111111111111111111112a087265706169722d3130033a0431303030401e4a290a200101010101010101010101010101010101010101010101010101010101010101100118012080084a290a200202020202020202020202020202020202020202020202020202020202020202100218022080104a290a20030303030303030303030303030303030303030303030303030303030303030310021802208108"
	if hex.EncodeToString(EncodeDeal(sampleDeal())) != want {
		t.Fatalf("create %x", EncodeDeal(sampleDeal()))
	}
	pub := sampleDeal()
	pub.Granter = ""
	pub.RepairDelegate = ""
	pub.Class = DealClassPublicPin
	pub.Nonce = bytes.Repeat([]byte{0x22}, 32)
	pub.DurationEpochs = 1
	one, err := NewPiece(bytes.Repeat([]byte{0x04}, 32), 1024)
	if err != nil {
		t.Fatal(err)
	}
	pub.Pieces = []Piece{one}
	const public = "0a2c6f72616d613139726c34636d32686d7238616679346b6c6470787a33666b61346a677571306135747570307318022220222222222222222222222222222222222222222222222222222222222222222230033a043130303040014a290a20040404040404040404040404040404040404040404040404040404040404040410011801208008"
	if hex.EncodeToString(EncodeDeal(pub)) != public {
		t.Fatalf("public %x", EncodeDeal(pub))
	}
	ext := Extend{Signer: sampleDeal().Signer, DealID: 7, ExtraEpochs: 9}
	const extend = "0a2c6f72616d613139726c34636d32686d7238616679346b6c6470787a33666b61346a677571306135747570307310071809"
	if hex.EncodeToString(EncodeExtend(ext)) != extend {
		t.Fatalf("extend %x", EncodeExtend(ext))
	}
	act := SlotAct{Signer: sampleDeal().Signer, NodeID: "node-a", DealID: 7}
	const accept = "0a2c6f72616d613139726c34636d32686d7238616679346b6c6470787a33666b61346a677571306135747570307312066e6f64652d611807"
	if hex.EncodeToString(EncodeAccept(act)) != accept {
		t.Fatalf("accept %x", EncodeAccept(act))
	}
	if hex.EncodeToString(EncodeDecline(act)) != accept {
		t.Fatal("an empty decline reason must match accept")
	}
	act.Slot = 2
	act.Reason = "full"
	const decline = "0a2c6f72616d613139726c34636d32686d7238616679346b6c6470787a33666b61346a677571306135747570307312066e6f64652d61180720022a0466756c6c"
	if hex.EncodeToString(EncodeDecline(act)) != decline {
		t.Fatalf("decline %x", EncodeDecline(act))
	}
	act.Slot = 1
	act.Reason = ""
	if hex.EncodeToString(EncodeAccept(act)) != accept+"2001" {
		t.Fatalf("slot %x", EncodeAccept(act))
	}
}

func TestValidateDeal_refusesWrongShape(t *testing.T) {
	if err := ValidateDeal(sampleDeal()); err != nil {
		t.Fatal(err)
	}
	d := sampleDeal()
	d.Class = 3
	if err := ValidateDeal(d); err == nil {
		t.Fatal("archive")
	}
	d = sampleDeal()
	d.Pieces = d.Pieces[:1]
	if err := ValidateDeal(d); err == nil {
		t.Fatal("private piece count")
	}
	d = sampleDeal()
	d.Class = DealClassPublicPin
	if err := ValidateDeal(d); err == nil {
		t.Fatal("public piece count")
	}
	d = sampleDeal()
	d.Nonce = d.Nonce[:31]
	if err := ValidateDeal(d); err == nil {
		t.Fatal("nonce")
	}
	d = sampleDeal()
	d.RepairDelegate = "has space"
	if err := ValidateDeal(d); err == nil {
		t.Fatal("delegate")
	}
	d = sampleDeal()
	d.Pieces[0].RealLeafCount = 9
	if err := ValidateDeal(d); err == nil {
		t.Fatal("leaf count")
	}
	if _, err := ParseDealClass("archive"); err == nil {
		t.Fatal("parse archive")
	}
	class, err := ParseDealClass("public-pin")
	if err != nil || class != DealClassPublicPin {
		t.Fatal(class, err)
	}
	if _, err := ParsePieceSpec("abcd:1024"); err == nil {
		t.Fatal("short root")
	}
	spec := hex.EncodeToString(bytes.Repeat([]byte{0x01}, 32)) + ":1025"
	p, err := ParsePieceSpec(spec)
	if err != nil || p.RealLeafCount != 2 || p.PaddedLeafCount != 2 {
		t.Fatalf("piece %+v %v", p, err)
	}
	three, err := NewPiece(bytes.Repeat([]byte{0x01}, 32), 3072)
	if err != nil || three.RealLeafCount != 3 || three.PaddedLeafCount != 4 {
		t.Fatalf("three %+v %v", three, err)
	}
}

func TestDealRulesMatchTheChainModule(t *testing.T) {
	keys, err := os.ReadFile("../../testdata/repo/chain/x/storage/types/keys.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"NonceLen = 32",
		"RootLen = 32",
		"MinReplicas uint32 = 3",
		"MaxReplicas uint32 = 32",
		"MaxDurationEpochs uint64 = 1_000_000",
		"MaxNodeIDLen = 128",
	} {
		if !bytes.Contains(keys, []byte(want)) {
			t.Errorf("keys.go no longer contains %q", want)
		}
	}
	msgs, err := os.ReadFile("../../testdata/repo/chain/x/storage/types/msgs.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(msgs, []byte("user deals must be PRIVATE or PUBLIC_PIN")) {
		t.Fatal("create-deal class rule changed")
	}
	piece, err := os.ReadFile("../../testdata/repo/chain/piece/piece.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(piece, []byte("LeafSize = 1024")) {
		t.Fatal("piece leaf size changed")
	}
	tx, err := os.ReadFile("../../testdata/repo/chain/x/storage/types/tx.pb.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`proto.RegisterType((*MsgCreateDeal)(nil), "orama.storage.v1.MsgCreateDeal")`,
		`proto.RegisterType((*MsgExtendDeal)(nil), "orama.storage.v1.MsgExtendDeal")`,
		`proto.RegisterType((*MsgAcceptDeal)(nil), "orama.storage.v1.MsgAcceptDeal")`,
		`proto.RegisterType((*MsgDeclineDeal)(nil), "orama.storage.v1.MsgDeclineDeal")`,
	} {
		if !strings.Contains(string(tx), want) {
			t.Errorf("tx.pb.go no longer contains %q", want)
		}
	}
}
