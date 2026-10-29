package chainread

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMethods_listsEveryOramaQuery(t *testing.T) {
	methods, err := Methods()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, m := range methods {
		have[m] = true
	}
	for _, want := range []string{
		"orama.nodes.v1.Query/Node",
		"orama.storage.v1.Query/Deal",
		"orama.fees.v1.Query/Earnings",
		"orama.houses.v1.Query/Proposal",
	} {
		if !have[want] {
			t.Errorf("query %s is missing from the embedded descriptors", want)
		}
	}
}

func TestLookup_errors(t *testing.T) {
	for _, name := range []string{"", "Node", "orama.nodes.v1.Query/Nope", "orama.nope.v1.Query/Node", "orama.nodes.v1.Node/Node"} {
		if _, err := Lookup(name); err == nil {
			t.Errorf("Lookup(%q) succeeded", name)
		}
	}
}

func TestEncodeRequest_wireBytes(t *testing.T) {
	m, err := Lookup("orama.nodes.v1.Query/Node")
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.EncodeRequest(`{"node_id":"abc"}`)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(got) != "0a03616263" {
		t.Fatalf("request bytes = %x", got)
	}
	empty, err := m.EncodeRequest("")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty request = %x, %v", empty, err)
	}
	if _, err := m.EncodeRequest(`{"nope":1}`); err == nil {
		t.Fatal("an unknown request field was accepted")
	}
}

// A dynamicpb message keeps its fields in a Go map: without a deterministic marshal the three
// pagination fields came out in a different order on some calls (fuzz corpus 9527f7f5edf06759).
func TestEncodeRequest_deterministic(t *testing.T) {
	m, err := Lookup("orama.nodes.v1.Query/Nodes")
	if err != nil {
		t.Fatal(err)
	}
	const request = `{"pagination":{"limit":"5","offset":"2","count_total":true}}`
	const encodings = 200
	for i := 0; i < encodings; i++ {
		got, err := m.EncodeRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(got) != "0a06100218052001" {
			t.Fatalf("encoding %d = %x", i, got)
		}
	}
}

func TestDecodeResponse_nodeUsesProtoNames(t *testing.T) {
	m, _ := Lookup("orama.nodes.v1.Query/Node")
	// QueryNodeResponse{node: Node{node_id: "abc", declared_capacity_bytes: 300}}
	got, err := m.DecodeResponse([]byte{0x0a, 0x08, 0x0a, 0x03, 'a', 'b', 'c', 0x48, 0xac, 0x02})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Node struct {
			NodeID   string `json:"node_id"`
			Capacity string `json:"declared_capacity_bytes"`
		} `json:"node"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Node.NodeID != "abc" || decoded.Node.Capacity != "300" {
		t.Fatalf("decoded %s", got)
	}
	if _, err := m.DecodeResponse([]byte{0xff}); err == nil {
		t.Fatal("garbage decoded")
	}
}

// rpcServer answers abci_query with value for the one path it expects.
func rpcServer(t *testing.T, wantPath string, code uint32, log string, value []byte, gotData *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
			Params struct {
				Path string `json:"path"`
				Data string `json:"data"`
			} `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil || r.Method != http.MethodPost {
			t.Errorf("bad request %s %s", r.Method, body)
		}
		if req.Method != "abci_query" || req.Params.Path != wantPath {
			t.Errorf("request = %+v", req)
		}
		if gotData != nil {
			*gotData = req.Params.Data
		}
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{
			"response": map[string]any{"code": code, "log": log, "value": base64.StdEncoding.EncodeToString(value)},
		}})
		w.Write(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGRPC_roundTripThroughAbciQuery(t *testing.T) {
	var data string
	srv := rpcServer(t, "/orama.nodes.v1.Query/Node", 0, "", []byte{0x0a, 0x05, 0x0a, 0x03, 'a', 'b', 'c'}, &data)
	r := &Reader{RPC: srv.URL}

	got, err := r.GRPC(context.Background(), "orama.nodes.v1.Query/Node", `{"node_id":"abc"}`)
	if err != nil {
		t.Fatal(err)
	}
	if data != "0a03616263" {
		t.Fatalf("query data = %q", data)
	}
	if !strings.Contains(string(got), `"node_id":"abc"`) {
		t.Fatalf("response = %s", got)
	}
}

func TestGRPC_chainErrorIsAnError(t *testing.T) {
	srv := rpcServer(t, "/orama.nodes.v1.Query/Node", 5, "node not found", nil, nil)
	_, err := (&Reader{RPC: srv.URL}).GRPC(context.Background(), "orama.nodes.v1.Query/Node", `{"node_id":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "node not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestGRPC_needsRPCAndAKnownQuery(t *testing.T) {
	if _, err := (&Reader{}).GRPC(context.Background(), "orama.nodes.v1.Query/Node", ""); err == nil || !strings.Contains(err.Error(), "--rpc") {
		t.Fatalf("err = %v, want the flag to pass", err)
	}
	if _, err := (&Reader{RPC: "http://127.0.0.1:1"}).GRPC(context.Background(), "orama.nodes.v1.Query/Nope", ""); err == nil {
		t.Fatal("an unknown query was sent")
	}
}

func TestGatewayGet_readsUnderV1Chain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chain/status" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"result":{"sync_info":{"latest_block_height":"7"}}}`))
	}))
	defer srv.Close()
	r := &Reader{Gateway: srv.URL + "/"}
	got, err := r.GatewayGet(context.Background(), "status")
	if err != nil || !strings.Contains(string(got), "latest_block_height") {
		t.Fatalf("got %s, %v", got, err)
	}
	if _, err := r.GatewayGet(context.Background(), "nope"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want the HTTP status", err)
	}
	if _, err := (&Reader{}).GatewayGet(context.Background(), "status"); err == nil || !strings.Contains(err.Error(), "--gateway") {
		t.Fatalf("err = %v, want the flag to pass", err)
	}
}

