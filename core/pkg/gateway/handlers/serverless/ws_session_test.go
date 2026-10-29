package serverless

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/ctxkeys"
	"github.com/Orama-Network/orama/core/pkg/gateway/wssession"
	"github.com/Orama-Network/orama/core/pkg/serverless"
	"github.com/gorilla/websocket"
)

type denyAll struct{}

func (denyAll) Revoked(*auth.JWTClaims) (bool, error) { return true, nil }
func (denyAll) RefreshRevocations(context.Context)    {}
func (denyAll) RevocationsUsable() bool               { return true }
func (denyAll) RevokedKey(int64) (bool, error)        { return true, nil }
func (denyAll) DeviceRevoked(*auth.JWTClaims) bool    { return false }

func wsURL(srv *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + path
}

func readCloseCode(t *testing.T, conn *websocket.Conn) int {
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

func waitRegistered(t *testing.T, sessions *wssession.Registry, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for sessions.Len() != n {
		if time.Now().After(deadline) {
			t.Fatalf("%d sockets registered, want %d", sessions.Len(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The bug: a stateless function socket invoked every frame with the identity
// captured at upgrade, and nothing ever ended it — not the token expiring, not
// the session being revoked.
func TestHandleWebSocket_aRevokedSessionClosesTheStatelessSocket(t *testing.T) {
	h := newTestHandlers(nil)
	sessions := h.sessions

	now := time.Now()
	claims := &auth.JWTClaims{Sub: "0xwallet", Namespace: "anchat", Jti: "j",
		Iat: now.Unix(), Exp: now.Add(15 * time.Minute).Unix()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), ctxkeys.NamespaceOverride, "anchat")
		ctx = context.WithValue(ctx, ctxkeys.JWT, claims)
		h.HandleWebSocket(w, r.WithContext(ctx), "rpc", 0)
	}))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(srv, "/v1/functions/rpc/ws"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	waitRegistered(t, sessions, 1)

	sessions.Sweep(now, denyAll{})

	if code := readCloseCode(t, conn); code != wssession.CloseRevoked {
		t.Errorf("closed with %d, want %d", code, wssession.CloseRevoked)
	}
	// The handler's own teardown unregisters it.
	waitRegistered(t, sessions, 0)
}

// serverConn upgrades one connection and hands the server side back.
func serverConn(t *testing.T) (server, client *websocket.Conn) {
	t.Helper()
	got := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		got <- c
	}))
	t.Cleanup(srv.Close)

	client, _, err := websocket.DefaultDialer.Dial(wsURL(srv, "/"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	select {
	case server = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the server never saw the connection")
	}
	t.Cleanup(func() { _ = server.Close() })
	return server, client
}

func readAck(t *testing.T, conn *websocket.Conn) oramaControlAck {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	var ack oramaControlAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatalf("decode ack %q: %v", raw, err)
	}
	return ack
}

// The bug: auth.refresh on an open persistent socket installed a token for any
// subject in the namespace, so the socket — opened, and ws_open'd, for one
// account — went on running as another.
func TestHandleAuthRefresh_refusesATokenForAnotherSubject(t *testing.T) {
	now := time.Now()
	sessions := wssession.NewRegistry(nil)
	sock := sessions.Register(&auth.JWTClaims{Sub: "0xalice", Namespace: "anchat", Jti: "a",
		Iat: now.Unix(), Exp: now.Add(time.Minute).Unix()}, func(int, string) {})
	h := newTestHandlers(nil)
	h.SetJWTVerifier(&fakeJWTVerifier{claims: &auth.JWTClaims{Sub: "0xmallory", Namespace: "anchat",
		Jti: "m", Iat: now.Unix(), Exp: now.Add(time.Hour).Unix()}})
	server, client := serverConn(t)

	// No instance is passed: a refused refresh must not reach it.
	fn := &serverless.Function{Name: "rpc", Namespace: "anchat"}
	err := h.handleAuthRefresh(context.Background(), oramaControlFrame{Type: "auth.refresh", JWT: "x.y.z"}, fn, nil, sock, "anchat", "c1", server)
	if err != nil {
		t.Fatalf("ack write: %v", err)
	}

	ack := readAck(t, client)
	if ack.OK {
		t.Fatal("a token for another subject was installed on an open socket")
	}
	if !strings.Contains(ack.Error, "different subject") {
		t.Errorf("ack error %q does not say why", ack.Error)
	}
	if got := sock.Claims(); got.Sub != "0xalice" || got.Jti != "a" {
		t.Errorf("the socket now answers to %+v", got)
	}
}

// A socket opened with an API key, or with no credential on a public function,
// has no token: taking one on would give it an identity it was not opened
// with.
func TestHandleAuthRefresh_refusesATokenOnAKeylessSocket(t *testing.T) {
	now := time.Now()
	h := newTestHandlers(nil)
	h.SetJWTVerifier(&fakeJWTVerifier{claims: &auth.JWTClaims{Sub: "0xalice", Namespace: "anchat",
		Iat: now.Unix(), Exp: now.Add(time.Hour).Unix()}})
	server, client := serverConn(t)

	fn := &serverless.Function{Name: "rpc", Namespace: "anchat"}
	if err := h.handleAuthRefresh(context.Background(), oramaControlFrame{Type: "auth.refresh", JWT: "x.y.z"}, fn, nil, nil, "anchat", "c1", server); err != nil {
		t.Fatalf("ack write: %v", err)
	}
	if ack := readAck(t, client); ack.OK {
		t.Fatal("a keyless socket took on a token's identity")
	}
}

// The revocation list being unreadable says nothing about the token, and its
// error names why the registry could not be read — internal addresses. The ack
// the client sees is constant and retryable, not the refusal of an invalid token.
func TestHandleAuthRefresh_anUnreadableRevocationListAcksARetryableConstant(t *testing.T) {
	sessions := wssession.NewRegistry(nil)
	now := time.Now()
	sock := sessions.Register(&auth.JWTClaims{Sub: "0xalice", Namespace: "anchat", Jti: "a",
		Iat: now.Unix(), Exp: now.Add(time.Minute).Unix()}, func(int, string) {})
	h := newTestHandlers(nil)
	h.SetJWTVerifier(&fakeJWTVerifier{err: fmt.Errorf("%w: dial tcp 10.0.0.7:4001: connection refused",
		auth.ErrRevocationsUnavailable)})
	server, client := serverConn(t)

	fn := &serverless.Function{Name: "rpc", Namespace: "anchat"}
	if err := h.handleAuthRefresh(context.Background(), oramaControlFrame{Type: "auth.refresh", JWT: "x.y.z"}, fn, nil, sock, "anchat", "c1", server); err != nil {
		t.Fatalf("ack write: %v", err)
	}
	ack := readAck(t, client)
	if ack.OK {
		t.Fatal("a token was installed on an unknown revocation state")
	}
	if ack.Error != authRefreshUnavailableMessage {
		t.Errorf("ack error = %q, want the constant %q", ack.Error, authRefreshUnavailableMessage)
	}
	if strings.Contains(ack.Error, "10.0.0") || strings.Contains(ack.Error, "invalid") {
		t.Errorf("ack error %q leaks the cause or calls the token invalid", ack.Error)
	}
	if got := sock.Claims(); got.Jti != "a" {
		t.Errorf("the socket's token changed to %+v", got)
	}
}
