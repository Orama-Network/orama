//go:build e2e_fleet

package serverless

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Orama-Network/orama/e2e/features/internal/tenancy"
	"github.com/Orama-Network/orama/e2e/harness/gw"
	"github.com/Orama-Network/orama/e2e/harness/wallet"
)

// feat-971: a function deployed with invocable_by runs only when a function it
// names calls it (website/src/docs/developer/functions.mdx#invocable-by).

const (
	codeNotDirectlyInvocable = "FUNCTION_NOT_DIRECTLY_INVOCABLE"
	// codeNestedForbidden is what function_invoke_last_error names a refused
	// nested call (core/pkg/serverless/invoke_last_error.go).
	codeNestedForbidden = "AUTH_FORBIDDEN"
	wsAnswerBudget      = time.Minute
)

// callerContext is what the fixture's whoami reports of the caller, which a
// child must see exactly as its parent does.
func callerContext(m map[string]any) map[string]any {
	return map[string]any{"wallet": m["wallet"], "subject": m["subject"], "device": m["device"], "verdict": m["verdict"]}
}

// TestInvocableBy_onlyTheNamedRoutersReachTheFunction: with invocable_by set, a
// user session is refused 403 FUNCTION_NOT_DIRECTLY_INVOCABLE on POST
// /v1/functions/{name}/invoke, POST /v1/invoke/{ns}/{name} and the function's
// WebSocket; another function's function_invoke is refused; the routers it
// names reach it with function_invoke (stateless and persistent) and
// function_invoke_async, and it sees the caller's subject, device and
// attestation verdict unchanged; an admin still invokes it; list and get show
// the field.
func TestInvocableBy_onlyTheNamedRoutersReachTheFunction(t *testing.T) {
	t.Parallel()
	fx := setup(t)
	const router, wsRouter, other, child = "e2e-ib-router", "e2e-ib-wsrouter", "e2e-ib-other", "e2e-ib-child"
	deploy(t, fx, fnSpec{name: router})
	deploy(t, fx, fnSpec{name: other})
	deploy(t, fx, fnSpec{name: wsRouter, src: routerDir, yaml: "ws_persistent: true\n"})
	deploy(t, fx, fnSpec{name: child, yaml: "invocable_by: [" + router + ", " + wsRouter + "]\n"})

	if out := fx.n.CLI.MustOK(t, "function", "get", child).Stdout; !strings.Contains(out, `"invocable_by"`) || !strings.Contains(out, wsRouter) {
		t.Errorf("function get does not show invocable_by:\n%s", out)
	}
	if out := fx.n.CLI.MustOK(t, "function", "list").Stdout; !strings.Contains(out, router+","+wsRouter) {
		t.Errorf("function list does not show invocable_by:\n%s", out)
	}

	// A runtime member on a device-bound session: everything a private
	// function asks of a caller.
	w, err := wallet.NewEVM()
	if err != nil {
		t.Fatal(err)
	}
	fx.n.CLI.MustOK(t, "members", "add", w.Address(), "--role", roleRuntime)
	dev := gw.NewDevice(t, wallet.AlgEd25519)
	session, err := fx.c.For(t).SignIn(t.Context(), w, fx.n.Name, dev)
	if err != nil {
		t.Fatal(err)
	}
	user := session.AccessToken
	whoami := map[string]any{"op": "whoami"}

	if r := invoke(t, fx.c, child, user, whoami); r.Status != http.StatusForbidden || rpcCode(r) != codeNotDirectlyInvocable {
		t.Errorf("POST /v1/functions/%s/invoke as a user: %d %.300s, want 403 %s", child, r.Status, r.Body, codeNotDirectlyInvocable)
	}
	raw, _ := json.Marshal(whoami)
	r := fx.c.MustSend(t, gw.Req{Method: http.MethodPost, Path: "/v1/invoke/" + fx.n.Name + "/" + child, Bearer: user,
		Header: http.Header{"Content-Type": {"application/json"}}, Body: raw})
	if r.Status != http.StatusForbidden || rpcCode(r) != codeNotDirectlyInvocable {
		t.Errorf("POST /v1/invoke/%s/%s as a user: %d %.300s, want 403 %s", fx.n.Name, child, r.Status, r.Body, codeNotDirectlyInvocable)
	}
	conn, resp, err := fx.c.DialWS(t.Context(), "/v1/functions/"+child+"/ws", user, nil)
	if err == nil {
		conn.Close()
		t.Errorf("the function's WebSocket opened for a user")
	} else if resp == nil {
		t.Errorf("opening the function's WebSocket: %v, want a 403 answer", err)
	} else {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), codeNotDirectlyInvocable) {
			t.Errorf("the function's WebSocket as a user: %d %.300s, want 403 %s", resp.StatusCode, body, codeNotDirectlyInvocable)
		}
	}
	if res := call(t, fx, child, whoami); res["subject"] == "" {
		t.Errorf("an admin's direct invoke ran with no caller: %v", res)
	}

	var direct map[string]any
	if err := invoke(t, fx.c, router, user, whoami).Expect(t, http.StatusOK).Decode(&direct); err != nil {
		t.Fatal(err)
	}
	if direct["device"] != dev.ID() {
		t.Fatalf("the router sees device %v, want %s", direct["device"], dev.ID())
	}
	want := callerContext(direct)
	nested := map[string]any{"op": "nested", "name": child, "data": whoami}

	var viaRouter map[string]any
	if err := invoke(t, fx.c, router, user, nested).Expect(t, http.StatusOK).Decode(&viaRouter); err != nil {
		t.Fatal(err)
	}
	if got := callerContext(sub(viaRouter, "result")); !reflect.DeepEqual(got, want) {
		t.Errorf("function_invoke from %s: the child saw %v, the router %v (last_error %v)", router, got, want, viaRouter["last_error"])
	}

	var viaOther map[string]any
	if err := invoke(t, fx.c, other, user, nested).Expect(t, http.StatusOK).Decode(&viaOther); err != nil {
		t.Fatal(err)
	}
	if last := sub(viaOther, "last_error"); last["code"] != codeNestedForbidden || !strings.Contains(asString(last["error"]), "not directly invocable") {
		t.Errorf("function_invoke from %s, which the child does not name: result %v, last_error %v", other, viaOther["result"], last)
	}

	member := tenancy.Members(t, fx.f, fx.n.Name)[0]
	sock, _, err := fx.c.PinTo(member.PublicIP).DialWS(t.Context(), "/v1/functions/"+wsRouter+"/ws", user, nil)
	if err != nil {
		t.Fatalf("opening the persistent router's socket: %v", err)
	}
	defer sock.Close()
	for _, mode := range []string{"sync", "async"} {
		if got := callerContext(routedWhoami(t, sock, child, mode)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s from the persistent router: the child saw %v, the caller is %v", mode, got, want)
		}
	}
}

