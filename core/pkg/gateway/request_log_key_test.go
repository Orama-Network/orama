package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/client"
	gwauth "github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/telemetry/traffic"
)

// bug-944: request_logs.api_key_id names the key that made the request. It is
// the registry id the gateway authenticated, carried out to loggingMiddleware
// and over the proxy hop, never looked up again from the raw key (which never
// matches a hashed api_keys.key, and on a namespace gateway was asked of the
// tenant's stale copy).

const (
	requestLogTestKey    = "ak_logged:alice"
	requestLogTestSecret = "hmac-secret-for-request-logs"
	requestLogTestKeyID  = int64(4072)
)

// hashedKeyRegistry answers a key lookup only for the stored (hashed) form of
// one key, as the registry does when an HMAC secret is configured.
type hashedKeyRegistry struct {
	client.DatabaseClient
	stored string
}

func (d *hashedKeyRegistry) Query(_ context.Context, _ string, args ...interface{}) (*client.QueryResult, error) {
	columns := []string{"namespaces.name", "api_keys.scopes", "api_keys.resources", "api_keys.id"}
	if len(args) == 0 || args[0] != d.stored {
		return &client.QueryResult{Columns: columns}, nil
	}
	return &client.QueryResult{
		Columns: columns,
		Rows:    [][]interface{}{{"alice", "invoke", nil, float64(requestLogTestKeyID)}},
		Count:   1,
	}, nil
}

