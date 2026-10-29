package serverless

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/ctxkeys"
	"github.com/Orama-Network/orama/core/pkg/httputil"
	"github.com/Orama-Network/orama/core/pkg/serverless"
	"go.uber.org/zap"
)

// feat-971: a function that declares invocable_by is refused to a client on
// every route that reaches it, by name, before it runs: both HTTP invoke
// routes and its WebSocket.

func routedFn() *serverless.Function {
	return &serverless.Function{ID: "fn-send", Name: "send-message", Namespace: "anchat-test", InvocableBy: []string{"rpc-router"}}
}

// userSession is a signed-in user holding the invoke grant: everything a
// private function asks of a caller.
var userSession = map[any]any{
	ctxkeys.Scopes: auth.ScopeSet{auth.ScopeInvoke: {}},
	ctxkeys.JWT:    &auth.JWTClaims{Sub: "0xUser"},
}

func rpcErrorCode(t *testing.T, rec *httptest.ResponseRecorder) httputil.RPCErrorCode {
	t.Helper()
	var env httputil.RPCErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error == nil {
		t.Fatalf("not an RPC error envelope (%v): %s", err, rec.Body.String())
	}
	return env.Error.Code
}

func TestInvoke_aFunctionOnlyItsParentsMayInvokeIsRefusedToAClientByName(t *testing.T) {
	for name, invoke := range map[string]func(h *ServerlessHandlers, rec *httptest.ResponseRecorder, r *http.Request){
		"POST /v1/functions/{name}/invoke": func(h *ServerlessHandlers, rec *httptest.ResponseRecorder, r *http.Request) {
			h.InvokeFunction(rec, r, "send-message", 0)
		},
		"POST /v1/invoke/{namespace}/{name}": func(h *ServerlessHandlers, rec *httptest.ResponseRecorder, r *http.Request) {
			r.URL.Path = "/v1/invoke/anchat-test/send-message"
			h.HandleInvoke(rec, r)
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := handlersWith(routedFn())
			h.invoker = serverless.NewInvoker(nil, h.registry, nil, "anchat-test", zap.NewNop())
			r := upgradeRequest(userSession)
			r.Method = http.MethodPost
			rec := httptest.NewRecorder()
			invoke(h, rec, r)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
			}
			if code := rpcErrorCode(t, rec); code != httputil.ErrCodeFunctionNotDirectlyInvocable {
				t.Errorf("code = %s, want %s", code, httputil.ErrCodeFunctionNotDirectlyInvocable)
			}
		})
	}
}

// refusingRegistry records the definition a deploy hands it and refuses it as
// the registry refuses a list it cannot accept.
type refusingRegistry struct {
	*mockRegistry
	got *serverless.FunctionDefinition
}

func (r *refusingRegistry) Register(_ context.Context, def *serverless.FunctionDefinition, _ []byte) (*serverless.Function, error) {
	r.got = def
	return nil, &serverless.ValidationError{Field: "invocable_by", Message: `"rpc-routr" is not a function of namespace "anchat-test"; deploy it first`}
}

// The CLI sends invocable_by in the deploy metadata; a list the registry
// refuses is the deployer's 400 naming the field, not a 500 deploy failure.
func TestDeployFunction_invocableByArrivesAndARefusalIsA400(t *testing.T) {
	reg := &refusingRegistry{mockRegistry: newMockRegistry()}
	h := newTestHandlers(reg)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("name", "send-message")
	_ = mw.WriteField("metadata", `{"invocable_by":["rpc-routr"]}`)
	part, _ := mw.CreateFormFile("wasm", "function.wasm")
	_, _ = part.Write([]byte("\x00asm\x01\x00\x00\x00"))
	_ = mw.Close()
	req := asCredentialOf(httptest.NewRequest(http.MethodPost, "/v1/functions", &body), "anchat-test")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.DeployFunction(rec, req)

	if reg.got == nil || len(reg.got.InvocableBy) != 1 || reg.got.InvocableBy[0] != "rpc-routr" {
		t.Fatalf("the registry was handed %+v, want invocable_by [rpc-routr]", reg.got)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if code := rpcErrorCode(t, rec); code != httputil.ErrCodeValidationFailed {
		t.Errorf("code = %s, want %s", code, httputil.ErrCodeValidationFailed)
	}
}

func TestHandleWebSocket_aFunctionOnlyItsParentsMayInvokeIsRefusedBeforeTheUpgrade(t *testing.T) {
	for _, fn := range []*serverless.Function{routedFn(), func() *serverless.Function {
		f := routedFn()
		f.WSPersistent = true
		return f
	}()} {
		h := handlersWith(fn)
		rec := httptest.NewRecorder()
		h.HandleWebSocket(rec, upgradeRequest(userSession), fn.Name, 0)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("persistent=%v: status = %d, want 403", fn.WSPersistent, rec.Code)
		}
		if code := rpcErrorCode(t, rec); code != httputil.ErrCodeFunctionNotDirectlyInvocable {
			t.Errorf("persistent=%v: code = %s, want %s", fn.WSPersistent, code, httputil.ErrCodeFunctionNotDirectlyInvocable)
		}
	}

	// An admin is not refused here; the upgrade then fails on the recorder,
	// which cannot be hijacked.
	h := handlersWith(routedFn())
	rec := httptest.NewRecorder()
	h.HandleWebSocket(rec, upgradeRequest(map[any]any{ctxkeys.Scopes: auth.ScopeSet{auth.ScopeAdmin: {}}}), "send-message", 0)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("an admin was refused: %s", rec.Body.String())
	}
}
