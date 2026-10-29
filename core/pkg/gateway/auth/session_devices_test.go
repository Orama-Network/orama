package auth

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/client"
)

// These run against the real schema, as the device-login tests do: what makes
// a device safe is the predicates in the SQL, and a fake would model them.

const deviceOwner = "0xa11ce"

func mustEnrol(t *testing.T, s *Service, subject string, d testDevice, state DeviceState) *SessionDevice {
	t.Helper()
	key, err := ParseDeviceKey([]byte(d.jwk))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	dev, err := s.EnrolDevice(context.Background(), "anchat", subject, key, "phone", state, "")
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	return dev
}

// proofFor signs a fresh proof the way a device does.
func proofFor(d testDevice, action, namespace, binding string) *DeviceProof {
	id := "proof-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	iat := time.Now().Unix()
	return &DeviceProof{IssuedAt: iat, ID: id, Signature: d.sign(DeviceProofMessage(action, namespace, binding, iat, id))}
}

// hidingNewDevices is the registry a follower is for a moment after the
// leader has stored a device: the insert is committed, and a local read of
// that id still returns no row.
type hidingNewDevices struct {
	*sqliteDatabase
}

func (h *hidingNewDevices) Query(ctx context.Context, query string, args ...interface{}) (*client.QueryResult, error) {
	if strings.Contains(query, "FROM session_devices") && strings.Contains(query, "WHERE id") {
		return &client.QueryResult{}, nil
	}
	return h.sqliteDatabase.Query(ctx, query, args...)
}

type hidingNet struct {
	client.NetworkClient
	db client.DatabaseClient
}

func (n hidingNet) Database() client.DatabaseClient { return n.db }

func TestEnrolDevice_aLocalReadThatMissesTheInsertStillEnrolls(t *testing.T) {
	s, db, _ := realRegistry(t)
	s.orm = hidingNet{db: &hidingNewDevices{sqliteDatabase: db}}
	d := ed25519Device(t)
	key, err := ParseDeviceKey([]byte(d.jwk))
	if err != nil {
		t.Fatal(err)
	}
	dev, err := s.EnrolDevice(context.Background(), "anchat", deviceOwner, key, "phone", DeviceStateActive, "")
	if err != nil {
		t.Fatalf("a committed insert was reported as someone else's key: %v", err)
	}
	if dev.ID != d.id || dev.State != DeviceStateActive || dev.Subject != deviceOwner {
		t.Fatalf("enrolled %+v", dev)
	}
	// The row is in the registry. A read that can see it finds this account's device.
	s.orm = &sqliteNet{db: db}
	got, err := s.Device(context.Background(), "anchat", d.id)
	if err != nil || got.Subject != deviceOwner || got.State != DeviceStateActive {
		t.Fatalf("stored device = %+v, err %v", got, err)
	}
}

func TestEnrolDevice_aKeyBelongsToOneAccount(t *testing.T) {
	s, _, _ := realRegistry(t)
	d := p256Device(t)
	first := mustEnrol(t, s, deviceOwner, d, DeviceStateActive)
	if first.ID != d.id || first.State != DeviceStateActive {
		t.Fatalf("enrolled %+v", first)
	}

	key, _ := ParseDeviceKey([]byte(d.jwk))
	if _, err := s.EnrolDevice(context.Background(), "anchat", "0xb0b", key, "", DeviceStateActive, ""); !errors.Is(err, ErrDeviceBelongsToAnother) {
		t.Errorf("another account enrolled a device key that is already somebody's: %v", err)
	}
	again, err := s.EnrolDevice(context.Background(), "anchat", deviceOwner, key, "", DeviceStateActive, "")
	if err != nil || again.ID != first.ID {
		t.Errorf("signing in again from the same device did not find it: %v", err)
	}
}

