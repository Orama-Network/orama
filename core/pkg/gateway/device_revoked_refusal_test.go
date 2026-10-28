package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gwauth "github.com/Orama-Network/orama/core/pkg/gateway/auth"
)

// revokedDeviceEvidence is what the revoker attached to the revocation.
const revokedDeviceEvidence = `{"roster":"v7"}`

// revokedDeviceToken is a token of anchat's 0xwallet bound to device dev-1,
// which the account then revoked with evidence. It is minted before the
// revocation, as every token a revoked device still presents was.
func revokedDeviceToken(t *testing.T) (*Gateway, string, *sql.DB) {
	t.Helper()
	g, _, db := keyRegistry(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519 keygen: %v", err)
	}
	g.authService.SetEdDSAKey(priv, "")
	g.logger = newRQLiteTestLogger()
	g.cfg = &Config{}
	if _, err := db.Exec(`INSERT INTO session_devices(id, namespace_id, subject, public_key, state)
		VALUES ('dev-1', 10, '0xwallet', '{}', 'active')`); err != nil {
		t.Fatalf("enroll the device: %v", err)
	}
	token, _, err := g.authService.GenerateBoundJWT("anchat", "0xwallet", 15*time.Minute, nil,
		gwauth.SessionBinding{DeviceID: "dev-1"})
	if err != nil {
		t.Fatalf("GenerateBoundJWT: %v", err)
	}
	// What Service.RevokeDevice writes: the tombstone, then the list entry.
	if _, err := db.Exec(`UPDATE session_devices
		SET state = 'revoked', revoked_at = datetime('now'), revoked_by = 'account', revocation_evidence = ?
		WHERE id = 'dev-1'`, revokedDeviceEvidence); err != nil {
		t.Fatalf("tombstone the device: %v", err)
	}
	if err := g.authService.Revocations().RevokeDevice(context.Background(), "anchat", "dev-1"); err != nil {
		t.Fatalf("revoke the device: %v", err)
	}
	return g, token, db
}