// routedWhoami sends the persistent router one frame running the child's
// whoami in mode and returns the answer that reaches the socket: the child's
// output (sync) or what the child sent itself (async).
func routedWhoami(t *testing.T, conn *websocket.Conn, child, mode string) map[string]any {
	t.Helper()
	if err := conn.WriteJSON(map[string]any{"fn": child, "op": "whoami", "mode": mode, "key": mode}); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(wsAnswerBudget)); err != nil {
		t.Fatal(err)
	}
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("%s: no answer: %v", mode, err)
	}
	var out map[string]any
	if err := json.Unmarshal(msg, &out); err != nil || out["error"] != nil {
		t.Fatalf("%s: answer %s (%v)", mode, msg, err)
	}
	return out
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// TestDeploy_refusesAnInvocableByThatCannotBeMeant: a parent the namespace
// does not have, and the function itself, are refused at deploy naming the
// field, and nothing is deployed.
func TestDeploy_refusesAnInvocableByThatCannotBeMeant(t *testing.T) {
	t.Parallel()
	fx := setup(t)
	const name = "e2e-ib-bad"
	for _, parent := range []string{"e2e-ib-no-such-router", name} {
		dir := writeFixture(t, fnSpec{name: name, yaml: "invocable_by: [" + parent + "]\n"})
		res, err := fx.n.CLI.For(t).Run(t.Context(), "function", "deploy", dir)
		if err != nil {
			t.Fatal(err)
		}
		if res.Exit == 0 {
			t.Cleanup(func() { deleteFn(t, fx.n.CLI, name) })
		}
		if res.Exit == 0 || !strings.Contains(res.Stdout+res.Stderr, "invocable_by") {
			t.Errorf("invocable_by [%s] deployed (exit %d): %s %s", parent, res.Exit, res.Stdout, res.Stderr)
		}
	}
}
