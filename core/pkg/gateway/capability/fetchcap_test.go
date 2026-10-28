package capability

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/client"
	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
)

const fetchCID = "bafkreigh2akiscaildcqabsyg3dfr6chu3fgpregiymsck7e7aqa4s52zy"

func mintFetch(t *testing.T, a *Authority) (string, *FetchClaims) {
	t.Helper()
	token, claims, err := a.MintFetch("anchat", fetchCID, "device-1", 2*time.Hour, now)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return token, claims
}

func TestVerifyFetch_acceptsWhatItMinted(t *testing.T) {
	a := authority(t)
	token, minted := mintFetch(t, a)
	got, err := a.VerifyFetch(token, "anchat", fetchCID, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("a genuine fetch capability was refused: %v", err)
	}
	if *got != *minted || got.CID != fetchCID || got.Namespace != "anchat" {
		t.Errorf("verified %+v, minted %+v", got, minted)
	}
}

func TestVerifyFetch_refusalsAreOneError(t *testing.T) {
	a := authority(t)
	token, _ := mintFetch(t, a)
	other, _ := NewAuthority("another-cluster")
	foreign, _, _ := other.MintFetch("anchat", fetchCID, "device-1", 2*time.Hour, now)
	wsToken, _, err := a.Mint("anchat", "rpc-router", fetchCID, "device-1", 2*time.Hour, now)
	if err != nil {
		t.Fatalf("mint a WebSocket capability: %v", err)
	}
	payload, sig, _ := strings.Cut(token, ".")

	for name, tc := range map[string]struct {
		token, ns, cid string
		at             time.Time
	}{
		"expired":                      {token, "anchat", fetchCID, now.Add(2 * time.Hour)},
		"another CID":                  {token, "anchat", "bafkreiother", now},
		"another namespace":            {token, "elsewhere", fetchCID, now},
		"another cluster's":            {foreign, "anchat", fetchCID, now},
		"a WebSocket capability":       {wsToken, "anchat", fetchCID, now},
		"a tampered payload":           {"eyJ2IjoxfQ." + sig, "anchat", fetchCID, now},
		"a truncated signature":        {payload + "." + sig[:10], "anchat", fetchCID, now},
		"no signature":                 {payload, "anchat", fetchCID, now},
		"empty":                        {"", "anchat", fetchCID, now},
		"oversize":                     {strings.Repeat("a", maxTokenLength+1), "anchat", fetchCID, now},
		"a token with the empty CID":   {token, "anchat", "", now},
		"a token for the empty tenant": {token, "", fetchCID, now},
	} {
		if _, err := a.VerifyFetch(tc.token, tc.ns, tc.cid, tc.at); !errors.Is(err, ErrFetchInvalid) {
			t.Errorf("%s: err = %v, want ErrFetchInvalid", name, err)
		}
	}
}

func TestVerify_aFetchCapabilityIsNotAWebSocketCapability(t *testing.T) {
	a := authority(t)
	token, _ := mintFetch(t, a)
	if _, err := a.Parse(token, "anchat"); !errors.Is(err, ErrInvalid) {
		t.Errorf("a fetch capability parsed as a WebSocket capability: %v", err)
	}
}

