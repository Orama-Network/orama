//go:build e2e_fleet

package serverless

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Orama-Network/orama/e2e/features/internal/tenancy"
	"github.com/Orama-Network/orama/e2e/harness/eventually"
	"github.com/Orama-Network/orama/e2e/harness/gw"
)

// bug-963: a persistent socket's frame order against function_invoke_async,
// where an invocation's duration went, and a deployed function's first call.
// website/src/docs/developer/functions.mdx, "Frame order on a persistent
// socket" and "Invocation Logging".

const (
	routerDir = "testdata/wsrouter"
	// burstOps ops sent together on one socket, each opReads sequential reads:
	// I/O-bound, like the ops of a chat app's router, so overlap shows as wall
	// time saved rather than as CPU contention.
	burstOps = 20
	opReads  = 40
	// overlapFactor: async ops must finish in under a third of the time the
	// same ops take one after another. Ten slots give about burstOps/10
	// rounds, so a third leaves room for a busy runner.
	overlapFactor = 3
	burstBudget   = 2 * time.Minute
	// prewarmBudget is the prewarm interval (15 s) plus a compile on a
	// loaded node.
	prewarmBudget = 90 * time.Second
	// warmLoadMS bounds a first call's load_ms once its gateway compiled the
	// function ahead: the compiled code is read back from disk and the WASM
	// from the node's own IPFS, against one to two seconds for a compile.
	warmLoadMS = 400
	logsBudget = time.Minute
	// prewarmLogLine is what a namespace gateway logs after a sweep that
	// compiled something (core/pkg/serverless/prewarm.go).
	prewarmLogLine = "prewarm: compiled functions ahead of their first call"
)

// invocationRow is one record of GET /v1/functions/{name}/logs.
type invocationRow struct {
	RequestID  string `json:"request_id"`
	DurationMS int64  `json:"duration_ms"`
	NodeID     string `json:"node_id"`
	QueueMS    int64  `json:"queue_ms"`
	LoadMS     int64  `json:"load_ms"`
	DBCalls    int64  `json:"db_calls"`
	DBMS       int64  `json:"db_ms"`
}

func invocations(t *testing.T, fx *fixture, fn string) []invocationRow {
	t.Helper()
	r := fx.c.MustSend(t, gw.Req{Path: "/v1/functions/" + url.PathEscape(fn) + "/logs", Query: url.Values{"limit": {"200"}}, Bearer: fx.admin}).Expect(t, http.StatusOK)
	var out struct {
		Invocations []invocationRow `json:"invocations"`
	}
	if err := r.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Invocations
}

