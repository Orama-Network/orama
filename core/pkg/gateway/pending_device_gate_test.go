package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/routepolicy"
)

// feat-973: a pending device's restricted session reaches the routes that act
// on its own session and the function routes, and nothing else.

// pendingDeviceRoutes is the reviewed set of declared patterns a pending
// device's session reaches. Adding one should be a diff somebody reads.
var pendingDeviceRoutes = map[string]routepolicy.PendingDeviceAccess{
	"/.well-known/jwks.json": routepolicy.PendingOwnSession,
	"/v1/attest/android":     routepolicy.PendingOwnSession,
	"/v1/attest/challenge":   routepolicy.PendingOwnSession,
	"/v1/attest/ios":         routepolicy.PendingOwnSession,
	"/v1/auth/challenge":     routepolicy.PendingOwnSession,
	"/v1/auth/jwks":          routepolicy.PendingOwnSession,
	"/v1/auth/logout":        routepolicy.PendingOwnSession,
	"/v1/auth/refresh":       routepolicy.PendingOwnSession,
	"/v1/auth/verify":        routepolicy.PendingOwnSession,
	"/v1/auth/whoami":        routepolicy.PendingOwnSession,
	"/v1/invoke/":            routepolicy.PendingFunctionInvoke,
}

func TestRoutePolicy_aPendingDeviceReachesOnlyTheReviewedRoutes(t *testing.T) {
	methods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch}
	for _, pattern := range gatewayRoutes.Patterns() {
		for _, method := range methods {
			got := policyOf(method, pattern).PendingDevice
			want := pendingDeviceRoutes[pattern]
			if got != want {
				t.Errorf("%s %s: a pending device is %s, want %s. If that is intended, change "+
					"pendingDeviceRoutes — opening a route to pending devices should be a diff somebody reads.",
					method, pattern, got, want)
			}
		}
	}
	for pattern := range pendingDeviceRoutes {
		if !gatewayRoutes.Declared(pattern) {
			t.Errorf("%s is in pendingDeviceRoutes and is not a declared route", pattern)
		}
	}
}

// The function routes are one dynamic pattern: invoking and the socket admit a
// pending device (the invoker and the socket handler decide by the function),
// managing a function does not.
func TestRoutePolicy_aPendingDeviceReachesAFunctionOnlyToRunIt(t *testing.T) {
	for _, c := range []struct {
		method, path string
		want         routepolicy.PendingDeviceAccess
	}{
		{http.MethodPost, "/v1/functions/device-join/invoke", routepolicy.PendingFunctionInvoke},
		{http.MethodGet, "/v1/functions/device-join/ws", routepolicy.PendingFunctionInvoke},
		{http.MethodGet, "/v1/functions/device-join", routepolicy.PendingRefused},
		{http.MethodDelete, "/v1/functions/device-join", routepolicy.PendingRefused},
		{http.MethodGet, "/v1/functions/device-join/logs", routepolicy.PendingRefused},
		{http.MethodPost, "/v1/functions", routepolicy.PendingRefused},
	} {
		if got := policyOf(c.method, c.path).PendingDevice; got != c.want {
			t.Errorf("%s %s: a pending device is %s, want %s", c.method, c.path, got, c.want)
		}
	}
}

func pendingDeviceRequest(method, path string, claims *auth.JWTClaims) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	return r.WithContext(context.WithValue(r.Context(), ctxKeyJWT, claims))
}

// The gate refuses a pending session on every route class it is not opened to,
// with the code a client acts on and the device it is about.
func TestPendingDeviceGate_refusesEverythingElse(t *testing.T) {
	g := &Gateway{}
	pending := &auth.JWTClaims{Sub: "0xwallet", Namespace: "anchat", Did: "device-2", Aud: auth.AudiencePendingDevice}
	reached := false
	gate := g.pendingDeviceGate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	refused := []struct{ method, path string }{
		{http.MethodGet, "/v1/auth/devices"},
		{http.MethodPost, "/v1/auth/devices/device-1/revoke"},
		{http.MethodGet, "/v1/auth/sessions"},
		{http.MethodGet, "/v1/pubsub/ws"},
		{http.MethodPost, "/v1/pubsub/publish"},
		{http.MethodPost, "/v1/storage/upload"},
		{http.MethodGet, "/v1/storage/get/bafy"},
		{http.MethodPost, "/v1/webrtc/rooms"},
		{http.MethodPost, "/v1/push/send"},
		{http.MethodPost, "/v1/cache/get"},
		{http.MethodPost, "/v1/rqlite/query"},
		{http.MethodPost, "/v1/auth/token"},
		{http.MethodGet, "/v1/functions/device-join/logs"},
		{http.MethodGet, "/v1/nothing-is-here"},
	}
	for _, c := range refused {
		reached = false
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, pendingDeviceRequest(c.method, c.path, pending))
		if reached || rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: a pending device got through (status %d)", c.method, c.path, rec.Code)
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s %s: body %q: %v", c.method, c.path, rec.Body.String(), err)
		}
		if !strings.Contains(rec.Body.String(), CodeDevicePendingApproval) || !strings.Contains(rec.Body.String(), "device-2") {
			t.Errorf("%s %s: refusal %s does not carry %s and the device", c.method, c.path, rec.Body.String(), CodeDevicePendingApproval)
		}
	}

	admitted := []struct{ method, path string }{
		{http.MethodPost, "/v1/auth/refresh"},
		{http.MethodPost, "/v1/auth/logout"},
		{http.MethodGet, "/v1/auth/whoami"},
		{http.MethodPost, "/v1/attest/challenge"},
		{http.MethodPost, "/v1/functions/device-join/invoke"},
		{http.MethodGet, "/v1/functions/device-join/ws"},
		{http.MethodPost, "/v1/invoke/anchat/device-join"},
	}
	for _, c := range admitted {
		reached = false
		gate.ServeHTTP(httptest.NewRecorder(), pendingDeviceRequest(c.method, c.path, pending))
		if !reached {
			t.Errorf("%s %s: a pending device was refused a route open to it", c.method, c.path)
		}
	}

	// Anybody else is not the gate's business.
	for _, claims := range []*auth.JWTClaims{nil, {Sub: "0xwallet", Did: "device-1", Aud: auth.AudienceGateway}} {
		reached = false
		gate.ServeHTTP(httptest.NewRecorder(), pendingDeviceRequest(http.MethodGet, "/v1/auth/devices", claims))
		if !reached {
			t.Errorf("claims %+v were refused by the pending-device gate", claims)
		}
	}
}