// Property 1 and 3: the device's refresh needs the device, and a revoked device
// can never come back.
func TestRefreshToken_needsTheDeviceAndRevocationEndsIt(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	grantEndUser(t, s)
	d := ed25519Device(t)
	mustEnrol(t, s, deviceOwner, d, DeviceStateActive)

	access, refresh, _, err := s.IssueDeviceTokens(ctx, deviceOwner, "anchat", d.id)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := s.ParseAndVerifyJWT(access)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Did != d.id || claims.Sid == "" {
		t.Fatalf("the access token is not bound: did=%q sid=%q", claims.Did, claims.Sid)
	}

	// The refresh token alone is not enough, and trying does not burn it.
	if _, _, _, _, err := s.RefreshToken(ctx, refresh, "anchat", nil); !errors.Is(err, ErrDeviceProofRequired) {
		t.Fatalf("a device-bound session refreshed without the device: %v", err)
	}
	other := ed25519Device(t)
	if _, _, _, _, err := s.RefreshToken(ctx, refresh, "anchat", proofFor(other, DeviceProofRefresh, "anchat", refresh)); !errors.Is(err, ErrDeviceProofInvalid) {
		t.Fatalf("another key's proof refreshed the session: %v", err)
	}
	proof := proofFor(d, DeviceProofRefresh, "anchat", refresh)
	next, rotated, _, _, err := s.RefreshToken(ctx, refresh, "anchat", proof)
	if err != nil {
		t.Fatalf("the device could not refresh its own session: %v", err)
	}
	if _, _, _, _, err := s.RefreshToken(ctx, rotated, "anchat", proof); !errors.Is(err, ErrDeviceProofInvalid) {
		t.Errorf("a spent proof was accepted again: %v", err)
	}
	nextClaims, err := s.ParseAndVerifyJWT(next)
	if err != nil || nextClaims.Did != d.id || nextClaims.Sid != claims.Sid {
		t.Fatalf("the rotated token lost its binding: %+v %v", nextClaims, err)
	}

	if err := s.RevokeDevice(ctx, "anchat", deviceOwner, d.id, byAccount); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if !mustRevoked(s, nextClaims) {
		t.Error("an access token of a revoked device is still accepted")
	}
	if _, _, _, _, err := s.RefreshToken(ctx, rotated, "anchat", proofFor(d, DeviceProofRefresh, "anchat", rotated)); err == nil {
		t.Error("a revoked device refreshed its session")
	}
	key, _ := ParseDeviceKey([]byte(d.jwk))
	if _, err := s.EnrolDevice(ctx, "anchat", deviceOwner, key, "", DeviceStateActive, ""); !errors.Is(err, ErrDeviceRevoked) {
		t.Errorf("a revoked device key signed in again: %v", err)
	}
}

// Property 3: the account's other devices stay signed in.
func TestRevokeDevice_leavesTheAccountsOtherDevices(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	grantEndUser(t, s)
	lost, kept := p256Device(t), p256Device(t)
	mustEnrol(t, s, deviceOwner, lost, DeviceStateActive)
	mustEnrol(t, s, deviceOwner, kept, DeviceStateActive)
	keptAccess, keptRefresh, _, err := s.IssueDeviceTokens(ctx, deviceOwner, "anchat", kept.id)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	if err := s.RevokeDevice(ctx, "anchat", deviceOwner, lost.id, byAccount); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.ParseAndVerifyJWT(keptAccess); err != nil {
		t.Errorf("revoking one device signed another out: %v", err)
	}
	if _, _, _, _, err := s.RefreshToken(ctx, keptRefresh, "anchat", proofFor(kept, DeviceProofRefresh, "anchat", keptRefresh)); err != nil {
		t.Errorf("the kept device could not refresh: %v", err)
	}
	revoked, err := s.RevokedDevices(ctx, "anchat", []string{lost.id, kept.id})
	if err != nil || !revoked[lost.id] || revoked[kept.id] {
		t.Errorf("RevokedDevices = %v, %v", revoked, err)
	}
	// Somebody else's account cannot revoke it, and revoking twice finishes
	// rather than refuses.
	if err := s.RevokeDevice(ctx, "anchat", "0xb0b", kept.id, byAccount); !errors.Is(err, ErrDeviceNotFound) {
		t.Errorf("another account revoked this one's device: %v", err)
	}
	if err := s.RevokeDevice(ctx, "anchat", deviceOwner, lost.id, byAccount); err != nil {
		t.Errorf("repeating a revocation failed: %v", err)
	}
}

