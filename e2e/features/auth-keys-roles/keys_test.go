//go:build e2e_fleet

package authkeysroles

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/e2e/harness"
	"github.com/Orama-Network/orama/e2e/harness/eventually"
	"github.com/Orama-Network/orama/e2e/harness/gw"
	"github.com/Orama-Network/orama/e2e/harness/ns"
)

const (
	// keyLifetime and maxKeyDays: "90 days by default, a year at most" (docs/whitepaper/technical-reference/vol1/14-authorization.md#api-keys).
	keyLifetime = 90 * 24 * time.Hour
	maxKeyDays  = 365
	// rotationOverlap and maxOverlapDays (docs/whitepaper/technical-reference/appendices/d-cli-reference.md "orama namespace keys rotate").
	rotationOverlap = 7 * 24 * time.Hour
	maxOverlapDays  = 30
	dayTolerance    = 10 * time.Minute
	// appRuntimeScopes is the app-runtime profile, canonical and sorted.
	appRuntimeScopes = "invoke,proxy,push,webrtc"
)

// keyShape is orama_<sk|rk>_<base62>_<base62 checksum> (docs/whitepaper/technical-reference/vol1/14-authorization.md#api-keys).
var keyShape = regexp.MustCompile(`^orama_(sk|rk)_[0-9A-Za-z]+_[0-9A-Za-z]+$`)

func untilTime(t testing.TB, s string) time.Duration {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("time %q: %v", s, err)
	}
	return time.Until(at)
}

func near(got, want time.Duration) bool {
	d := got - want
	return d < dayTolerance && d > -dayTolerance
}

// TestKeys_formatAndShownOnce: rk for the data plane, sk for the control
// plane, a checksum, no namespace in the string, 90 days by default, and the
// list never shows it again (docs/whitepaper/technical-reference/vol1/14-authorization.md#refusals-and-the-error-code-table).
func TestKeys_formatAndShownOnce(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	rk := mintKey(t, n, map[string]any{"scope": "app-runtime", "label": "web"})
	sk := mintKey(t, n, map[string]any{"scope": "admin", "label": "ci"})
	for want, k := range map[string]mintedKey{"rk": rk, "sk": sk} {
		m := keyShape.FindStringSubmatch(k.APIKey)
		if m == nil || m[1] != want || strings.Contains(k.APIKey, n.Name) {
			t.Errorf("key for %s has the wrong shape or names its namespace", want)
		}
		if !near(untilTime(t, k.ExpiresAt), keyLifetime) {
			t.Errorf("%s key expires at %s, want 90 days out", want, k.ExpiresAt)
		}
	}
	if rk.Scopes != appRuntimeScopes || rk.Label != "web" || rk.Namespace != n.Name {
		t.Errorf("app-runtime key: scopes %q label %q namespace %q", rk.Scopes, rk.Label, rk.Namespace)
	}
	list := send(t, n.Owner.Client, http.MethodGet, pathKeys, n.Owner.Token(), nil).Expect(t, http.StatusOK)
	for _, k := range []mintedKey{rk, sk} {
		if strings.Contains(string(list.Body), k.APIKey) || !strings.Contains(string(list.Body), fmt.Sprintf(`"id":%d`, k.ID)) {
			t.Errorf("key %d: the list shows the key material, or omits the key", k.ID)
		}
	}
}

// TestKeys_mintRefusals: every key expires within a year; an empty or
// unknown scope is refused (docs/whitepaper/technical-reference/vol1/14-authorization.md#refusals-and-the-error-code-table).
func TestKeys_mintRefusals(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	for name, body := range map[string]map[string]any{
		"no scope":         {"label": "x"},
		"empty scope":      {"scope": ""},
		"unknown grant":    {"scope": "root"},
		"a year and a day": {"scope": "cache", "expires_in_days": maxKeyDays + 1},
		"negative life":    {"scope": "cache", "expires_in_days": -1},
		"wrong type":       {"scope": 7},
	} {
		resp := send(t, n.Owner.Client, http.MethodPost, pathKeys, n.Owner.Token(), body)
		if resp.Status != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d %s", name, resp.Status, resp.Body)
		}
	}
	year := mintKey(t, n, map[string]any{"scope": "cache,pubsub", "expires_in_days": maxKeyDays})
	if !near(untilTime(t, year.ExpiresAt), maxKeyDays*24*time.Hour) {
		t.Errorf("a %d-day key expires at %s", maxKeyDays, year.ExpiresAt)
	}
}

