package clusterreg

import (
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

// RootWallet's cross-language ORAMA vector: MsgSend on orama-stagenet-2.
const vectorSignDocHex = "0ab1010a92010a1c2f636f736d6f732e62616e6b2e763162657461312e4d736753656e6412720a2c6f72616d613139726c34636d32686d7238616679346b6c6470787a33666b61346a6775713061357475703073122c6f72616d613139726c34636d32686d7238616679346b6c6470787a33666b61346a67757130613574757030731a140a066e6f72616d61120a31353030303030303030121a726f6f7477616c6c6574206f72616d6120747820766563746f7212680a500a460a1f2f636f736d6f732e63727970746f2e736563703235366b312e5075624b657912230a21024f4e2ad99c34d60b9ba6283c9431a8418af8673212961f97a77b6377fcd05b6212040a020801180312140a0e0a066e6f72616d6112043230303010c09a0c1a106f72616d612d73746167656e65742d322007"

const vectorAddress = "orama19rl4cm2hmr8afy4kldpxz3fka4jguq0a5tup0s"
const vectorPubKey = "024f4e2ad99c34d60b9ba6283c9431a8418af8673212961f97a77b6377fcd05b62"

func TestEncodeMsgSendSignDoc_matchesRootWalletVector(t *testing.T) {
	pub, err := hex.DecodeString(vectorPubKey)
	if err != nil {
		t.Fatal(err)
	}
	got := EncodeMsgSendSignDoc(vectorAddress, vectorAddress, "1500000000", "rootwallet orama tx vector", pub, 3, "2000", 200000, "orama-stagenet-2", 7)
	want, err := hex.DecodeString(vectorSignDocHex)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("sign doc\n got %x\nwant %x", got, want)
	}
}

func TestCanonicalAccount_vectorAddress(t *testing.T) {
	got, err := CanonicalAccount(vectorAddress)
	if err != nil {
		t.Fatal(err)
	}
	if got != vectorAddress {
		t.Fatalf("canonical %s", got)
	}
	if _, err := CanonicalAccount("cosmos1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqnrql8a"); err == nil {
		t.Fatal("a cosmos address was accepted")
	}
}

func TestEncodeRegisterCluster_matchesChainMarshal(t *testing.T) {
	const want = "0a2c6f72616d613139726c34636d32686d7238616679346b6c6470787a33666b61346a677571306135747570307312096d79636c75737465721a13636c75737465722e6578616d706c652e636f6d221b68747470733a2f2f636c75737465722e6578616d706c652e636f6d"
	got := EncodeRegisterCluster(Registration{
		Operator: vectorAddress, ClusterID: "mycluster", BaseDomain: "cluster.example.com",
		Endpoints: []string{"https://cluster.example.com"},
	})
	if hex.EncodeToString(got) != want {
		t.Fatalf("wire %x", got)
	}
}

func TestValidate_refusesAPrivateEndpoint(t *testing.T) {
	r := Registration{
		Operator: vectorAddress, ClusterID: "mycluster", BaseDomain: "cluster.example.com",
		Endpoints: []string{"https://10.0.0.2"},
	}
	if err := Validate(r); err == nil {
		t.Fatal("a private endpoint was accepted")
	}
	r.Endpoints = []string{"https://cluster.example.com", "https://user:pw@cluster.example.com"}
	if err := Validate(r); err == nil || !strings.Contains(err.Error(), "userinfo") {
		t.Fatalf("userinfo: %v", err)
	}
	r.Endpoints = []string{"https://cluster.example.com"}
	r.MetadataURI = "http://cluster.example.com/meta"
	if err := Validate(r); err == nil {
		t.Fatal("an http metadata URI was accepted")
	}
	r.MetadataURI = ""
	if err := Validate(r); err != nil {
		t.Fatal(err)
	}
}

func TestRulesMatchTheChainModule(t *testing.T) {
	data, err := os.ReadFile("../../testdata/repo/chain/x/nodes/types/validate.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		idPattern.String(),
		"MaxEndpointLen = 256",
		"MaxDomainLen = 253",
		"MaxMetadataURILen = 256",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("chain validate.go no longer contains %q", want)
		}
	}
	params, err := os.ReadFile("../../testdata/repo/chain/x/nodes/types/params.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(params, []byte("absoluteEndpointCap uint32 = 64")) {
		t.Fatal("chain endpoint cap is no longer 64")
	}
	msgs, err := os.ReadFile("../../testdata/repo/chain/x/nodes/types/tx.pb.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(msgs, []byte(`proto.RegisterType((*MsgRegisterCluster)(nil), "orama.nodes.v1.MsgRegisterCluster")`)) {
		t.Fatal("MsgRegisterCluster type URL changed")
	}
}