func TestMintFetch_refusals(t *testing.T) {
	a := authority(t)
	for name, tc := range map[string]struct {
		ns, cid, device string
		ttl             time.Duration
		want            error
	}{
		"no device":      {"anchat", fetchCID, "", time.Hour, ErrNoIssuer},
		"no namespace":   {"", fetchCID, "d", time.Hour, nil},
		"no CID":         {"anchat", "", "d", time.Hour, nil},
		"too long a CID": {"anchat", strings.Repeat("b", maxFetchCIDLength+1), "d", time.Hour, nil},
		"too short":      {"anchat", fetchCID, "d", FetchMinTTL - time.Second, nil},
		"too long":       {"anchat", fetchCID, "d", MaxTTL + time.Second, nil},
	} {
		_, _, err := a.MintFetch(tc.ns, tc.cid, tc.device, tc.ttl, now)
		if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, _, err := a.MintFetch("anchat", fetchCID, "d", FetchMinTTL, now); err != nil {
		t.Errorf("the shortest ttl was refused: %v", err)
	}
	if _, _, err := a.MintFetch("anchat", fetchCID, "d", MaxTTL, now); err != nil {
		t.Errorf("the longest ttl was refused: %v", err)
	}
}

// The tag is the reason a fetch capability is not a WebSocket one: two tokens
// of one device must share nothing the serving node could compare.
func TestMintFetch_tokensOfOneDeviceShareNoTag(t *testing.T) {
	a := authority(t)
	seen := map[string]bool{}
	for range 20 {
		_, claims := mintFetch(t, a)
		if strings.Contains(claims.RevocationTag, "device-1") {
			t.Fatalf("the tag carries the device in the clear: %q", claims.RevocationTag)
		}
		if seen[claims.RevocationTag] || seen[claims.ID] {
			t.Fatalf("two tokens share a tag or an id: %+v", claims)
		}
		seen[claims.RevocationTag], seen[claims.ID] = true, true
	}
}

func TestFetchRevocationClaims_opensTheDeviceFromTheTag(t *testing.T) {
	a := authority(t)
	_, claims := mintFetch(t, a)
	got, err := a.FetchRevocationClaims(claims)
	if err != nil {
		t.Fatalf("FetchRevocationClaims: %v", err)
	}
	if got.Did != "device-1" || got.Jti != RevocationID("anchat", claims.ID) || got.Exp != claims.ExpiresAt {
		t.Errorf("revocation claims = %+v", got)
	}
	forged := *claims
	forged.RevocationTag = "enc:AAAA"
	if _, err := a.FetchRevocationClaims(&forged); err == nil {
		t.Error("a tag this cluster did not seal opened")
	}
}

func TestIssuer_MintFetchCapsIssuesDistinctTokens(t *testing.T) {
	i, _ := issuer(t)
	caps, err := i.MintFetchCaps(context.Background(), "anchat", fetchCID, "device-1", 3, 2*time.Hour)
	if err != nil || len(caps) != 3 {
		t.Fatalf("MintFetchCaps: %v (%d caps)", err, len(caps))
	}
	ids := map[string]bool{}
	for _, c := range caps {
		if _, err := i.CheckFetch(c.Token, "anchat", fetchCID); err != nil {
			t.Errorf("a minted capability does not check: %v", err)
		}
		ids[c.ID] = true
	}
	if len(ids) != 3 {
		t.Errorf("ids = %v, want 3 distinct", ids)
	}
	for _, count := range []int{0, -1, MaxFetchCapsPerMint + 1} {
		if _, err := i.MintFetchCaps(context.Background(), "anchat", fetchCID, "device-1", count, time.Hour); err == nil {
			t.Errorf("count %d was accepted", count)
		}
	}
	if _, err := i.MintFetchCaps(context.Background(), "anchat", fetchCID, "", 1, time.Hour); !errors.Is(err, ErrNoIssuer) {
		t.Errorf("a mint with no device: %v", err)
	}
}

func TestIssuer_CheckFetchHonoursRevocationByIdAndByDevice(t *testing.T) {
	i, list := issuer(t)
	ctx := context.Background()
	caps, err := i.MintFetchCaps(ctx, "anchat", fetchCID, "device-1", 3, 2*time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	other, err := i.MintFetchCaps(ctx, "anchat", fetchCID, "device-2", 1, 2*time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	if err := i.RevokeFetchCap(ctx, "anchat", caps[0].ID, caps[0].RevokeKey); err != nil {
		t.Fatalf("RevokeFetchCap: %v", err)
	}
	if _, err := i.CheckFetch(caps[0].Token, "anchat", fetchCID); !errors.Is(err, ErrFetchRevoked) {
		t.Errorf("a revoked capability: err = %v, want ErrFetchRevoked", err)
	}
	if _, err := i.CheckFetch(caps[1].Token, "anchat", fetchCID); err != nil {
		t.Errorf("revoking one capability reached another: %v", err)
	}

	if err := list.RevokeDevice(ctx, "anchat", "device-1"); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}
	if _, err := i.CheckFetch(caps[1].Token, "anchat", fetchCID); !errors.Is(err, ErrFetchRevoked) {
		t.Errorf("a capability of a revoked device: err = %v, want ErrFetchRevoked", err)
	}
	if _, err := i.CheckFetch(other[0].Token, "anchat", fetchCID); err != nil {
		t.Errorf("revoking a device reached another device's capability: %v", err)
	}
}

func TestIssuer_RevokeFetchCapRefusesWhatIsNotAnId(t *testing.T) {
	i, _ := issuer(t)
	for _, id := range []string{"", "short", strings.Repeat("z", 32), strings.Repeat("a", 33), "../" + strings.Repeat("a", 29)} {
		if err := i.RevokeFetchCap(context.Background(), "anchat", id, "key"); err == nil {
			t.Errorf("%q was accepted as an id", id)
		}
	}
}

func issuerWithTable(t *testing.T) (*Issuer, *revokedTokensTable) {
	t.Helper()
	table := &revokedTokensTable{}
	list := auth.NewRevocationList(func() client.DatabaseClient { return table }, nil)
	i, err := NewIssuer(authority(t), list)
	if err != nil {
		t.Fatal(err)
	}
	return i, table
}

// An id is not proof that it was issued. Without the revoke key the mint returned
// for it, nothing is written, however many ids are tried.
func TestIssuer_RevokeFetchCapWritesNothingWithoutTheRevokeKey(t *testing.T) {
	i, table := issuerWithTable(t)
	ctx := context.Background()
	caps, err := i.MintFetchCaps(ctx, "anchat", fetchCID, "device-1", 2, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := i.MintFetchCaps(ctx, "other", fetchCID, "device-1", 1, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ id, key string }{
		"an invented id":          {strings.Repeat("ab", 16), caps[0].RevokeKey},
		"no key":                  {caps[0].ID, ""},
		"another capability's":    {caps[0].ID, caps[1].RevokeKey},
		"another namespace's":     {caps[0].ID, foreign[0].RevokeKey},
		"the token as the key":    {caps[0].ID, caps[0].Token},
		"an upper-cased key":      {caps[0].ID, strings.ToUpper(caps[0].RevokeKey)},
		"a key and a trailing nl": {caps[0].ID, caps[0].RevokeKey + "\n"},
	} {
		if err := i.RevokeFetchCap(ctx, "anchat", tc.id, tc.key); !errors.Is(err, ErrFetchRevokeKeyInvalid) {
			t.Errorf("%s: err = %v, want ErrFetchRevokeKeyInvalid", name, err)
		}
	}
	if len(table.rows) != 0 {
		t.Errorf("refused revokes wrote %d rows", len(table.rows))
	}
	if _, err := i.CheckFetch(caps[0].Token, "anchat", fetchCID); err != nil {
		t.Errorf("a refused revoke revoked the capability: %v", err)
	}
}

func TestIssuer_RevokeFetchCapWritesOneRowHoweverOftenItIsCalled(t *testing.T) {
	i, table := issuerWithTable(t)
	ctx := context.Background()
	caps, err := i.MintFetchCaps(ctx, "anchat", fetchCID, "device-1", 1, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if err := i.RevokeFetchCap(ctx, "anchat", caps[0].ID, caps[0].RevokeKey); err != nil {
			t.Fatalf("RevokeFetchCap: %v", err)
		}
	}
	if len(table.rows) != 1 {
		t.Errorf("rows = %d, want 1", len(table.rows))
	}
}

func TestRevokeKey_isPerNamespaceAndPerID(t *testing.T) {
	a := authority(t)
	k1, _ := a.RevokeKey("anchat", strings.Repeat("a", 32))
	k2, _ := a.RevokeKey("anchat", strings.Repeat("b", 32))
	k3, _ := a.RevokeKey("other", strings.Repeat("a", 32))
	if k1 == k2 || k1 == k3 || len(k1) != 64 {
		t.Errorf("revoke keys: %q %q %q", k1, k2, k3)
	}
	if ok, _ := a.VerifyRevokeKey("anchat", strings.Repeat("a", 32), k1); !ok {
		t.Error("its own key was refused")
	}
	if ok, _ := a.VerifyRevokeKey("anchat", strings.Repeat("a", 32), ""); ok {
		t.Error("an empty key was accepted")
	}
}

func TestIssuer_CheckFetchInvalidIsNotRevoked(t *testing.T) {
	i, _ := issuer(t)
	if _, err := i.CheckFetch("garbage", "anchat", fetchCID); !errors.Is(err, ErrFetchInvalid) {
		t.Errorf("err = %v, want ErrFetchInvalid", err)
	}
}