// TestKeys_checksumAndExchange: the key exchanges for a 15-minute token whose
// subject is not the key; a key one character off is refused before it can
// match anything (AUTH_INVALID_KEY).
func TestKeys_checksumAndExchange(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	k := mintKey(t, n, map[string]any{"scope": "cache"})
	s, _, err := c.For(t).Token(t.Context(), k.APIKey)
	if err != nil {
		t.Fatalf("exchanging a fresh key: %v", err)
	}
	if strings.Contains(s.AccessToken, k.APIKey) || strings.Contains(string(mustWhoami(t, c, s.AccessToken)), k.APIKey) {
		t.Fatal("the exchanged token or whoami carries the raw key")
	}
	last := k.APIKey[len(k.APIKey)-1]
	flip := byte('A')
	if last == 'A' {
		flip = 'B'
	}
	for _, bad := range []string{k.APIKey[:len(k.APIKey)-1] + string(flip), k.APIKey + "0", "orama_rk_0_0", "ak_legacy:" + n.Name} {
		resp := c.MustSend(t, gw.Req{Method: http.MethodPost, Path: gw.PathToken, Bearer: bad})
		refusal(t, resp, http.StatusUnauthorized, "AUTH_INVALID_KEY")
	}
}

// TestKeys_anAdminKeyTokenManagesKeysAndMembers: a token exchanged for an
// admin key, which is how the CLI sends ORAMA_TOKEN, lists, mints and revokes
// keys and lists the members; a storage key's token belongs to the namespace
// and is refused key management with INSUFFICIENT_SCOPE, never
// OWNERSHIP_REQUIRED (change-975: the grant lookup hashed the token's subject
// a second time and found no grant for any key token).
func TestKeys_anAdminKeyTokenManagesKeysAndMembers(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	admin := mintKey(t, n, map[string]any{"scope": "admin", "label": "change-975"})
	s, _, err := c.For(t).Token(t.Context(), admin.APIKey)
	if err != nil {
		t.Fatalf("exchanging the admin key: %v", err)
	}
	send(t, c, http.MethodGet, pathKeys, s.AccessToken, nil).Expect(t, http.StatusOK)
	send(t, c, http.MethodGet, pathMembers, s.AccessToken, nil).Expect(t, http.StatusOK)
	var k mintedKey
	if err := send(t, c, http.MethodPost, pathKeys, s.AccessToken, map[string]any{"scope": "storage", "label": "change-975-storage"}).
		Expect(t, http.StatusCreated).Decode(&k); err != nil {
		t.Fatal(err)
	}
	protect(t, c, k.APIKey)
	t.Cleanup(func() { revokeAtCleanup(t, n, k.ID) })

	storage, _, err := c.For(t).Token(t.Context(), k.APIKey)
	if err != nil {
		t.Fatalf("exchanging the storage key: %v", err)
	}
	refusal(t, send(t, c, http.MethodGet, pathKeys, storage.AccessToken, nil), http.StatusForbidden, "INSUFFICIENT_SCOPE")

	send(t, c, http.MethodDelete, keyPath(k.ID), s.AccessToken, nil).Expect(t, http.StatusOK)
}

func mustWhoami(t testing.TB, c *gw.Client, bearer string) []byte {
	t.Helper()
	return c.MustSend(t, gw.Req{Path: gw.PathWhoami, Bearer: bearer}).Expect(t, http.StatusOK).Body
}

// TestKeys_revokeStopsKeyAndItsTokens: revoking a key refuses the key and the
// tokens exchanged from it on every gateway within the revocation staleness (docs/whitepaper/technical-reference/vol1/13-identity.md#revocation); revoking twice is
// 404, a malformed id 400.
func TestKeys_revokeStopsKeyAndItsTokens(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	k := mintKey(t, n, map[string]any{"scope": "cache"})
	s, _, err := c.For(t).Token(t.Context(), k.APIKey)
	if err != nil {
		t.Fatal(err)
	}
	send(t, c, http.MethodDelete, keyPath(k.ID), n.Owner.Token(), nil).Expect(t, http.StatusOK)
	// The DELETE and the exchange may reach different gateways; each drops
	// the key from its cache within the revocation list's staleness.
	var resp *gw.Response
	eventually.Require(t, pollEvery, revocationStaleness+stalenessSlack, "the revoked key to be refused", func() (bool, error) {
		resp = c.MustSend(t, gw.Req{Method: http.MethodPost, Path: gw.PathToken, Bearer: k.APIKey})
		if resp.Status == http.StatusUnauthorized {
			return true, nil
		}
		return false, fmt.Errorf("HTTP %d %s", resp.Status, resp.ErrorCode())
	})
	refusal(t, resp, http.StatusUnauthorized, "AUTH_INVALID_KEY")
	eventually.Require(t, pollEvery, revocationStaleness+stalenessSlack, "the exchanged token to be refused", func() (bool, error) {
		r := c.MustSend(t, gw.Req{Path: gw.PathWhoami, Bearer: s.AccessToken})
		if r.Status == http.StatusUnauthorized {
			return true, nil
		}
		return false, fmt.Errorf("HTTP %d %s", r.Status, r.ErrorCode())
	})
	if r := send(t, c, http.MethodDelete, keyPath(k.ID), n.Owner.Token(), nil); r.Status != http.StatusNotFound {
		t.Errorf("revoking twice: want 404, got %d", r.Status)
	}
	for _, id := range []string{"abc", "0", "-3"} {
		if r := send(t, c, http.MethodDelete, pathKeys+"/"+id, n.Owner.Token(), nil); r.Status != http.StatusBadRequest {
			t.Errorf("revoking key %q: want 400, got %d", id, r.Status)
		}
	}
	list := send(t, c, http.MethodGet, pathKeys, n.Owner.Token(), nil).Expect(t, http.StatusOK)
	if !strings.Contains(string(list.Body), `"revoked_at"`) {
		t.Errorf("the list does not show the revoked key as revoked: %s", list.Body)
	}
}