// keyedMainGateway is a main gateway with an HMAC secret whose registry knows
// requestLogTestKey, and which batches request logs.
func keyedMainGateway(t *testing.T) *Gateway {
	t.Helper()
	g := chainGateway(t, "alice", nil)
	svc, err := gwauth.NewService(g.logger, emptyRegistryNet{}, "", "alice", requestLogTestSecret)
	if err != nil {
		t.Fatalf("auth service: %v", err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	svc.SetEdDSAKey(priv, "")
	g.authService = svc
	// The registry holds only the HMAC of the key, as production does.
	g.client = &fakeNetworkClient{db: &hashedKeyRegistry{stored: gwauth.HmacSHA256Hex(requestLogTestKey, requestLogTestSecret)}}
	g.traffic = traffic.New(nil)
	g.logBatcher = &requestLogBatcher{gw: g, maxBatch: 1000}
	return g
}

// loggedKeyID serves r through loggingMiddleware around inner and returns the
// api_key_id of the one row it queued.
func loggedKeyID(t *testing.T, g *Gateway, inner http.Handler, r *http.Request) int64 {
	t.Helper()
	g.loggingMiddleware(inner).ServeHTTP(httptest.NewRecorder(), r)
	if n := len(g.logBatcher.entries); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	return g.logBatcher.entries[0].apiKeyID
}

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

func TestRequestLog_aBareKeyOnTheGatewayItselfRecordsItsID(t *testing.T) {
	g := keyedMainGateway(t)
	r := chainRequest(http.MethodPost, "/v1/invoke/alice/x", map[string]string{"X-API-Key": requestLogTestKey})
	if got := loggedKeyID(t, g, g.authMiddleware(okHandler), r); got != requestLogTestKeyID {
		t.Errorf("api_key_id = %d, want %d", got, requestLogTestKeyID)
	}
}

func TestRequestLog_anExchangedKeyRecordsItsID(t *testing.T) {
	g := keyedMainGateway(t)
	token, _, err := g.authService.GenerateJWT("alice", g.authService.HashAPIKey(requestLogTestKey), time.Hour,
		map[string]string{"scopes": "invoke"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	r := chainRequest(http.MethodPost, "/v1/invoke/alice/x", map[string]string{"Authorization": "Bearer " + token})
	if got := loggedKeyID(t, g, g.authMiddleware(okHandler), r); got != requestLogTestKeyID {
		t.Errorf("api_key_id = %d, want %d", got, requestLogTestKeyID)
	}
}

func TestRequestLog_aRequestWithNoKeyRecordsNone(t *testing.T) {
	g := keyedMainGateway(t)
	r := chainRequest(http.MethodGet, "/v1/health", nil)
	if got := loggedKeyID(t, g, g.authMiddleware(okHandler), r); got != 0 {
		t.Errorf("api_key_id = %d for a keyless request, want none", got)
	}
}

// The main gateway logs a request it forwards, or refuses before forwarding,
// under the key that made it, and forwards the id for the namespace gateway's
// own row.
func TestRequestLog_theMainGatewayRecordsAndForwardsTheID(t *testing.T) {
	g := keyedMainGateway(t)
	headers := map[string]string{"X-API-Key": requestLogTestKey}

	_, _, key, errMsg := g.validateAuthForNamespaceProxy(chainRequest(http.MethodPost, "/v1/invoke/alice/x", headers))
	if errMsg != "" || key.id != requestLogTestKeyID {
		t.Fatalf("forwarded key id %d (err %q), want %d", key.id, errMsg, requestLogTestKeyID)
	}
	h := http.Header{}
	setInternalAuthKeyHeaders(h, key)
	if got := h.Get(HeaderInternalAuthKeyID); got != strconv.FormatInt(requestLogTestKeyID, 10) {
		t.Errorf("hop header = %q, want %d", got, requestLogTestKeyID)
	}

	// Aimed at another namespace, so it is refused before any proxying.
	proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.proxyToNamespaceGateway(w, r, "bob", g.namespaceProxyAuthFor(r))
	})
	if got := loggedKeyID(t, g, proxy, chainRequest(http.MethodPost, "/v1/invoke/bob/x", headers)); got != requestLogTestKeyID {
		t.Errorf("api_key_id of the refused proxy request = %d, want %d", got, requestLogTestKeyID)
	}
}

// keyIDHop is a bare-key hop that carries a key id.
func keyIDHop(t *testing.T, g *Gateway, id int64) *http.Request {
	t.Helper()
	const path = "/v1/functions/rpc/invoke"
	r := httptest.NewRequest(http.MethodPost, path, nil)
	r.Header.Set(HeaderInternalAuthValidated, "true")
	r.Header.Set(HeaderInternalAuthNamespace, hopNamespace)
	setInternalAuthKeyHeaders(r.Header, keyAuthority{scopes: "invoke", id: id})
	if err := signInternalAuthHeaders(g.internalAuthKey, r.Header, r.Method, path, time.Now()); err != nil {
		t.Fatalf("sign: %v", err)
	}
	return r
}

func loggedNamespaceGateway(t *testing.T) *Gateway {
	t.Helper()
	g, _ := namespaceGatewayForHops(t, "runtime")
	g.traffic = traffic.New(nil)
	g.logBatcher = &requestLogBatcher{gw: g, maxBatch: 1000}
	return g
}

func TestRequestLog_theNamespaceGatewayRecordsTheForwardedID(t *testing.T) {
	g := loggedNamespaceGateway(t)
	inner := g.internalAuthMiddleware(g.loggingMiddleware(g.authMiddleware(okHandler)))
	inner.ServeHTTP(httptest.NewRecorder(), keyIDHop(t, g, requestLogTestKeyID))
	if len(g.logBatcher.entries) != 1 || g.logBatcher.entries[0].apiKeyID != requestLogTestKeyID {
		t.Errorf("rows = %+v, want one with api_key_id %d", g.logBatcher.entries, requestLogTestKeyID)
	}
}

// A main gateway that predates v6 forwards no id the namespace gateway may
// believe: one it carries anyway is dropped, not recorded.
func TestRequestLog_aV5HopRecordsNoID(t *testing.T) {
	g := loggedNamespaceGateway(t)
	r := keyIDHop(t, g, requestLogTestKeyID)
	r.Header.Del(HeaderInternalAuthMACv8)
	r.Header.Del(HeaderInternalAuthMACv7)
	r.Header.Del(HeaderInternalAuthMACv6)
	g.internalAuthMiddleware(g.loggingMiddleware(g.authMiddleware(okHandler))).ServeHTTP(httptest.NewRecorder(), r)
	if len(g.logBatcher.entries) != 1 || g.logBatcher.entries[0].apiKeyID != 0 {
		t.Errorf("rows = %+v, want one with no api_key_id", g.logBatcher.entries)
	}
}

func TestRequestLog_theHopMACCoversTheID(t *testing.T) {
	g, _ := namespaceGatewayForHops(t, "runtime")
	for name, edit := range map[string]func(http.Header){
		"changed": func(h http.Header) { h.Set(HeaderInternalAuthKeyID, "1") },
		"removed": func(h http.Header) { h.Del(HeaderInternalAuthKeyID) },
	} {
		r := keyIDHop(t, g, requestLogTestKeyID)
		edit(r.Header)
		if verifyInternalAuthHeaders(g.internalAuthKey, r, time.Now()) {
			t.Errorf("a %s key id still verified", name)
		}
	}
}

// recordingDB records every statement it is sent.
type recordingDB struct {
	client.DatabaseClient
	statements []string
	args       [][]interface{}
}

func (d *recordingDB) Query(_ context.Context, q string, args ...interface{}) (*client.QueryResult, error) {
	d.statements = append(d.statements, q)
	d.args = append(d.args, args)
	return &client.QueryResult{}, nil
}

// The rows go to the gateway's own database with the id as given;
// last_used_at goes to the registry, the only store that holds the key.
func TestRequestLogFlush_rowsHereLastUsedInTheRegistry(t *testing.T) {
	tenant, registry := &recordingDB{}, &recordingDB{}
	g := &Gateway{client: &fakeNetworkClient{db: tenant}, authClient: &fakeNetworkClient{db: registry}}
	b := &requestLogBatcher{gw: g, maxBatch: 1000}
	b.Add(requestLogEntry{method: "GET", path: "/a", apiKeyID: requestLogTestKeyID})
	b.Add(requestLogEntry{method: "GET", path: "/b"})
	b.flush()

	if len(tenant.statements) == 0 || !strings.HasPrefix(tenant.statements[0], "INSERT INTO request_logs") {
		t.Fatalf("tenant statements = %v, want the request_logs insert first", tenant.statements)
	}
	if got := tenant.args[0][6]; got != requestLogTestKeyID {
		t.Errorf("first row api_key_id = %v, want %d", got, requestLogTestKeyID)
	}
	if got := tenant.args[0][13]; got != nil {
		t.Errorf("keyless row api_key_id = %v, want NULL", got)
	}
	for _, q := range tenant.statements {
		if strings.Contains(q, "api_keys") {
			t.Errorf("the tenant's database was asked about api_keys: %s", q)
		}
	}
	if len(registry.statements) != 1 || !strings.HasPrefix(registry.statements[0], "UPDATE api_keys SET last_used_at") {
		t.Fatalf("registry statements = %v, want one last_used_at update", registry.statements)
	}
	if len(registry.args[0]) != 1 || registry.args[0][0] != requestLogTestKeyID {
		t.Errorf("last_used_at ids = %v, want [%d]", registry.args[0], requestLogTestKeyID)
	}
}

// A busy key costs the registry one last_used_at write per resolution, not
// one per flush.
func TestRequestLogFlush_lastUsedIsWrittenOncePerResolution(t *testing.T) {
	b := &requestLogBatcher{}
	batch := []requestLogEntry{{apiKeyID: 1}, {apiKeyID: 1}, {apiKeyID: 2}, {}}
	now := time.Unix(1_800_000_000, 0)

	if got := b.keysToTouch(batch, now); len(got) != 2 {
		t.Errorf("first flush touched %v, want keys 1 and 2 once each", got)
	}
	if got := b.keysToTouch(batch, now.Add(apiKeyLastUsedResolution/2)); len(got) != 0 {
		t.Errorf("a flush within the resolution touched %v, want none", got)
	}
	if got := b.keysToTouch(batch, now.Add(apiKeyLastUsedResolution)); len(got) != 2 {
		t.Errorf("a flush a resolution later touched %v, want keys 1 and 2", got)
	}
	if len(b.lastUsedWritten) != 2 {
		t.Errorf("the batcher remembers %d keys, want only the 2 in use", len(b.lastUsedWritten))
	}
}
