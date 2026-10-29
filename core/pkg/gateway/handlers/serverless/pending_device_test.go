package serverless

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/ctxkeys"
	authhandlers "github.com/Orama-Network/orama/core/pkg/gateway/handlers/auth"
	"github.com/Orama-Network/orama/core/pkg/httputil"
	"github.com/Orama-Network/orama/core/pkg/serverless"
)

// A pending device's restricted session (the function device policy,
// feat-973) runs only the functions open to pending devices.

// The serverless package names the states it is handed without importing the
// gateway's auth package; the two must be the same strings, or a pending
// session would read as some other state to the invoker.
func TestCallerDeviceStates_areTheGatewaysOwn(t *testing.T) {
	if serverless.CallerDevicePending != auth.CallerDevicePending || serverless.CallerDeviceActive != auth.CallerDeviceActive {
		t.Fatalf("serverless states %q/%q differ from auth's %q/%q",
			serverless.CallerDevicePending, serverless.CallerDeviceActive, auth.CallerDevicePending, auth.CallerDeviceActive)
	}
	if string(httputil.ErrCodeDevicePendingApproval) != authhandlers.ErrCodeDevicePendingApproval {
		t.Fatalf("the invoke refusal %q is not the gateway's %q", httputil.ErrCodeDevicePendingApproval, authhandlers.ErrCodeDevicePendingApproval)
	}
}

func pendingClaims() *auth.JWTClaims {
	return &auth.JWTClaims{Sub: "0xwallet", Namespace: "anchat-test", Did: "device-2", Aud: auth.AudiencePendingDevice}
}

func TestDeviceStateFromRequest_isTheTokens(t *testing.T) {
	h := newTestHandlers(newMockRegistry())
	for _, tc := range []struct {
		claims *auth.JWTClaims
		want   string
	}{
		{nil, ""},
		{&auth.JWTClaims{Sub: "0xwallet", Aud: auth.AudienceGateway}, ""},
		{&auth.JWTClaims{Sub: "0xwallet", Did: "device-1", Aud: auth.AudienceGateway}, serverless.CallerDeviceActive},
		{pendingClaims(), serverless.CallerDevicePending},
	} {
		values := map[any]any{}
		if tc.claims != nil {
			values[ctxkeys.JWT] = tc.claims
		}
		if got := h.getDeviceStateFromRequest(upgradeRequest(values)); got != tc.want {
			t.Errorf("claims %+v: device state = %q, want %q", tc.claims, got, tc.want)
		}
	}
}

// A pending session is never an admin, even with the admin scopes on its
// request: admin passes over internal and invocable_by.
func TestCallerIsAdmin_neverForAPendingDevice(t *testing.T) {
	h := newTestHandlers(newMockRegistry())
	r := upgradeRequest(map[any]any{ctxkeys.JWT: pendingClaims(), ctxkeys.Scopes: auth.ScopeSet{auth.ScopeAdmin: {}}})
	if h.getCallerIsAdminFromRequest(r) {
		t.Fatal("a pending device's session counted as an admin")
	}
}

// A persistent socket never reaches the invoker, so a pending session is
// refused one at the upgrade, before any instance is taken, unless the
// function is open to pending devices.
func TestHandleWebSocket_aPendingDeviceIsRefusedAFunctionNotOpenToIt(t *testing.T) {
	for _, persistent := range []bool{true, false} {
		fn := persistentFn("rpc-router", true, false)
		fn.WSPersistent = persistent
		h := handlersWith(fn)

		rec := httptest.NewRecorder()
		h.HandleWebSocket(rec, upgradeRequest(map[any]any{ctxkeys.JWT: pendingClaims()}), "rpc-router", 0)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("persistent=%v: status = %d, want 403", persistent, rec.Code)
		}
		var env httputil.RPCErrorEnvelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("persistent=%v: body %q: %v", persistent, rec.Body.String(), err)
		}
		if env.Error == nil || env.Error.Code != httputil.ErrCodeDevicePendingApproval {
			t.Errorf("persistent=%v: code = %q, want %q", persistent, env.Error.Code, httputil.ErrCodeDevicePendingApproval)
		}
	}
}

// An unknown function is refused too: the pending session learns nothing of
// what the namespace deployed.
func TestHandleWebSocket_aPendingDeviceIsRefusedAnUnknownFunction(t *testing.T) {
	h := newTestHandlers(newMockRegistry())

	rec := httptest.NewRecorder()
	h.HandleWebSocket(rec, upgradeRequest(map[any]any{ctxkeys.JWT: pendingClaims()}), "nothing-here", 0)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// A function open to pending devices takes the socket past the gate (the
// recorder cannot be upgraded, so the upgrade itself fails — after the gate).
func TestHandleWebSocket_aPendingDeviceReachesAFunctionOpenToIt(t *testing.T) {
	fn := persistentFn("device-join", true, false)
	fn.WSPersistent = false
	fn.OpenToPendingDevices = true
	h := handlersWith(fn)

	rec := httptest.NewRecorder()
	h.HandleWebSocket(rec, upgradeRequest(map[any]any{ctxkeys.JWT: pendingClaims()}), "device-join", 0)

	var env httputil.RPCErrorEnvelope
	if json.Unmarshal(rec.Body.Bytes(), &env) == nil && env.Error != nil && env.Error.Code == httputil.ErrCodeDevicePendingApproval {
		t.Fatal("a pending device was refused a function open to pending devices")
	}
}