// TestKeys_rotateKeepsBothForTheOverlap: rotation mints a successor with the
// same grants and shortens the original to the overlap (7 days by default,
// 30 at most); both keys work meanwhile.
func TestKeys_rotateKeepsBothForTheOverlap(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	k := mintKey(t, n, map[string]any{"scope": "cache", "label": "rotating"})
	for _, days := range []int{-1, maxOverlapDays + 1} {
		if r := send(t, c, http.MethodPost, keyPath(k.ID)+"/rotate", n.Owner.Token(), map[string]int{"overlap_days": days}); r.Status != http.StatusBadRequest {
			t.Errorf("overlap %d days: want 400, got %d", days, r.Status)
		}
	}
	var rot struct {
		ID              int64  `json:"id"`
		APIKey          string `json:"api_key"`
		RotatedFrom     int64  `json:"rotated_from"`
		PreviousExpires string `json:"previous_expires"`
	}
	if err := send(t, c, http.MethodPost, keyPath(k.ID)+"/rotate", n.Owner.Token(), nil).Expect(t, http.StatusCreated).Decode(&rot); err != nil {
		t.Fatal(err)
	}
	protect(t, c, rot.APIKey)
	t.Cleanup(func() { revokeAtCleanup(t, n, rot.ID) })
	if rot.RotatedFrom != k.ID || rot.APIKey == k.APIKey || !near(untilTime(t, rot.PreviousExpires), rotationOverlap) {
		t.Errorf("rotation: from %d (want %d), previous expires %s (want 7 days)", rot.RotatedFrom, k.ID, rot.PreviousExpires)
	}
	for _, key := range []string{k.APIKey, rot.APIKey} {
		if _, _, err := c.For(t).Token(t.Context(), key); err != nil {
			t.Errorf("during the overlap a key does not exchange: %v", err)
		}
	}
	if r := send(t, c, http.MethodPost, keyPath(999999999)+"/rotate", n.Owner.Token(), nil); r.Status != http.StatusNotFound {
		t.Errorf("rotating an unknown key: want 404, got %d", r.Status)
	}
}

// TestKeys_legacySpellingsDeprecated: X-API-Key, "ApiKey <k>" and a bare
// Authorization still work but come back with Deprecation: true and what to
// send instead; Bearer does not (docs/whitepaper/technical-reference/vol1/12-gateway-architecture.md#credential-resolution).
func TestKeys_legacySpellingsDeprecated(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)
	k := mintKey(t, n, map[string]any{"scope": "cache"})
	spellings := map[string]http.Header{
		"X-API-Key":            {"X-Api-Key": {k.APIKey}},
		"Authorization ApiKey": {"Authorization": {"ApiKey " + k.APIKey}},
		"bare Authorization":   {"Authorization": {k.APIKey}},
	}
	for name, h := range spellings {
		resp := c.MustSend(t, gw.Req{Path: gw.PathWhoami, Header: h}).Expect(t, http.StatusOK)
		if resp.Header.Get("Deprecation") != "true" || !strings.Contains(resp.Header.Get("X-Orama-Deprecation"), "Authorization: Bearer") {
			t.Errorf("%s: Deprecation %q X-Orama-Deprecation %q", name, resp.Header.Get("Deprecation"), resp.Header.Get("X-Orama-Deprecation"))
		}
	}
	bearer := c.MustSend(t, gw.Req{Path: gw.PathWhoami, Bearer: k.APIKey}).Expect(t, http.StatusOK)
	if bearer.Header.Get("Deprecation") != "" {
		t.Error("Authorization: Bearer <key> was marked deprecated")
	}
	bogus := c.MustSend(t, gw.Req{Path: gw.PathWhoami, Header: http.Header{"X-Api-Key": {"orama_rk_1_1"}}})
	if bogus.Status != http.StatusUnauthorized || bogus.Header.Get("Deprecation") != "true" {
		t.Errorf("a refused legacy spelling: %d Deprecation %q", bogus.Status, bogus.Header.Get("Deprecation"))
	}
	twice := c.MustSend(t, gw.Req{Path: gw.PathWhoami, Header: http.Header{"X-Api-Key": {k.APIKey, "orama_rk_1_1"}}})
	if twice.Status >= http.StatusInternalServerError {
		t.Errorf("a duplicated X-API-Key header answered %d", twice.Status)
	}
}

