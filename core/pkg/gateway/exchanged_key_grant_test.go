package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Orama-Network/orama/core/pkg/client"
	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/ctxkeys"
	"github.com/Orama-Network/orama/core/pkg/logging"
)

// A key's grant is recorded under the key's stored form, and an exchanged
// token's subject is that form already. The grant lookup hashed the subject a
// second time, so a key presented as a token held no grant in its own
// namespace: an admin key was refused key management with OWNERSHIP_REQUIRED,
// which the CLI reads as "only the namespace's owner may do this"
// (change-975). The CLI sends key management a token, never the key.

const exchangedKeyRaw = "orama_sk_exchangedKeyTestPayload_0000"

// storedKeyRegistry holds one service-account grant, under one identifier.
type storedKeyRegistry struct {
	client.DatabaseClient
	client.NetworkClient

	identifier string
	role       auth.Role
	// looked records every identifier a grant was looked up under.
	looked []string
}

func (s *storedKeyRegistry) Database() client.DatabaseClient { return s }

func (s *storedKeyRegistry) Query(_ context.Context, query string, args ...interface{}) (*client.QueryResult, error) {
	switch {
	case strings.Contains(query, "INSERT OR IGNORE INTO namespaces"):
		return &client.QueryResult{Count: 1}, nil
	case strings.Contains(query, "SELECT id FROM namespaces"):
		return &client.QueryResult{Count: 1, Rows: [][]interface{}{{int64(1)}}}, nil
	case strings.Contains(query, "SELECT g.role, g.resource"):
		if len(args) < 3 {
			return &client.QueryResult{}, nil
		}
		id, _ := args[2].(string)
		s.looked = append(s.looked, id)
		if args[1] != string(auth.PrincipalServiceAccount) || id != s.identifier {
			return &client.QueryResult{}, nil
		}
		return &client.QueryResult{Count: 1, Rows: [][]interface{}{
			{string(s.role), "", "", "", "", ""},
		}}, nil
	}
	return &client.QueryResult{}, nil
}

// storedKeyGateway is an index gateway whose registry records a key of the
// given role under its HMAC, the way IssueScopedKey writes it.
func storedKeyGateway(t *testing.T, role auth.Role) (*Gateway, *storedKeyRegistry) {
	t.Helper()
	logger, _ := logging.NewColoredLogger(logging.ComponentGateway, false)
	registry := &storedKeyRegistry{role: role}
	svc, err := auth.NewService(logger, registry, "", "anchat", "test-hmac-secret")
	if err != nil {
		t.Fatalf("auth service: %v", err)
	}
	registry.identifier = svc.HashAPIKey(exchangedKeyRaw)
	if registry.identifier == exchangedKeyRaw {
		t.Fatal("the test needs an HMAC secret: with none, the stored form is the raw key and the bug cannot show")
	}
	return &Gateway{
		logger:      logger,
		client:      registry,
		authService: svc,
		cfg:         &Config{BaseDomain: "dbrs.space"},
	}, registry
}

// keyTokenRequest is a request carrying the token /v1/auth/token exchanged
// for the key, as the auth middleware leaves it: the subject is the key's
// stored form and the key's scopes come from its row.
func keyTokenRequest(g *Gateway, method, path, scopes string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	ctx := context.WithValue(r.Context(), ctxkeys.JWT, &auth.JWTClaims{Sub: g.authService.HashAPIKey(exchangedKeyRaw)})
	ctx = context.WithValue(ctx, ctxkeys.NamespaceOverride, "anchat")
	ctx = context.WithValue(ctx, ctxkeys.Scopes, auth.ParseScopes(scopes))
	return r.WithContext(ctx)
}

// bareKeyRequest is the same key sent as X-API-Key.
func bareKeyRequest(method, path, scopes string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	ctx := context.WithValue(r.Context(), ctxkeys.APIKey, exchangedKeyRaw)
	ctx = context.WithValue(ctx, ctxkeys.NamespaceOverride, "anchat")
	ctx = context.WithValue(ctx, ctxkeys.Scopes, auth.ParseScopes(scopes))
	return r.WithContext(ctx)
}

func TestAuthorizationMiddleware_anAdminKeyTokenManagesKeys(t *testing.T) {
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/namespace/keys"},
		{http.MethodPost, "/v1/namespace/keys"},
		{http.MethodDelete, "/v1/namespace/keys/7"},
		{http.MethodGet, "/v1/namespace/members"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			g, registry := storedKeyGateway(t, auth.RoleAdmin)
			chain, reached := controlChain(g)

			w := httptest.NewRecorder()
			chain.ServeHTTP(w, keyTokenRequest(g, tc.method, tc.path, auth.ScopeAdmin))

			if !*reached {
				t.Fatalf("an admin key's token was refused: %d %s (grant looked up under %v, recorded under %q)",
					w.Code, strings.TrimSpace(w.Body.String()), registry.looked, registry.identifier)
			}
		})
	}
}

func TestAuthorizationMiddleware_anAdminKeySentBareManagesKeys(t *testing.T) {
	g, registry := storedKeyGateway(t, auth.RoleAdmin)
	chain, reached := controlChain(g)

	w := httptest.NewRecorder()
	chain.ServeHTTP(w, bareKeyRequest(http.MethodGet, "/v1/namespace/keys", auth.ScopeAdmin))

	if !*reached {
		t.Fatalf("an admin key sent as X-API-Key was refused: %d %s (looked up under %v)",
			w.Code, strings.TrimSpace(w.Body.String()), registry.looked)
	}
	for _, id := range registry.looked {
		if id == exchangedKeyRaw {
			t.Fatal("the grant was looked up under the raw key")
		}
	}
}

// Finding a key's grant must not widen what it holds: a storage key belongs to
// the namespace and still may not manage its keys.
func TestAuthorizationMiddleware_aStorageKeyTokenDoesNotManageKeys(t *testing.T) {
	g, _ := storedKeyGateway(t, auth.RoleRuntime)
	chain, reached := controlChain(g)

	w := httptest.NewRecorder()
	chain.ServeHTTP(w, keyTokenRequest(g, http.MethodGet, "/v1/namespace/keys", auth.ScopeStorage))

	if *reached || w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), CodeScopeMissing) {
		t.Fatalf("reached %v, status %d, body %s; want 403 %s", *reached, w.Code,
			strings.TrimSpace(w.Body.String()), CodeScopeMissing)
	}
}

// lookupRequestGrant, which routes that do not require a grant use to apply a
// narrowed one, reads an exchanged key's grant under the subject as it is.
func TestLookupRequestGrant_findsAnExchangedKeysGrant(t *testing.T) {
	g, registry := storedKeyGateway(t, auth.RoleRuntime)

	grant, err := g.lookupRequestGrant(keyTokenRequest(g, http.MethodPost, "/v1/storage/upload", auth.ScopeStorage))
	if err != nil {
		t.Fatalf("lookupRequestGrant: %v", err)
	}
	if grant == nil || grant.PrincipalType != auth.PrincipalServiceAccount {
		t.Fatalf("an exchanged key's grant was not found (looked up under %v, recorded under %q)",
			registry.looked, registry.identifier)
	}
}
