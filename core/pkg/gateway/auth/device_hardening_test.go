package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

// A small-order Ed25519 key "verifies" signatures nobody made: the identity
// point accepts R = identity, S = 0 for every message.
func TestParseDeviceKey_refusesSmallOrderEd25519(t *testing.T) {
	identity := make([]byte, 32)
	identity[0] = 1
	signed := append([]byte(nil), identity...)
	signed[31] |= 0x80 // the same point with the sign bit set
	for name, x := range map[string][]byte{"identity": identity, "identity, sign bit set": signed} {
		jwk := `{"kty":"OKP","crv":"Ed25519","x":"` + base64.RawURLEncoding.EncodeToString(x) + `"}`
		if _, err := ParseDeviceKey([]byte(jwk)); !errors.Is(err, ErrDeviceKeyInvalid) {
			t.Errorf("%s: accepted: %v", name, err)
		}
	}
}

// A gateway that predates devices hashes a refresh token bare. It must never
// find a device-bound session's row, or during a rolling upgrade it would
// rotate the session without the device and strip its binding.
func TestIssueDeviceTokens_aDeviceBoundRefreshTokenIsInvisibleToTheBareHash(t *testing.T) {
	s, db, nsID := realRegistry(t)
	grantEndUser(t, s)
	ctx := context.Background()
	d := p256Device(t)
	mustEnrol(t, s, deviceOwner, d, DeviceStateActive)

	_, refresh, _, err := s.IssueDeviceTokens(ctx, deviceOwner, "anchat", d.id)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !strings.HasPrefix(refresh, deviceBoundRefreshPrefix) {
		t.Errorf("a device-bound refresh token is not marked: %q", refresh)
	}
	res, err := db.Query(ctx, `SELECT COUNT(*) FROM refresh_tokens WHERE namespace_id = ? AND token = ?`, nsID, sha256Hex(refresh))
	if err != nil || cellInt64(res.Rows[0][0]) != 0 {
		t.Errorf("an older gateway's lookup found the device-bound row: %v %v", res, err)
	}
	_, plain, _, err := s.IssueTokens(ctx, deviceOwner, "anchat")
	if err != nil || strings.HasPrefix(plain, deviceBoundRefreshPrefix) {
		t.Errorf("an account session's refresh token is marked as device-bound: %q %v", plain, err)
	}
}

// Someone holding a just-rotated device-bound token but not the device must not
// spend the lost-response grace of the client that has it.
func TestRefreshToken_withoutTheDeviceDoesNotSpendTheGrace(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	grantEndUser(t, s)
	d := ed25519Device(t)
	mustEnrol(t, s, deviceOwner, d, DeviceStateActive)
	_, first, _, err := s.IssueDeviceTokens(ctx, deviceOwner, "anchat", d.id)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, _, _, _, err := s.RefreshToken(ctx, first, "anchat", proofFor(d, DeviceProofRefresh, "anchat", first)); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	// The rotated token, again, without the device: refused.
	if _, _, _, _, err := s.RefreshToken(ctx, first, "anchat", nil); !errors.Is(err, ErrDeviceProofRequired) {
		t.Fatalf("a rotated device-bound token was recovered without the device: %v", err)
	}
	// The device can still use its grace.
	if _, _, _, _, err := s.RefreshToken(ctx, first, "anchat", proofFor(d, DeviceProofRefresh, "anchat", first)); err != nil {
		t.Errorf("the device lost its lost-response recovery to a caller without it: %v", err)
	}
}

// Ending a session ends all of it: a token rotated a moment ago cannot use its
// reuse grace to bring the session back.
func TestEndSession_endsTheRowsStillInTheirGrace(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	grantEndUser(t, s)
	_, first, _, err := s.IssueTokens(ctx, deviceOwner, "anchat")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, _, _, _, err := s.RefreshToken(ctx, first, "anchat", nil); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	live, err := s.ListSessions(ctx, "anchat", deviceOwner)
	if err != nil || len(live) != 1 {
		t.Fatalf("list: %v %+v", err, live)
	}
	if err := s.EndSession(ctx, "anchat", deviceOwner, live[0].ID); err != nil {
		t.Fatalf("end: %v", err)
	}
	if _, _, _, _, err := s.RefreshToken(ctx, first, "anchat", nil); err == nil {
		t.Error("an ended session came back through its previous token's reuse grace")
	}
}