// TestKeys_revokeLegacyCutover: a namespace with no legacy key revokes none,
// and the step is safe to run again.
func TestKeys_revokeLegacyCutover(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	for i := 0; i < 2; i++ {
		var out struct {
			Status  string `json:"status"`
			Revoked int    `json:"revoked"`
		}
		resp := send(t, n.Owner.Client, http.MethodPost, pathKeys+"/revoke-legacy", n.Owner.Token(), nil)
		if err := resp.Expect(t, http.StatusOK).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if out.Status != "revoked-legacy" || out.Revoked != 0 {
			t.Errorf("run %d: %+v", i+1, out)
		}
	}
}

// fiveFunctions is one key's whole reach: five functions sharing no single
// pattern (docs/whitepaper/technical-reference/vol1/14-authorization.md#api-keys).
var fiveFunctions = []string{
	"fn:subscription-intent-create",
	"fn:subscription-read",
	"fn:subscription-record-payment",
	"fn:user-find-by-username",
	"fn:user-find-by-wallet",
}

// TestKeys_narrowedToResources: a key minted with resources reports them,
// the list shows them, a rotation carries them to the successor, and a
// resource the key could not be narrowed to is refused at mint
// (docs/whitepaper/technical-reference/vol1/14-authorization.md#api-keys). What
// a narrowed key may invoke is asserted where functions are deployed
// (auth-capability-ws).
func TestKeys_narrowedToResources(t *testing.T) {
	t.Parallel()
	f := harness.Fleet(t)
	n := ns.New(t, f, ns.Options{})
	c := harness.GW(t)

	k := mintKey(t, n, map[string]any{"scope": "invoke-only", "label": "five", "resources": fiveFunctions})
	if strings.Join(k.Resources, ",") != strings.Join(fiveFunctions, ",") {
		t.Errorf("minted resources %v, want %v", k.Resources, fiveFunctions)
	}
	pattern := mintKey(t, n, map[string]any{"scope": "invoke-only", "resource": "fn:premium-*"})
	if strings.Join(pattern.Resources, ",") != "fn:premium-*" {
		t.Errorf("minted resources %v, want [fn:premium-*]", pattern.Resources)
	}

	for name, body := range map[string]map[string]any{
		"a domain the key does not hold": {"scope": "invoke-only", "resource": "storage:avatars/*"},
		"a domain nothing applies":       {"scope": "push", "resource": "push:topic=x"},
		"an admin key":                   {"scope": "admin", "resource": "fn:a"},
		"a scope no resource narrows":    {"scope": "app-runtime", "resource": "fn:a"},
		"not a selector":                 {"scope": "invoke-only", "resource": "premium-status"},
	} {
		if r := send(t, c, http.MethodPost, pathKeys, n.Owner.Token(), body); r.Status != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d %s", name, r.Status, r.Body)
		}
	}

	var rot struct {
		ID     int64  `json:"id"`
		APIKey string `json:"api_key"`
	}
	if err := send(t, c, http.MethodPost, keyPath(k.ID)+"/rotate", n.Owner.Token(), nil).Expect(t, http.StatusCreated).Decode(&rot); err != nil {
		t.Fatal(err)
	}
	protect(t, c, rot.APIKey)
	t.Cleanup(func() { revokeAtCleanup(t, n, rot.ID) })

	var list struct {
		Keys []struct {
			ID        int64    `json:"id"`
			Resources []string `json:"resources"`
		} `json:"keys"`
	}
	if err := send(t, c, http.MethodGet, pathKeys, n.Owner.Token(), nil).Expect(t, http.StatusOK).Decode(&list); err != nil {
		t.Fatal(err)
	}
	listed := map[int64]string{}
	for _, row := range list.Keys {
		listed[row.ID] = strings.Join(row.Resources, ",")
	}
	want := strings.Join(fiveFunctions, ",")
	if listed[k.ID] != want || listed[rot.ID] != want {
		t.Errorf("listed resources: key %d %q, successor %d %q, want %q", k.ID, listed[k.ID], rot.ID, listed[rot.ID], want)
	}
}