// assertDeviceRevoked checks rec is the 403 DEVICE_REVOKED of dev-1 with the
// revocation the account recorded.
func assertDeviceRevoked(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		Code       string                         `json:"code"`
		Revocation *gwauth.DeviceRevocationRecord `json:"revocation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if rec.Code != http.StatusForbidden || body.Code != CodeDeviceRevoked {
		t.Fatalf("status %d code %q, want 403 %s: %s", rec.Code, body.Code, CodeDeviceRevoked, rec.Body.String())
	}
	r := body.Revocation
	if r == nil || r.DeviceID != "dev-1" || r.RevokedBy != gwauth.RevokedByAccount ||
		r.Evidence == nil || *r.Evidence != revokedDeviceEvidence || r.RevokedAt == nil {
		t.Errorf("revocation = %+v, want dev-1's, by the account, with its evidence", r)
	}
}

// feat-976: a token refused because its device was revoked was answered
// 401 AUTH_REVOKED, with nothing that told the device it was revoked or who
// revoked it. It is answered DEVICE_REVOKED with the revocation, on a header
// token and on a WebSocket upgrade's ?jwt=, which used to fall through to
// "no credential was presented".
func TestAuthMiddleware_aRevokedDevicesTokenAnswersDeviceRevoked(t *testing.T) {
	g, token, _ := revokedDeviceToken(t)
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the handler ran on a revoked device's token")
	})

	t.Run("bearer", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/v1/rpc", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		g.authMiddleware(next).ServeHTTP(rec, r)
		assertDeviceRevoked(t, rec)
	})
	t.Run("function invoke", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/v1/invoke/anchat/rpc", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		g.authMiddleware(next).ServeHTTP(rec, r)
		assertDeviceRevoked(t, rec)
	})
	t.Run("websocket jwt query", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/v1/functions/rpc/ws?jwt="+token, nil)
		r.Header.Set("Connection", "upgrade")
		r.Header.Set("Upgrade", "websocket")
		rec := httptest.NewRecorder()
		g.authMiddleware(next).ServeHTTP(rec, r)
		assertDeviceRevoked(t, rec)
	})
}

// The same token reaching the main gateway in front of the namespace, or a
// serverless route that names no namespace, is refused the same way.
func TestNamespaceProxy_aRevokedDevicesTokenAnswersDeviceRevoked(t *testing.T) {
	g, token, _ := revokedDeviceToken(t)

	t.Run("proxy", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/v1/cache/get", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		g.proxyToNamespaceGateway(rec, r, "anchat", g.namespaceProxyAuthFor(r))
		assertDeviceRevoked(t, rec)
	})
	// An open route serves anonymous callers; a revoked device's token is
	// still refused there, not served as no credential.
	t.Run("proxy, open route", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/v1/invoke/anchat/rpc", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		g.proxyToNamespaceGateway(rec, r, "anchat", g.namespaceProxyAuthFor(r))
		assertDeviceRevoked(t, rec)
	})
	t.Run("serverless by credential", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/v1/functions/rpc/invoke", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		g.routeServerlessByCredential(rec, r, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Error("a revoked device's request was routed")
		}))
		assertDeviceRevoked(t, rec)
	})
}

// A revoked session, token or account is not a revoked device: it stays
// AUTH_REVOKED.
func TestAuthMiddleware_aRevokedSessionStaysAuthRevoked(t *testing.T) {
	g := revocableGateway(t)
	r := httptest.NewRequest(http.MethodPost, "/v1/rpc", nil)
	r.Header.Set("Authorization", "Bearer "+revokedToken(t, g))
	rec := httptest.NewRecorder()
	g.authMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the handler ran on a revoked session's token")
	})).ServeHTTP(rec, r)
	assertAuthCode(t, rec, CodeAuthRevoked)
}

// A device registry that cannot be read when the refusal reads the revocation
// is a retryable 503, not a refusal that tells the device nothing.
func TestAuthMiddleware_aRevokedDeviceWhoseRevocationCannotBeReadIsRetryable(t *testing.T) {
	g, token, db := revokedDeviceToken(t)
	// The revocation list still answers; the device registry does not.
	if _, err := db.Exec(`DROP TABLE session_devices`); err != nil {
		t.Fatalf("break the device registry: %v", err)
	}

	r := httptest.NewRequest(http.MethodPost, "/v1/rpc", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	g.authMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the handler ran on a revoked device's token")
	})).ServeHTTP(rec, r)
	assertUnavailable(t, rec)
}

// feat-976 review: a device the list refuses is told DEVICE_REVOKED only when
// its row says revoked. A row that is not revoked is the registry's read not
// having caught up (503, retryable); a device that is not in the registry, or
// whose namespace is gone, has its token refused as revoked (AUTH_REVOKED).
func TestAuthMiddleware_aDeviceTheRegistryDoesNotHoldRevokedIsNeverToldDeviceRevoked(t *testing.T) {
	serve := func(g *Gateway, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v1/rpc", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		g.authMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Error("the handler ran on a token the list refuses")
		})).ServeHTTP(rec, r)
		return rec
	}

	t.Run("row active", func(t *testing.T) {
		g, token, db := revokedDeviceToken(t)
		if _, err := db.Exec(`UPDATE session_devices SET state = 'active', revoked_by = NULL WHERE id = 'dev-1'`); err != nil {
			t.Fatal(err)
		}
		assertUnavailable(t, serve(g, token))
	})
	t.Run("no row", func(t *testing.T) {
		g, token, db := revokedDeviceToken(t)
		if _, err := db.Exec(`DELETE FROM session_devices WHERE id = 'dev-1'`); err != nil {
			t.Fatal(err)
		}
		assertAuthCode(t, serve(g, token), CodeAuthRevoked)
	})
	t.Run("namespace deleted", func(t *testing.T) {
		g, token, db := revokedDeviceToken(t)
		if _, err := db.Exec(`DELETE FROM namespaces WHERE name = 'anchat'`); err != nil {
			t.Fatal(err)
		}
		assertAuthCode(t, serve(g, token), CodeAuthRevoked)
	})
}