// The timeout height is in the signed body (TxBody field 3), so a node cannot hold the
// transaction and release it later.
func TestDirect_timeoutHeightIsInTheSignedBodyAndAbsentWhenZero(t *testing.T) {
	pub, _ := hex.DecodeString(vectorPubKey)
	d := Direct{TypeURL: "/x.Msg", Msg: []byte{1}, PubKey: pub, FeeAmount: "5", Gas: 9, ChainID: "orama-1"}
	body := func(d Direct) map[protowire.Number]uint64 {
		doc, err := d.SignDoc()
		if err != nil {
			t.Fatal(err)
		}
		raw := fieldBytes(t, doc, 1)
		got := map[protowire.Number]uint64{}
		for len(raw) > 0 {
			num, typ, n := protowire.ConsumeTag(raw)
			raw = raw[n:]
			if typ == protowire.VarintType {
				v, n := protowire.ConsumeVarint(raw)
				got[num] = v
				raw = raw[n:]
				continue
			}
			raw = raw[protowire.ConsumeFieldValue(num, typ, raw):]
		}
		return got
	}
	if _, set := body(d)[3]; set {
		t.Error("a zero timeout height was encoded")
	}
	d.TimeoutHeight = 12345
	if got := body(d)[3]; got != 12345 {
		t.Errorf("timeout height in the body = %d, want 12345", got)
	}
	signed, _ := d.SignDoc()
	d.TimeoutHeight = 12346
	other, _ := d.SignDoc()
	if bytes.Equal(signed, other) {
		t.Error("the timeout height is not covered by the sign document")
	}
}

func fieldBytes(t *testing.T, msg []byte, want protowire.Number) []byte {
	t.Helper()
	for len(msg) > 0 {
		num, typ, n := protowire.ConsumeTag(msg)
		msg = msg[n:]
		if num == want && typ == protowire.BytesType {
			v, _ := protowire.ConsumeBytes(msg)
			return v
		}
		msg = msg[protowire.ConsumeFieldValue(num, typ, msg):]
	}
	t.Fatalf("field %d not found", want)
	return nil
}

// The cluster registration's own sign input carries the timeout height into the body too.
func TestSignInput_passesTheTimeoutHeightThrough(t *testing.T) {
	pub, _ := hex.DecodeString(vectorPubKey)
	in := SignInput{
		Registration: Registration{Operator: vectorAddress, ClusterID: "mycluster", BaseDomain: "cluster.example.com", Endpoints: []string{"https://cluster.example.com"}},
		PubKey:       pub, FeeAmount: "5", Gas: 9, ChainID: "orama-1", TimeoutHeight: 777,
	}
	doc, err := in.SignDoc()
	if err != nil {
		t.Fatal(err)
	}
	if got := timeoutOf(t, fieldBytes(t, doc, 1)); got != 777 {
		t.Errorf("sign document timeout height = %d, want 777", got)
	}
	raw, err := in.TxRaw(make([]byte, 64))
	if err != nil {
		t.Fatal(err)
	}
	if got := timeoutOf(t, fieldBytes(t, raw, 1)); got != 777 {
		t.Errorf("tx body timeout height = %d, want 777", got)
	}
	in.TimeoutHeight = 0
	doc, _ = in.SignDoc()
	if got := timeoutOf(t, fieldBytes(t, doc, 1)); got != 0 {
		t.Errorf("no timeout height was encoded as %d", got)
	}
}

// timeoutOf is TxBody.timeout_height of an encoded body.
func timeoutOf(t *testing.T, body []byte) uint64 {
	t.Helper()
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		body = body[n:]
		if num == 3 && typ == protowire.VarintType {
			v, _ := protowire.ConsumeVarint(body)
			return v
		}
		body = body[protowire.ConsumeFieldValue(num, typ, body):]
	}
	return 0
}
