package hostfunctions

import (
	"context"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/serverless"
)

// feat-971: a nested call names the function that made it, so a target that
// declares invocable_by can tell its router from a client, and carries the
// caller's context unchanged.
func TestNestedInvoke_namesTheParentAndKeepsTheCaller(t *testing.T) {
	att := &serverless.CallerAttestation{}
	ctx := serverless.WithInvocationContext(context.Background(), &serverless.InvocationContext{
		Namespace:         "anchat",
		FunctionName:      "rpc-router",
		RequestID:         "req-1",
		CallerWallet:      "0xwallet",
		CallerJWTSubject:  "0xsubject",
		CallerDeviceID:    "device-1",
		CallerAttestation: att,
		CallerHasInvoke:   true,
	})
	check := func(t *testing.T, req *serverless.InvokeRequest) {
		t.Helper()
		if req.ParentFunction != "rpc-router" {
			t.Errorf("ParentFunction = %q, want rpc-router", req.ParentFunction)
		}
		if req.CallerJWTSubject != "0xsubject" || req.CallerDeviceID != "device-1" ||
			req.CallerAttestation != att || req.CallerWallet != "0xwallet" || !req.CallerHasInvoke {
			t.Errorf("the child lost the caller's context: %+v", req)
		}
	}

	t.Run("function_invoke", func(t *testing.T) {
		inv := &recordingInvoker{}
		if _, err := newAsyncHF(inv, 4).FunctionInvoke(ctx, "send-message", nil); err != nil {
			t.Fatal(err)
		}
		check(t, inv.reqs[0])
	})
	t.Run("function_invoke_async", func(t *testing.T) {
		inv := &recordingInvoker{called: make(chan *serverless.InvokeRequest, 1)}
		if err := newAsyncHF(inv, 4).FunctionInvokeAsync(ctx, "send-message", nil); err != nil {
			t.Fatal(err)
		}
		select {
		case req := <-inv.called:
			check(t, req)
		case <-time.After(2 * time.Second):
			t.Fatal("the target was never invoked")
		}
	})
}