func TestEndSession_refusesItsAccessTokensAtOnce(t *testing.T) {
	s, db, nsID := realRegistry(t)
	grantEndUser(t, s)
	ctx := context.Background()
	access, _, _, err := s.IssueTokens(ctx, deviceOwner, "anchat")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, _ := s.ParseAndVerifyJWT(access)
	res, err := db.Query(ctx, `SELECT id FROM refresh_tokens WHERE namespace_id = ? AND subject = ?`, nsID, deviceOwner)
	if err != nil || len(res.Rows) == 0 {
		t.Fatalf("find the session: %v", err)
	}
	if err := s.EndSession(ctx, "anchat", deviceOwner, cellInt64(res.Rows[0][0])); err != nil {
		t.Fatalf("end: %v", err)
	}
	if !mustRevoked(s, claims) {
		t.Error("an ended session's access token is still accepted")
	}
}

func TestDevicePolicy_defaultsToOptionalAndSparesOperators(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	if p, err := s.DevicePolicyOf(ctx, "anchat"); err != nil || p != DevicePolicyOptional {
		t.Fatalf("a namespace with no policy has %q, %v", p, err)
	}
	if _, err := s.SetDevicePolicy(ctx, "anchat", DevicePolicyApproval, "0xowner"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.Grant(ctx, GrantRequest{Namespace: "anchat", PrincipalType: PrincipalWallet,
		Identifier: "0xowner", Role: RoleAdmin, CreatedBy: "0xowner"}); err != nil {
		t.Fatalf("grant admin: %v", err)
	}
	if err := s.Grant(ctx, GrantRequest{Namespace: "anchat", PrincipalType: PrincipalWallet,
		Identifier: deviceOwner, Role: RoleRuntime, CreatedBy: "0xowner"}); err != nil {
		t.Fatalf("grant runtime: %v", err)
	}
	if p, _ := s.DevicePolicyFor(ctx, "anchat", deviceOwner); p != DevicePolicyApproval {
		t.Errorf("an end user is held to %q, want approval", p)
	}
	if p, _ := s.DevicePolicyFor(ctx, "anchat", "0xowner"); p != DevicePolicyOptional {
		t.Errorf("an admin is held to %q; the CLI has no device key and would be locked out", p)
	}
	if _, err := ParseDevicePolicy("sometimes"); err == nil {
		t.Error("an unknown policy was accepted")
	}
}

// A policy another gateway just set holds at once for a path that issues a
// credential; only a refresh may be judged by what this gateway read earlier,
// and a fresh read replaces that.
func TestDevicePolicyFor_readsThePolicyThatIsRecordedNow(t *testing.T) {
	s, db, nsID := realRegistry(t)
	ctx := context.Background()
	if p, err := s.refreshDevicePolicyFor(ctx, "anchat", deviceOwner); err != nil || p != DevicePolicyOptional {
		t.Fatalf("before any policy: %q, %v", p, err)
	}
	if _, err := db.Query(ctx, `INSERT INTO namespace_session_policy(namespace_id, device_policy, updated_by, updated_at)
		VALUES (?, 'required', '0xowner', datetime('now'))`, nsID); err != nil {
		t.Fatalf("another gateway sets the policy: %v", err)
	}
	if p, err := s.DevicePolicyFor(ctx, "anchat", deviceOwner); err != nil || p != DevicePolicyRequired {
		t.Errorf("a sign-in right after the policy was set is held to %q, %v; want required", p, err)
	}
	if p, _ := s.refreshDevicePolicyFor(ctx, "anchat", deviceOwner); p != DevicePolicyRequired {
		t.Errorf("a refresh after a fresh read answered %q; the fresh read should have replaced the cached policy", p)
	}
}

// Turning the policy on ends an end user's account-level session at its next
// refresh; it does not leave it refreshing for ever beside the requirement.
func TestRefreshToken_anAccountSessionEndsWhenThePolicyRequiresDevices(t *testing.T) {
	s, _, _ := realRegistry(t)
	grantEndUser(t, s)
	ctx := context.Background()
	_, refresh, _, err := s.IssueTokens(ctx, deviceOwner, "anchat")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := s.SetDevicePolicy(ctx, "anchat", DevicePolicyRequired, "0xowner"); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	if _, _, _, _, err := s.RefreshToken(ctx, refresh, "anchat", nil); !errors.Is(err, ErrDeviceRequired) {
		t.Errorf("an account-level session refreshed in a namespace that requires devices: %v", err)
	}
}

func TestHasActiveDevice_ignoresPendingAndRevoked(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	mustEnrol(t, s, deviceOwner, p256Device(t), DeviceStatePending)
	if has, err := s.HasActiveDevice(ctx, "anchat", deviceOwner); err != nil || has {
		t.Errorf("a pending device counted as active: %v %v", has, err)
	}
	active := mustEnrol(t, s, deviceOwner, p256Device(t), DeviceStateActive)
	if has, _ := s.HasActiveDevice(ctx, "anchat", deviceOwner); !has {
		t.Error("an active device was not found")
	}
	if _, err := s.RequireActiveDevice(ctx, "anchat", "0xb0b", active.ID); !errors.Is(err, ErrDeviceNotFound) {
		t.Errorf("another account's device was accepted as the caller's: %v", err)
	}
}

// staleDeviceReads is a follower that has applied the device's pending row and
// not yet the activation: the first read of a device is remembered and every
// later read of it returns that snapshot.
type staleDeviceReads struct {
	*sqliteDatabase
	snapshot *client.QueryResult
}

func (s *staleDeviceReads) Query(ctx context.Context, query string, args ...interface{}) (*client.QueryResult, error) {
	if !strings.Contains(query, "FROM session_devices") || !strings.Contains(query, "WHERE id") {
		return s.sqliteDatabase.Query(ctx, query, args...)
	}
	if s.snapshot == nil {
		res, err := s.sqliteDatabase.Query(ctx, query, args...)
		s.snapshot = res
		return res, err
	}
	return s.snapshot, nil
}

func TestEnrolDevice_approvingAPendingDeviceIsActiveEvenWhenTheLocalReadIsStale(t *testing.T) {
	s, db, _ := realRegistry(t)
	d := ed25519Device(t)
	mustEnrol(t, s, deviceOwner, d, DeviceStatePending)
	s.orm = hidingNet{db: &staleDeviceReads{sqliteDatabase: db}}
	key, err := ParseDeviceKey([]byte(d.jwk))
	if err != nil {
		t.Fatal(err)
	}
	dev, err := s.EnrolDevice(context.Background(), "anchat", deviceOwner, key, "phone", DeviceStateActive, "approver")
	if err != nil {
		t.Fatalf("approving a pending device: %v", err)
	}
	if dev.State != DeviceStateActive || dev.ApprovedBy != "approver" {
		t.Fatalf("an approved device answered %+v", dev)
	}
	s.orm = &sqliteNet{db: db}
	got, err := s.Device(context.Background(), "anchat", d.id)
	if err != nil || got.State != DeviceStateActive {
		t.Fatalf("stored device = %+v, err %v", got, err)
	}
}

func TestEnrolDevice_aRevokedDeviceIsStillRefusedOnApproval(t *testing.T) {
	s, db, _ := realRegistry(t)
	d := ed25519Device(t)
	mustEnrol(t, s, deviceOwner, d, DeviceStatePending)
	if _, err := db.Query(context.Background(), `UPDATE session_devices SET state = 'revoked' WHERE id = ?`, d.id); err != nil {
		t.Fatal(err)
	}
	key, _ := ParseDeviceKey([]byte(d.jwk))
	if _, err := s.EnrolDevice(context.Background(), "anchat", deviceOwner, key, "phone", DeviceStateActive, ""); !errors.Is(err, ErrDeviceRevoked) {
		t.Fatalf("err = %v, want ErrDeviceRevoked", err)
	}
}

// grantEndUser makes deviceOwner a runtime member of anchat: a refresh of a
// wallet holding no grant ends when the namespace is not open to such wallets,
// and these tests are about the device, not about that.
func grantEndUser(t *testing.T, s *Service) {
	t.Helper()
	if err := s.Grant(context.Background(), GrantRequest{Namespace: "anchat", PrincipalType: PrincipalWallet,
		Identifier: deviceOwner, Role: RoleRuntime, CreatedBy: "0xowner"}); err != nil {
		t.Fatalf("grant runtime: %v", err)
	}
}