// burst sends burstOps frames of mode at once on conn and returns how long the
// last answer took and the order the answers came in.
func burst(t *testing.T, conn *websocket.Conn, fn, mode string) (time.Duration, []string) {
	t.Helper()
	start := time.Now()
	for i := 0; i < burstOps; i++ {
		if err := conn.WriteJSON(map[string]any{"fn": fn, "mode": mode, "key": fmt.Sprint(i), "count": opReads}); err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.SetReadDeadline(time.Now().Add(burstBudget)); err != nil {
		t.Fatal(err)
	}
	order := make([]string, 0, burstOps)
	for len(order) < burstOps {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("%s: answer %d of %d: %v", mode, len(order)+1, burstOps, err)
		}
		var a struct {
			ID    string `json:"id"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(msg, &a); err != nil || a.Error != "" || a.ID == "" {
			t.Fatalf("%s: answer %s (%v)", mode, msg, err)
		}
		order = append(order, a.ID)
	}
	return time.Since(start), order
}

// TestPersistentSocket_frameOrderAndAsyncDispatch: the frames of one
// persistent socket run one at a time in arrival order, so ops run with
// function_invoke answer in order and the last after the sum of all of them;
// the same ops handed to function_invoke_async overlap in the namespace's
// execution slots, and the last answer comes well before that sum.
func TestPersistentSocket_frameOrderAndAsyncDispatch(t *testing.T) {
	t.Parallel()
	fx := setup(t)
	const router, op = "e2e-wsrouter", "e2e-wsop"
	deploy(t, fx, fnSpec{name: op})
	deploy(t, fx, fnSpec{name: router, src: routerDir, yaml: "ws_persistent: true\n"})
	member := tenancy.Members(t, fx.f, fx.n.Name)[0]
	conn, _, err := fx.c.PinTo(member.PublicIP).DialWS(t.Context(), "/v1/functions/"+router+"/ws", fx.admin, nil)
	if err != nil {
		t.Fatalf("opening the router socket: %v", err)
	}
	defer conn.Close()

	// Load both modules on this gateway first, so neither burst pays a compile.
	for _, mode := range []string{"sync", "async"} {
		if err := conn.WriteJSON(map[string]any{"fn": op, "mode": mode, "key": "warm", "count": 1}); err != nil {
			t.Fatal(err)
		}
		if err := conn.SetReadDeadline(time.Now().Add(burstBudget)); err != nil {
			t.Fatal(err)
		}
		if _, msg, err := conn.ReadMessage(); err != nil {
			t.Fatalf("warm-up %s: %v", mode, err)
		} else if string(msg) == "" {
			t.Fatalf("warm-up %s: empty answer", mode)
		}
	}

	serial, order := burst(t, conn, op, "sync")
	for i, id := range order {
		if id != fmt.Sprint(i) {
			t.Errorf("function_invoke ops answered in the order %v, want the order they were sent", order)
			break
		}
	}
	async, _ := burst(t, conn, op, "async")
	t.Logf("%d ops of %d reads on one socket: function_invoke %v, function_invoke_async %v", burstOps, opReads, serial, async)
	if async*overlapFactor >= serial {
		t.Errorf("function_invoke_async ops took %v against %v one after another: they did not overlap (want under a %dth)", async, serial, overlapFactor)
	}
}

// TestInvocationRecord_saysWhereTheDurationWent: every record names the node
// whose gateway ran it and counts the database calls it made, with their time
// inside the duration.
func TestInvocationRecord_saysWhereTheDurationWent(t *testing.T) {
	t.Parallel()
	fx := setup(t)
	const fn, reads = "e2e-phases", 3
	deploy(t, fx, fnSpec{name: fn})
	sent := map[string]bool{}
	for _, m := range tenancy.Members(t, fx.f, fx.n.Name) {
		r := invoke(t, fx.c.PinTo(m.PublicIP), fn, fx.admin, map[string]any{"op": "reads", "count": reads}).Expect(t, http.StatusOK)
		sent[r.Header.Get("X-Request-ID")] = true
	}
	var rows []invocationRow
	eventually.Require(t, pollEvery, logsBudget, "the invocation records to be written", func() (bool, error) {
		rows = rows[:0]
		for _, row := range invocations(t, fx, fn) {
			if sent[row.RequestID] {
				rows = append(rows, row)
			}
		}
		if len(rows) == len(sent) {
			return true, nil
		}
		return false, fmt.Errorf("%d of %d records", len(rows), len(sent))
	})
	nodes := map[string]bool{}
	for _, row := range rows {
		if row.NodeID == "" {
			t.Errorf("record %s names no node", row.RequestID)
		}
		nodes[row.NodeID] = true
		if row.DBCalls != reads {
			t.Errorf("record %s: %d database calls, want %d", row.RequestID, row.DBCalls, reads)
		}
		if row.QueueMS+row.LoadMS+row.DBMS > row.DurationMS {
			t.Errorf("record %s: queue %d + load %d + db %d ms exceed the duration %d ms", row.RequestID, row.QueueMS, row.LoadMS, row.DBMS, row.DurationMS)
		}
	}
	t.Logf("records %+v from %d node(s)", rows, len(nodes))
}

// TestPrewarm_firstCallOnEveryGatewayReadsCompiledCode: after a deploy each
// gateway of the namespace compiles the function ahead of its first call, so
// that call loads it in milliseconds instead of compiling it.
func TestPrewarm_firstCallOnEveryGatewayReadsCompiledCode(t *testing.T) {
	t.Parallel()
	fx := setup(t)
	const fn = "e2e-prewarm"
	members := tenancy.Members(t, fx.f, fx.n.Name)
	unit := "orama-namespace-gateway@" + fx.n.Name
	since := time.Now()
	deploy(t, fx, fnSpec{name: fn})
	// Each gateway logs its sweep; wait for every member's before any call,
	// so no call can be the one that compiles.
	eventually.Require(t, pollEvery, prewarmBudget, "every gateway of the namespace to compile the deploy ahead", func() (bool, error) {
		for _, m := range members {
			if !strings.Contains(fx.f.Journal(t, m, unit, since), prewarmLogLine) {
				return false, fmt.Errorf("%s has not logged %q", m.Name, prewarmLogLine)
			}
		}
		return true, nil
	})
	first := map[string]invocationRow{}
	eventually.Require(t, pollEvery, logsBudget, "a first call recorded by every gateway", func() (bool, error) {
		for _, m := range members {
			invoke(t, fx.c.PinTo(m.PublicIP), fn, fx.admin, map[string]any{"op": "echo"}).Expect(t, http.StatusOK)
		}
		rows := invocations(t, fx, fn)
		for i := len(rows) - 1; i >= 0; i-- { // oldest first
			if _, seen := first[rows[i].NodeID]; !seen && rows[i].NodeID != "" {
				first[rows[i].NodeID] = rows[i]
			}
		}
		if len(first) == len(members) {
			return true, nil
		}
		return false, fmt.Errorf("records from %d of %d gateways", len(first), len(members))
	})
	for node, row := range first {
		if row.LoadMS > warmLoadMS {
			t.Errorf("the first call on %s loaded the module in %d ms, want under %d: it was not compiled ahead", node, row.LoadMS, warmLoadMS)
		}
	}
	t.Logf("first call per gateway: %+v", first)
}
