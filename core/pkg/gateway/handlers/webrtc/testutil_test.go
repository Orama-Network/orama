package webrtc

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/migrations"
	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/ctxkeys"
	"github.com/Orama-Network/orama/core/pkg/rqlite/rqlitetest"
)

const (
	testTURNSecret = "test-secret-key-32bytes-long!!!!"
	testUser       = "0xalice"
)

// admissionDDL is the real migration, so the tests run against the schema that
// ships, preceded by the tracker table the migration records itself in.
func admissionDDL(t *testing.T) []string {
	t.Helper()
	stmts := []string{`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`}
	for _, name := range []string{"073_webrtc_admissions.sql", "075_webrtc_admission_generation.sql", "094_webrtc_video_mute.sql"} {
		ddl, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatalf("read migration: %v", err)
		}
		stmts = append(stmts, string(ddl))
	}
	return stmts
}

// newSQLiteStore is a store over in-memory SQLite and a clock the test moves.
func newSQLiteStore(t *testing.T) (*AdmissionStore, *sql.DB, *time.Time) {
	t.Helper()
	client, db := rqlitetest.SQLite(t, admissionDDL(t)...)
	now := time.Unix(1_800_000_000, 0)
	s := NewAdmissionStore(client)
	s.now = func() time.Time { return now }
	return s, db, &now
}

// asCaller returns r as the auth middleware leaves it for a validated token.
func asCaller(r *http.Request, sub, device string) *http.Request {
	claims := &auth.JWTClaims{Sub: sub, Did: device, Exp: time.Now().Add(time.Hour).Unix()}
	return r.WithContext(context.WithValue(r.Context(), ctxkeys.JWT, claims))
}

// asAccountCaller is asCaller for a token whose namespace named the account the
// session belongs to (the auth.AccountIDClaim custom claim).
func asAccountCaller(r *http.Request, sub, account, device string) *http.Request {
	claims := &auth.JWTClaims{Sub: sub, Did: device, Exp: time.Now().Add(time.Hour).Unix(),
		Custom: map[string]string{auth.AccountIDClaim: account}}
	return r.WithContext(context.WithValue(r.Context(), ctxkeys.JWT, claims))
}

// withAdmissions gives h a store (fresh, admission not required) and returns it.
func withAdmissions(t *testing.T, h *WebRTCHandlers) *AdmissionStore {
	t.Helper()
	s, _, _ := newSQLiteStore(t)
	s.now = time.Now
	h.SetAdmissionStore(s)
	return s
}

// requestWithNamespaceOf sets only the namespace on r.
func requestWithNamespaceOf(r *http.Request, namespace string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxkeys.NamespaceOverride, namespace))
}
