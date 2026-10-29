package pubsub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gwauth "github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/ctxkeys"
	"github.com/Orama-Network/orama/core/pkg/gateway/wssession"
	"github.com/Orama-Network/orama/core/pkg/logging"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

type denyAll struct{}

func (denyAll) Revoked(*gwauth.JWTClaims) (bool, error) { return true, nil }
func (denyAll) RefreshRevocations(context.Context)      {}
func (denyAll) RevocationsUsable() bool                 { return true }
func (denyAll) RevokedKey(int64) (bool, error)          { return true, nil }
func (denyAll) DeviceRevoked(*gwauth.JWTClaims) bool    { return false }

// subscribe opens a real subscriber socket authorized by claims, and returns it
// with the registry it was registered in.
func subscribe(t *testing.T, claims *gwauth.JWTClaims) (*websocket.Conn, *wssession.Registry) {
	t.Helper()
	return subscribeWith(t, claims, 0)
}

// subscribeWith opens a subscriber socket authorized by claims or, when claims
// is nil, by the bare API key with registry id keyID (0: no credential).
func subscribeWith(t *testing.T, claims *gwauth.JWTClaims, keyID int64) (*websocket.Conn, *wssession.Registry) {
	t.Helper()
	sessions := wssession.NewRegistry(nil)
	p := NewPubSubHandlers(&mockNetworkClient{pubsub: &mockPubSubClient{}}, sessions,
		&logging.ColoredLogger{Logger: zap.NewNop()})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), ctxkeys.NamespaceOverride, "anchat")
		if claims != nil {
			ctx = context.WithValue(ctx, ctxkeys.JWT, claims)
		}
		if keyID > 0 {
			ctx = context.WithValue(ctx, ctxkeys.KeyID, keyID)
		}
		p.WebsocketHandler(w, r.WithContext(ctx))
	}))
	t.Cleanup(srv.Close)

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/pubsub/ws?topic=chat", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	deadline := time.Now().Add(5 * time.Second)
	for sessions.Len() == 0 && (claims != nil || keyID > 0) {
		if time.Now().After(deadline) {
			t.Fatal("the subscriber socket was never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return conn, sessions
}

func closeCode(t *testing.T, conn *websocket.Conn) int {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue
		}
		var ce *websocket.CloseError
		if errors.As(err, &ce) {
			return ce.Code
		}
		t.Fatalf("the socket ended without a close frame: %v", err)
	}
}

// The bug: a subscriber socket had no expiry and no revocation check at all.
func TestWebsocketHandler_aRevokedSessionClosesTheSubscriberSocket(t *testing.T) {
	now := time.Now()
	conn, sessions := subscribe(t, &gwauth.JWTClaims{
		Sub: "0xwallet", Jti: "j", Iat: now.Unix(), Exp: now.Add(15 * time.Minute).Unix(),
	})

	sessions.Sweep(now, denyAll{})

	if code := closeCode(t, conn); code != wssession.CloseRevoked {
		t.Errorf("closed with %d, want %d", code, wssession.CloseRevoked)
	}
}

func TestWebsocketHandler_anExpiredTokenClosesTheSubscriberSocket(t *testing.T) {
	now := time.Now()
	conn, sessions := subscribe(t, &gwauth.JWTClaims{
		Sub: "0xwallet", Jti: "j", Iat: now.Unix(), Exp: now.Add(time.Minute).Unix(),
	})

	sessions.Sweep(now.Add(time.Minute+wssession.ExpiryGrace+time.Second), revokedNothing{})

	if code := closeCode(t, conn); code != wssession.CloseExpired {
		t.Errorf("closed with %d, want %d", code, wssession.CloseExpired)
	}
}

// bug-702: a socket opened with a bare API key is held to the key, and
// revoking the key closes it, saying so.
func TestWebsocketHandler_aRevokedKeyClosesTheSubscriberSocket(t *testing.T) {
	conn, sessions := subscribeWith(t, nil, 42)

	sessions.Sweep(time.Now(), denyAll{})

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue
		}
		var ce *websocket.CloseError
		if !errors.As(err, &ce) {
			t.Fatalf("the socket ended without a close frame: %v", err)
		}
		if ce.Code != wssession.CloseRevoked || ce.Text != wssession.ReasonKeyRevoked {
			t.Errorf("closed with %d %q, want %d %q", ce.Code, ce.Text, wssession.CloseRevoked, wssession.ReasonKeyRevoked)
		}
		return
	}
}

// A socket opened with no credential at all (a public topic route) has
// nothing to revoke, and is not registered.
func TestWebsocketHandler_aSocketWithNoCredentialIsNotRegistered(t *testing.T) {
	_, sessions := subscribe(t, nil)
	if sessions.Len() != 0 {
		t.Errorf("%d sockets registered for a request with no credential", sessions.Len())
	}
}

type revokedNothing struct{}

func (revokedNothing) Revoked(*gwauth.JWTClaims) (bool, error) { return false, nil }
func (revokedNothing) RefreshRevocations(context.Context)      {}
func (revokedNothing) RevocationsUsable() bool                 { return true }
func (revokedNothing) RevokedKey(int64) (bool, error)          { return false, nil }
func (revokedNothing) DeviceRevoked(*gwauth.JWTClaims) bool    { return false }
