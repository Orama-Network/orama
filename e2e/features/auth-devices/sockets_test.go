//go:build e2e_fleet

package authdevices

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Orama-Network/orama/e2e/harness"
	"github.com/Orama-Network/orama/e2e/harness/eventually"
	"github.com/Orama-Network/orama/e2e/harness/gw"
	"github.com/Orama-Network/orama/e2e/harness/ns"
	"github.com/Orama-Network/orama/e2e/harness/wallet"
)

const (
	pathPubsubWS = "/v1/pubsub/ws"
	pathKeys     = "/v1/namespace/keys"
	// closeRevoked: "the token, its session or its subject was revoked"
	// (docs/whitepaper/technical-reference/vol1/13-identity.md#open-websockets).
	closeRevoked = 4403
	// closeDeviceRevoked: "the device the token is bound to was revoked"
	// (docs/whitepaper/technical-reference/vol1/13-identity.md#open-websockets).
	closeDeviceRevoked = 4410
	// sweepBudget: sockets are re-checked every 10 seconds; a close frame
	// and the round trip come on top.
	sweepBudget = revocationStaleness + stalenessSlack
)

// subscribe opens a pub/sub subscription on c with bearer (header), or with
// a query credential when query is set.
func subscribe(t testing.TB, c *gw.Client, bearer string, query url.Values) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	if query == nil {
		query = url.Values{}
	}
	query.Set("topic", "e2e-auth")
	conn, resp, err := c.For(t).DialWS(t.Context(), pathPubsubWS+"?"+query.Encode(), bearer, nil)
	if conn != nil {
		t.Cleanup(func() { conn.Close() })
	}
	return conn, resp, err
}

// watched is an open socket with a goroutine reading it from the moment it
// opens. The server pings and drops a connection that does not answer within
// 75s, without a close frame; a socket nobody reads never answers, so a test
// that reads only when it asks for the close code would see 1006 instead of
// the server's close.
type watched struct {
	*websocket.Conn
	// closed receives the error that ended the read loop.
	closed chan error
}

func mustSubscribe(t testing.TB, c *gw.Client, bearer string, query url.Values) *watched {
	t.Helper()
	conn, resp, err := subscribe(t, c, bearer, query)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("subscribing: HTTP %d: %v", status, err)
	}
	w := &watched{Conn: conn, closed: make(chan error, 1)}
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				w.closed <- err
				return
			}
		}
	}()
	return w
}

// closeCode waits up to within for the server to close the socket and returns
// the close code (0 when the socket stayed open). An end without a close frame
// fails the test.
func (w *watched) closeCode(t testing.TB, within time.Duration) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), within)
	defer cancel()
	select {
	case err := <-w.closed:
		var ce *websocket.CloseError
		if errors.As(err, &ce) {
			return ce.Code
		}
		t.Fatalf("socket failed without a close frame: %v", err)
	case <-ctx.Done():
	}
	return 0
}