// Requiring devices revokes the keys end users' sign-ins minted — the whole
// chain of them — and leaves the operators' alone.
func TestSetDevicePolicy_revokesTheEndUsersSignInKeys(t *testing.T) {
	s, db, nsID := realRegistry(t)
	ctx := context.Background()
	for wallet, role := range map[string]Role{"0xowner": RoleAdmin, deviceOwner: RoleRuntime} {
		if err := s.Grant(ctx, GrantRequest{Namespace: "anchat", PrincipalType: PrincipalWallet,
			Identifier: wallet, Role: role, CreatedBy: "0xowner"}); err != nil {
			t.Fatalf("grant %s: %v", wallet, err)
		}
	}
	for _, wallet := range []string{deviceOwner, deviceOwner, "0xowner"} {
		if _, err := s.GetOrCreateAPIKey(ctx, wallet, "anchat"); err != nil {
			t.Fatalf("key for %s: %v", wallet, err)
		}
	}

	revoked, err := s.SetDevicePolicy(ctx, "anchat", DevicePolicyRequired, "0xowner")
	if err != nil {
		t.Fatalf("set policy: %v", err)
	}
	if revoked != 2 {
		t.Errorf("revoked %d keys, want the end user's 2 (the current one and the one it rotated from)", revoked)
	}
	res, _ := db.Query(ctx, `SELECT COUNT(*) FROM api_keys WHERE namespace_id = ? AND revoked_at IS NULL`, nsID)
	if n := cellInt64(res.Rows[0][0]); n != 1 {
		t.Errorf("%d keys left live, want the operator's 1", n)
	}
}

// A wallet whose admin grant has expired signs in as an end user, so the
// sweep treats its keys as an end user's; another namespace's keys are not
// this policy's to touch.
func TestSetDevicePolicy_sweepsAnExpiredOperatorAndNoOtherNamespace(t *testing.T) {
	s, db, nsID := realRegistry(t)
	ctx := context.Background()
	if _, err := db.Query(ctx, `INSERT INTO namespaces(id, name) VALUES (11, 'other')`); err != nil {
		t.Fatalf("second namespace: %v", err)
	}
	for _, ns := range []string{"anchat", "other"} {
		if err := s.Grant(ctx, GrantRequest{Namespace: ns, PrincipalType: PrincipalWallet, Identifier: deviceOwner,
			Role: RoleAdmin, ExpiresAt: time.Now().Add(time.Hour), CreatedBy: "0xowner"}); err != nil {
			t.Fatalf("grant in %s: %v", ns, err)
		}
		if _, err := s.GetOrCreateAPIKey(ctx, deviceOwner, ns); err != nil {
			t.Fatalf("key in %s: %v", ns, err)
		}
	}
	if _, err := db.Query(ctx, `UPDATE grants SET expires_at = datetime('now', '-1 minute') WHERE namespace_id = ?`, nsID); err != nil {
		t.Fatalf("expire the grant: %v", err)
	}

	if revoked, err := s.SetDevicePolicy(ctx, "anchat", DevicePolicyRequired, "0xowner"); err != nil || revoked != 1 {
		t.Errorf("SetDevicePolicy = %d, %v; want the expired admin's 1 key", revoked, err)
	}
	res, _ := db.Query(ctx, `SELECT COUNT(*) FROM api_keys WHERE namespace_id = 11 AND revoked_at IS NULL`)
	if n := cellInt64(res.Rows[0][0]); n != 1 {
		t.Errorf("%d keys live in the other namespace, want its 1 untouched", n)
	}
}

