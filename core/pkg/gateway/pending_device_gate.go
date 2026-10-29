package gateway

import (
	"net/http"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/routepolicy"
)

// A pending device's restricted session (the function device policy,
// feat-973) reaches what the namespace opened to it and nothing else.
//
// The decision is the route's declared policy, made in one place for every
// route: a route that does not say it admits pending devices refuses them
// (routepolicy.PendingRefused is the zero value), so a route added later is
// closed to them until somebody decides otherwise. The routes that admit one
// are the session's own (refresh, logout, whoami, attesting its device) and
// the function invoke and socket routes, where the invoker runs only a
// function deployed open to pending devices.
//
// It runs where the caller's identity enters the request: inside authMiddleware
// on the gateway that serves the request, whether the identity came from the
// token or from a proxy hop, and in proxyToNamespaceGateway before a request is
// forwarded, so a refused request goes no further than the first gateway.

// pendingDeviceGate refuses a pending device's session on every route that
// does not admit one.
func (g *Gateway) pendingDeviceGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := r.Context().Value(ctxKeyJWT).(*auth.JWTClaims)
		if !g.admitsPendingDevice(w, r, claims) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// admitsPendingDevice reports whether the request may go on, writing the
// refusal when it may not. Any caller that is not a pending device may.
func (g *Gateway) admitsPendingDevice(w http.ResponseWriter, r *http.Request, claims *auth.JWTClaims) bool {
	if !claims.PendingDevice() {
		return true
	}
	if g.policyFor(r).PendingDevice != routepolicy.PendingRefused {
		return true
	}
	forbidden(w, CodeDevicePendingApproval, auth.ErrDevicePendingApproval.Error(), map[string]any{"device_id": claims.Did})
	return false
}
