package gateway

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Orama-Network/orama/core/migrations"
	"github.com/Orama-Network/orama/core/pkg/gateway/corsorigins"
	"github.com/Orama-Network/orama/core/pkg/rqlite"
	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"
)

// corsGateway is a gateway for base domain orama.test whose namespace anchat
// lists https://app.anchat.example as an allowed origin.
func corsGateway(t *testing.T) *Gateway {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := rqlite.ApplyEmbeddedMigrations(t.Context(), db, migrations.FS, zap.NewNop()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO namespaces(id, name) VALUES (10, 'anchat')`); err != nil {
		t.Fatal(err)
	}
	store := corsorigins.NewStore(rqlite.NewClient(db))
	if err := store.Set(context.Background(), "anchat", []string{"https://app.anchat.example"}, "0xowner"); err != nil {
		t.Fatal(err)
	}
	cache := corsorigins.NewCache(store, nil)
	if err := cache.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &Gateway{cfg: &Config{BaseDomain: "orama.test"}, corsOrigins: cache}
}

func TestCORS_namespaceOriginsReachOnlyTheirNamespacesHost(t *testing.T) {
	g := corsGateway(t)
	handler := g.corsMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a preflight reached the handler")
	}))
	for _, tc := range []struct {
		host, origin string
		admitted     bool
	}{
		{"ns-anchat.orama.test", "https://app.anchat.example", true},
		{"ns-other.orama.test", "https://app.anchat.example", false},
		{"orama.test", "https://app.anchat.example", false},
		{"ns-anchat.orama.test", "https://evil.example", false},
		// Tauri on Windows and Android, and on macOS, Linux and iOS.
		{"ns-other.orama.test", "http://tauri.localhost", true},
		{"ns-other.orama.test", "https://tauri.localhost", true},
		{"ns-other.orama.test", "tauri://localhost", true},
		{"ns-other.orama.test", "http://tauri.localhost.evil.example", false},
	} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodOptions, "/v1/functions/x/invoke", nil)
		r.Host = tc.host
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Access-Control-Request-Method", http.MethodPost)
		handler.ServeHTTP(rec, r)
		got := rec.Header().Get("Access-Control-Allow-Origin") == tc.origin
		if got != tc.admitted {
			t.Errorf("%s from %s: admitted %v, want %v (ACAO %q)", tc.host, tc.origin, got, tc.admitted,
				rec.Header().Get("Access-Control-Allow-Origin"))
		}
	}
}

// The SDK sends X-Orama-Revoke-Key on a fetch-capability revoke and reads
// Retry-After on a 429 or 503; a browser drops both unless the CORS answer
// names them.
func TestCORS_allowsTheHeadersTheSDKSendsAndReads(t *testing.T) {
	g := corsGateway(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodOptions, "/v1/storage/fetch-caps/x", nil)
	r.Host = "orama.test"
	r.Header.Set("Origin", "https://orama.test")
	g.corsMiddleware(http.NotFoundHandler()).ServeHTTP(rec, r)
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != corsAllowHeaders {
		t.Errorf("Allow-Headers = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "Retry-After, Date" {
		t.Errorf("Expose-Headers = %q, want Retry-After, Date", got)
	}
}