// TestSocketRevocation_closesWith4403: a subscription opened with a token is
// closed with 4403 within ten seconds when that token is revoked (logout) or
// its session is ended, and with 4410 when its device is revoked — on the
// public and on the namespace gateway (docs/whitepaper/technical-reference/vol1/13-identity.md#open-websockets).
func TestSocketRevocation_closesWith4403(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	w := member(t, n, roleRuntime)
	closesWith := map[string]int{"logout": closeRevoked, "session ended": closeRevoked, "device revoked": closeDeviceRevoked}
	revokers := map[string]func(s *gw.Session, d *wallet.Device, plain *gw.Session){
		"logout": func(s *gw.Session, _ *wallet.Device, _ *gw.Session) {
			if _, err := c.For(t).Logout(t.Context(), s.AccessToken, s.RefreshToken, n.Name, false); err != nil {
				t.Fatal(err)
			}
		},
		"session ended": func(s *gw.Session, _ *wallet.Device, plain *gw.Session) {
			sid := sessionIDOf(t, c, plain.AccessToken, s.DeviceID)
			if _, err := c.For(t).EndSession(t.Context(), plain.AccessToken, sid, nil); err != nil {
				t.Fatal(err)
			}
		},
		"device revoked": func(_ *gw.Session, d *wallet.Device, plain *gw.Session) {
			if _, err := c.For(t).RevokeDevice(t.Context(), plain.AccessToken, d.ID(), nil); err != nil {
				t.Fatal(err)
			}
		},
	}
	plain := signInSession(t, c, w, n.Name, nil)
	for name, revoke := range revokers {
		d := gw.NewDevice(t, wallet.AlgEd25519)
		s := signInSession(t, c, w, n.Name, d)
		viaMain := mustSubscribe(t, c, s.AccessToken, nil)
		viaNS := mustSubscribe(t, n.Client, s.AccessToken, nil)
		revoke(s, d, plain)
		for gwName, conn := range map[string]*watched{"public gateway": viaMain, "namespace gateway": viaNS} {
			if code := conn.closeCode(t, sweepBudget); code != closesWith[name] {
				t.Errorf("%s, %s: socket closed with %d, want %d within %s", name, gwName, code, closesWith[name], sweepBudget)
			}
		}
	}
}

// sessionIDOf finds the listed session bound to deviceID.
func sessionIDOf(t testing.TB, c *gw.Client, bearer, deviceID string) int64 {
	t.Helper()
	sessions, _, err := c.For(t).Sessions(t.Context(), bearer)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if s.DeviceID == deviceID && deviceID != "" {
			return s.ID
		}
	}
	t.Fatalf("no session bound to device %s", deviceID)
	return 0
}

// TestSocketRevocation_revokedKeyClosesItsSockets: a socket opened with an
// API key is held to the key; revoking the key refuses its next upgrade and
// closes the open socket with 4403 (bug-702,
// docs/whitepaper/technical-reference/vol1/13-identity.md#open-websockets).
// ?jwt= opens a socket with a token where a header cannot be set.
func TestSocketRevocation_revokedKeyClosesItsSockets(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	var minted struct {
		ID     int64  `json:"id"`
		APIKey string `json:"api_key"`
	}
	resp := postJSON(t, c, pathKeys, n.Owner.Token(), map[string]any{"scope": "pubsub", "label": "e2e-socket"})
	if err := resp.Expect(t, http.StatusCreated).Decode(&minted); err != nil {
		t.Fatal(err)
	}
	protect(t, c, minted.APIKey)
	byKey := mustSubscribe(t, c, "", url.Values{"api_key": {minted.APIKey}})
	mustSubscribe(t, c, "", url.Values{"token": {minted.APIKey}}).Close()
	s := signInSession(t, c, member(t, n, roleRuntime), n.Name, nil)
	mustSubscribe(t, c, "", url.Values{"jwt": {s.AccessToken}}).Close()
	send(t, c, http.MethodDelete, pathKeys+"/"+strconv.FormatInt(minted.ID, 10), n.Owner.Token(), nil).Expect(t, http.StatusOK)
	// The DELETE and the upgrade may reach different gateways: each drops
	// the key from its cache within the revocation list's staleness
	// (core/pkg/gateway/middleware.go).
	eventually.Require(t, pollEvery, revocationStaleness+stalenessSlack, "every gateway to refuse the revoked key's upgrade", func() (bool, error) {
		conn, resp, err := subscribe(t, c, "", url.Values{"api_key": {minted.APIKey}})
		if conn != nil {
			conn.Close()
			return false, errors.New("the revoked key opened a new socket")
		}
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			return false, fmt.Errorf("upgrade with the revoked key: want 401, got %v (err %v)", resp, err)
		}
		return true, nil
	})
	if code := byKey.closeCode(t, sweepBudget); code != closeRevoked {
		t.Errorf("the revoked key's open socket: close code %d, want %d", code, closeRevoked)
	}
}