// A sweep that stops partway leaves the policy set and says so distinctly;
// setting the policy again revokes the keys that are left.
func TestSetDevicePolicy_aSweepThatStopsIsReportedAndResumable(t *testing.T) {
	s, db, nsID := realRegistry(t)
	ctx := context.Background()
	if err := s.Grant(ctx, GrantRequest{Namespace: "anchat", PrincipalType: PrincipalWallet,
		Identifier: deviceOwner, Role: RoleRuntime, CreatedBy: "0xowner"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	for range 2 {
		if _, err := s.GetOrCreateAPIKey(ctx, deviceOwner, "anchat"); err != nil {
			t.Fatalf("key: %v", err)
		}
	}
	// The registry takes the first revocation and refuses the second.
	if _, err := db.db.Exec(`CREATE TRIGGER refuse_revocation BEFORE UPDATE OF revoked_at ON api_keys
		WHEN (SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NOT NULL) >= 1
		BEGIN SELECT RAISE(FAIL, 'injected: the registry refused the write'); END`); err != nil {
		t.Fatalf("install the failure: %v", err)
	}

	revoked, err := s.SetDevicePolicy(ctx, "anchat", DevicePolicyRequired, "0xowner")
	if !errors.Is(err, ErrSignInKeySweepIncomplete) || revoked != 1 {
		t.Fatalf("SetDevicePolicy = %d, %v; want 1 and ErrSignInKeySweepIncomplete", revoked, err)
	}
	if p, err := s.DevicePolicyOf(ctx, "anchat"); err != nil || p != DevicePolicyRequired {
		t.Errorf("after a stopped sweep the policy is %q, %v; it was recorded and should hold", p, err)
	}

	if _, err := db.db.Exec(`DROP TRIGGER refuse_revocation`); err != nil {
		t.Fatalf("remove the failure: %v", err)
	}
	if revoked, err = s.SetDevicePolicy(ctx, "anchat", DevicePolicyRequired, "0xowner"); err != nil || revoked != 1 {
		t.Errorf("setting it again = %d, %v; want the 1 key left", revoked, err)
	}
	res, _ := db.Query(ctx, `SELECT COUNT(*) FROM api_keys WHERE namespace_id = ? AND revoked_at IS NULL`, nsID)
	if n := cellInt64(res.Rows[0][0]); n != 0 {
		t.Errorf("%d end-user keys still live", n)
	}
}

// The chain back from a wallet's current key is followed through a key already
// revoked: the older key behind it is still live and still an end user's.
func TestSetDevicePolicy_followsTheChainThroughARevokedKey(t *testing.T) {
	s, db, nsID := realRegistry(t)
	ctx := context.Background()
	if err := s.Grant(ctx, GrantRequest{Namespace: "anchat", PrincipalType: PrincipalWallet,
		Identifier: deviceOwner, Role: RoleRuntime, CreatedBy: "0xowner"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	for range 2 {
		if _, err := s.GetOrCreateAPIKey(ctx, deviceOwner, "anchat"); err != nil {
			t.Fatalf("key: %v", err)
		}
	}
	if _, err := db.Query(ctx, `UPDATE api_keys SET revoked_at = CURRENT_TIMESTAMP
		WHERE id = (SELECT api_key_id FROM wallet_api_keys WHERE namespace_id = ?)`, nsID); err != nil {
		t.Fatalf("revoke the current key: %v", err)
	}
	if revoked, err := s.SetDevicePolicy(ctx, "anchat", DevicePolicyRequired, "0xowner"); err != nil || revoked != 1 {
		t.Errorf("SetDevicePolicy = %d, %v; want the older key behind the revoked one", revoked, err)
	}
}

// A key that expired before any token exchanged from it could still be alive
// is left alone: revoking it would be a write that protects nothing.
func TestSetDevicePolicy_leavesLongExpiredKeysAlone(t *testing.T) {
	s, db, nsID := realRegistry(t)
	ctx := context.Background()
	if err := s.Grant(ctx, GrantRequest{Namespace: "anchat", PrincipalType: PrincipalWallet,
		Identifier: deviceOwner, Role: RoleRuntime, CreatedBy: "0xowner"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	for range 2 {
		if _, err := s.GetOrCreateAPIKey(ctx, deviceOwner, "anchat"); err != nil {
			t.Fatalf("key: %v", err)
		}
	}
	// The current key expired half an hour ago: tokens exchanged from it may
	// still be alive, so it is revoked. The older one expired two hours ago.
	if _, err := db.Query(ctx, `UPDATE api_keys SET expires_at = CASE
		WHEN id = (SELECT api_key_id FROM wallet_api_keys WHERE namespace_id = ?) THEN datetime('now', '-30 minutes')
		ELSE datetime('now', '-2 hours') END`, nsID); err != nil {
		t.Fatalf("age the keys: %v", err)
	}
	if revoked, err := s.SetDevicePolicy(ctx, "anchat", DevicePolicyRequired, "0xowner"); err != nil || revoked != 1 {
		t.Errorf("SetDevicePolicy = %d, %v; want only the recently expired key", revoked, err)
	}
}

// A key already revoked is ErrNoActiveKey, which the sweep counts as done.
func TestRevokeKey_anAlreadyRevokedKeyIsErrNoActiveKey(t *testing.T) {
	s, db, nsID := realRegistry(t)
	ctx := context.Background()
	if err := s.Grant(ctx, GrantRequest{Namespace: "anchat", PrincipalType: PrincipalWallet,
		Identifier: deviceOwner, Role: RoleRuntime, CreatedBy: "0xowner"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := s.GetOrCreateAPIKey(ctx, deviceOwner, "anchat"); err != nil {
		t.Fatalf("key: %v", err)
	}
	res, _ := db.Query(ctx, `SELECT api_key_id FROM wallet_api_keys WHERE namespace_id = ?`, nsID)
	id := cellInt64(res.Rows[0][0])
	if err := s.RevokeKey(ctx, "anchat", id); err != nil {
		t.Fatalf("first revocation: %v", err)
	}
	err := s.RevokeKey(ctx, "anchat", id)
	if !errors.Is(err, ErrNoActiveKey) || !strings.Contains(err.Error(), "no active key with id") {
		t.Errorf("revoking it again = %v; want ErrNoActiveKey with the same message", err)
	}
}

// An id the namespace never issued a session to is not a device to wake.
func TestRevokedDevices_anUnknownDeviceCountsAsRevoked(t *testing.T) {
	s, _, _ := realRegistry(t)
	live := mustEnrol(t, s, deviceOwner, p256Device(t), DeviceStateActive)
	revoked, err := s.RevokedDevices(context.Background(), "anchat", []string{live.ID, "never-issued"})
	if err != nil || revoked[live.ID] || !revoked["never-issued"] {
		t.Errorf("RevokedDevices = %v, %v", revoked, err)
	}
}

// Custom claims stored before the provider refused "did" are replayed on every
// rotation; minting drops them, so get_caller_claim("did") names nothing.
func TestGenerateBoundJWT_dropsTheBindingsNamesFromCustomClaims(t *testing.T) {
	s, _ := serviceWithRevocations(t)
	token, _, err := s.GenerateBoundJWT("anchat", "0xw", AccessTokenLifetime,
		map[string]string{"did": "forged", "sid": "forged", "tier": "pro"}, SessionBinding{DeviceID: "real"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	claims, err := s.ParseAndVerifyJWT(token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, present := claims.Custom["did"]; present || claims.Custom["tier"] != "pro" || claims.Did != "real" {
		t.Errorf("claims = %+v", claims)
	}
}

func TestCleanDeviceLabel_stripsWhatMakesOneLabelReadAsAnother(t *testing.T) {
	if got := cleanDeviceLabel("Alice\u202e\u200bphone\n"); got != "Alicephone" {
		t.Errorf("cleanDeviceLabel = %q", got)
	}
	if got := cleanDeviceLabel(strings.Repeat("x", maxDeviceLabelLength+10)); len([]rune(got)) != maxDeviceLabelLength {
		t.Errorf("a long label kept %d runes", len([]rune(got)))
	}
}

// A device link a wallet signature already tied to an account is that
// account's to refuse, not any member's.
func TestDenyDeviceAuthorization_aTiedLinkIsItsAccountsToRefuse(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	key, _ := ParseDeviceKey([]byte(p256Device(t).jwk))
	pending, err := s.StartDeviceLink(ctx, "anchat", deviceOwner, key, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := s.DenyDeviceAuthorization(ctx, pending.UserCode, "0xsomebodyelse"); !errors.Is(err, ErrDeviceCodeUnknown) {
		t.Errorf("another wallet refused this account's device link: %v", err)
	}
	if err := s.DenyDeviceAuthorization(ctx, pending.UserCode, deviceOwner); err != nil {
		t.Errorf("the account could not refuse its own device link: %v", err)
	}
}

// The CLI refreshes with whatever namespace it stored, which may be none.
func TestDevicePolicyFor_anEmptyNamespaceIsTheLobby(t *testing.T) {
	s, _, _ := realRegistry(t)
	if p, err := s.DevicePolicyFor(context.Background(), "", deviceOwner); err != nil || p != DevicePolicyOptional {
		t.Errorf("DevicePolicyFor(\"\") = %q, %v", p, err)
	}
}

// A revoked key cannot start a link either.
func TestStartDeviceLink_refusesARevokedKey(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	d := p256Device(t)
	mustEnrol(t, s, deviceOwner, d, DeviceStateActive)
	if err := s.RevokeDevice(ctx, "anchat", deviceOwner, d.id, byAccount); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	key, _ := ParseDeviceKey([]byte(d.jwk))
	if _, err := s.StartDeviceLink(ctx, "anchat", "", key, ""); !errors.Is(err, ErrDeviceRevoked) {
		t.Errorf("a revoked key started a device link: %v", err)
	}
}