func TestRPCGet_unwrapsResultAndError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			w.Write([]byte(`{"jsonrpc":"2.0","id":-1,"result":{"node_info":{"network":"orama-test"}}}`))
		default:
			w.Write([]byte(`{"jsonrpc":"2.0","id":-1,"error":{"message":"Internal error","data":"boom"}}`))
		}
	}))
	defer srv.Close()
	r := &Reader{RPC: srv.URL}
	got, err := r.RPCGet(context.Background(), "/status")
	if err != nil || !strings.Contains(string(got), "orama-test") {
		t.Fatalf("got %s, %v", got, err)
	}
	if _, err := r.RPCGet(context.Background(), "/block"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestRESTGet_refusesNonJSONAndOversized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/big" {
			w.Write([]byte(`"` + strings.Repeat("a", responseLimit) + `"`))
			return
		}
		w.Write([]byte("<html>"))
	}))
	defer srv.Close()
	r := &Reader{REST: srv.URL}
	if _, err := r.RESTGet(context.Background(), "/x"); err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.RESTGet(context.Background(), "/big"); err == nil || !strings.Contains(err.Error(), "over") {
		t.Fatalf("err = %v", err)
	}
	if _, err := (&Reader{}).RESTGet(context.Background(), "/x"); err == nil || !strings.Contains(err.Error(), "--node") {
		t.Fatalf("err = %v", err)
	}
}

// The embedded descriptors are generated from chain/proto. When protoc and the
// chain sources are here, a change to a query message that was not followed by
// gen.sh fails this test.
func TestQueriesDescriptor_isCurrent(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc is not installed")
	}
	if _, err := os.Stat("../../testdata/repo/chain/proto/orama"); err != nil {
		t.Skip("chain/proto is not next to core/")
	}
	declareGenInputs(t)
	out := filepath.Join(t.TempDir(), "queries.binpb")
	cmd := exec.Command("bash", "gen.sh")
	cmd.Env = append(os.Environ(), "OUT="+out)
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("gen.sh cannot run here: %v: %s", err, msg)
	}
	fresh, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(fresh) != string(queriesDescriptor) {
		t.Fatal("queries.binpb is stale: run core/pkg/chainread/gen.sh")
	}
}

// declareGenInputs reads what gen.sh reads in processes of its own, which the
// go test cache does not see: the script, chain/go.mod (it pins the cosmos
// protos) and every directory under chain/proto (an opened directory is
// recorded with each entry's size and modification time). Editing any of them
// reruns the freshness test.
func declareGenInputs(t *testing.T) {
	t.Helper()
	for _, name := range []string{"gen.sh", "../../testdata/repo/chain/go.mod"} {
		if _, err := os.ReadFile(name); err != nil {
			t.Fatal(err)
		}
	}
	if err := fs.WalkDir(os.DirFS("../../testdata/repo/chain/proto"), ".", func(string, fs.DirEntry, error) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayQuery_readsTheQueryRouteAndChecksTheRequestFirst(t *testing.T) {
	var gotPath, gotData string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotData = r.URL.Path, r.URL.Query().Get("data")
		w.Write([]byte(`{"node":{"node_id":"n-1"}}`))
	}))
	t.Cleanup(srv.Close)
	r := &Reader{Gateway: srv.URL}
	got, err := r.Query(context.Background(), "orama.nodes.v1.Query/Node", `{"node_id":"n-1"}`)
	if err != nil || !strings.Contains(string(got), `"node_id":"n-1"`) {
		t.Fatalf("got %s err %v", got, err)
	}
	if gotPath != "/v1/chain/query/orama.nodes.v1.Query/Node" || gotData != base64.RawURLEncoding.EncodeToString([]byte{0x0a, 0x03, 'n', '-', '1'}) {
		t.Fatalf("gateway saw %q data %q", gotPath, gotData)
	}
	gotPath = ""
	if _, err := r.GatewayQuery(context.Background(), "orama.nodes.v1.Query/Nope", ""); err == nil {
		t.Fatal("an unknown query was sent")
	}
	if _, err := r.GatewayQuery(context.Background(), "orama.nodes.v1.Query/Node", `{"nope":1}`); err == nil {
		t.Fatal("a malformed request was sent")
	}
	if gotPath != "" {
		t.Fatalf("a refused query reached the gateway: %s", gotPath)
	}
	if _, err := (&Reader{}).Query(context.Background(), "orama.nodes.v1.Query/Node", ""); err == nil || !strings.Contains(err.Error(), "--gateway") {
		t.Fatalf("err = %v, want the gateway flag", err)
	}
}

func TestDecodeRPC_missingKeyIsErrNotFound(t *testing.T) {
	m, _ := Lookup("orama.nodes.v1.Query/Node")
	body := []byte(`{"result":{"response":{"code":22,"codespace":"sdk","log":"key not found"}}}`)
	if _, err := m.DecodeRPC(body); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	other := []byte(`{"result":{"response":{"code":5,"codespace":"sdk","log":"x"}}}`)
	if _, err := m.DecodeRPC(other); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
