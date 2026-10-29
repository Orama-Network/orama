package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/logging"
)

// feat-973: a pending device's session crosses the proxy hop as one (v8), and
// a namespace gateway that predates pending devices sees no caller at all.

func pendingHopClaims() *auth.JWTClaims {
	c := hopClaims()
	c.Aud = auth.AudiencePendingDevice
	c.Did = "device-2"
	c.Sid = "session-2"
	return c
}

func hopChainGateway(t *testing.T) *Gateway {
	t.Helper()
	logger, _ := logging.NewColoredLogger(logging.ComponentGateway, false)
	return &Gateway{logger: logger, internalAuthKey: testHopKey(t), cfg: &Config{ClientNamespace: "anchat"}}
}

// servePendingHop runs the namespace gateway's side of the hop, and returns the
// claims the handler would see.
func servePendingHop(g *Gateway, r *http.Request) *auth.JWTClaims {
	var got *auth.JWTClaims
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = r.Context().Value(ctxKeyJWT).(*auth.JWTClaims)
	})
	g.internalAuthMiddleware(g.authMiddleware(next)).ServeHTTP(httptest.NewRecorder(), r)
	return got
}

func TestHop_aPendingSessionArrivesPending(t *testing.T) {
	g := hopChainGateway(t)
	r := signedJWTHop(t, g.internalAuthKey, pendingHopClaims())
	if r.Header.Get(HeaderInternalAuthJWTSub) != "" || r.Header.Get(HeaderInternalAuthJWTPendingSub) != "0xwallet" {
		t.Fatalf("the hop carries the pending subject in the v1 field: sub=%q pending=%q",
			r.Header.Get(HeaderInternalAuthJWTSub), r.Header.Get(HeaderInternalAuthJWTPendingSub))
	}

	got := servePendingHop(g, r)
	if got == nil || !got.PendingDevice() || got.Sub != "0xwallet" || got.Did != "device-2" {
		t.Fatalf("the namespace gateway rebuilt %+v, want the pending session of device-2", got)
	}
}

// A full session arrives as one, with the gateway audience.
func TestHop_aFullSessionArrivesFull(t *testing.T) {
	g := hopChainGateway(t)
	c := hopClaims()
	c.Did = "device-1"
	got := servePendingHop(g, signedJWTHop(t, g.internalAuthKey, c))
	if got == nil || got.PendingDevice() || got.Aud != auth.AudienceGateway || got.DeviceState() != auth.CallerDeviceActive {
		t.Fatalf("a full session arrived as %+v", got)
	}
}

// What a namespace gateway that predates v8 does: it verifies up to v7 and
// drops the fields only v8 covers. The pending session reaches it with no
// caller, so it runs for it only what it would run for anybody.
func TestHop_anOlderNamespaceGatewaySeesNoCaller(t *testing.T) {
	g := hopChainGateway(t)
	r := signedJWTHop(t, g.internalAuthKey, pendingHopClaims())
	r.Header.Del(HeaderInternalAuthMACv8)

	if got := servePendingHop(g, r); got != nil {
		t.Fatalf("a hop without its v8 MAC gave the handler a caller: %+v", got)
	}
}

// A pending subject nobody signed is dropped; so is one moved into the v1
// field, which the v1 MAC does not cover with that value.
func TestHop_aForgedPendingSubjectIsStripped(t *testing.T) {
	g := hopChainGateway(t)
	r := signedJWTHop(t, g.internalAuthKey, hopClaims())
	r.Header.Del(HeaderInternalAuthJWTSub)
	r.Header.Set(HeaderInternalAuthJWTPendingSub, "0xvictim")
	r.Header.Set(HeaderInternalAuthJWTDid, "device-9")
	if got := servePendingHop(g, r); got != nil && got.Sub == "0xvictim" {
		t.Fatalf("a forged pending subject reached the handler: %+v", got)
	}

	r = signedJWTHop(t, g.internalAuthKey, pendingHopClaims())
	r.Header.Set(HeaderInternalAuthJWTSub, r.Header.Get(HeaderInternalAuthJWTPendingSub))
	r.Header.Del(HeaderInternalAuthJWTPendingSub)
	if got := servePendingHop(g, r); got != nil && !got.PendingDevice() && got.Sub == "0xwallet" {
		t.Fatalf("a pending session promoted itself to a full one across the hop: %+v", got)
	}
}

// Only a device is ever pending.
func TestClaimsFromInternalAuthHeaders_aPendingSubjectNeedsADevice(t *testing.T) {
	h := http.Header{}
	h.Set(HeaderInternalAuthJWTPendingSub, "0xwallet")
	if _, err := claimsFromInternalAuthHeaders(h, "anchat", time.Now()); err == nil {
		t.Fatal("a pending subject with no device was accepted")
	}
}
