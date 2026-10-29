package auth

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// The function device policy (feat-973), against the real schema: what keeps a
// pending device at arm's length is the predicates in the SQL and the token's
// audience, and a fake would model them.

func functionPolicy(t *testing.T, s *Service) {
	t.Helper()
	grantEndUser(t, s)
	if _, err := s.SetDevicePolicy(context.Background(), "anchat", DevicePolicyFunction, "0xowner"); err != nil {
		t.Fatalf("set the function policy: %v", err)
	}
}

func enrolUnderFunction(t *testing.T, s *Service, d testDevice) *SessionDevice {
	t.Helper()
	key, err := ParseDeviceKey([]byte(d.jwk))
	if err != nil {
		t.Fatal(err)
	}
	dev, err := s.EnrolFirstOrPendingDevice(context.Background(), "anchat", deviceOwner, key, "phone")
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	return dev
}

func TestParseDevicePolicy_function(t *testing.T) {
	if p, err := ParseDevicePolicy("function"); err != nil || p != DevicePolicyFunction {
		t.Fatalf("function parsed as %q, %v", p, err)
	}
}

// The account's first device is active; every later one waits, and so does
// the next device of an account whose devices were all revoked: recovery is
// the application's to check.
func TestEnrolFirstOrPendingDevice_onlyTheFirstEverIsActive(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	functionPolicy(t, s)

	first := enrolUnderFunction(t, s, ed25519Device(t))
	if first.State != DeviceStateActive {
		t.Fatalf("the account's first device is %s, want active", first.State)
	}
	second := enrolUnderFunction(t, s, p256Device(t))
	if second.State != DeviceStatePending {
		t.Fatalf("a second device is %s, want pending", second.State)
	}
	if err := s.RevokeDevice(ctx, "anchat", deviceOwner, first.ID, byAccount); err != nil {
		t.Fatal(err)
	}
	if err := s.DenyPendingDevice(ctx, "anchat", deviceOwner, second.ID, "function:test", ""); err != nil {
		t.Fatal(err)
	}
	if third := enrolUnderFunction(t, s, ed25519Device(t)); third.State != DeviceStatePending {
		t.Errorf("an account whose devices were all revoked enrolled one %s; recovery must wait for the function", third.State)
	}
	// A denied device does not sign in again, as pending or otherwise.
	key, _ := ParseDeviceKey([]byte(second.PublicKey))
	if _, err := s.EnrolFirstOrPendingDevice(ctx, "anchat", deviceOwner, key, ""); !errors.Is(err, ErrDeviceRevoked) {
		t.Errorf("a denied device signed in again: %v, want ErrDeviceRevoked", err)
	}
}

// Two first sign-ins racing each other cannot both be first.
//
// rqlite applies the statements of racing gateways one at a time through its
// log; one connection to the in-memory database is that, and the check and
// the insert being one statement is what is under test.
func TestEnrolFirstOrPendingDevice_racingFirstSignInsMakeOneActive(t *testing.T) {
	s, db, _ := realRegistry(t)
	db.db.SetMaxOpenConns(1)
	functionPolicy(t, s)

	const racers = 8
	devices := make([]testDevice, racers)
	for i := range devices {
		devices[i] = ed25519Device(t)
	}
	states := make([]DeviceState, racers)
	var wg sync.WaitGroup
	for i := range devices {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key, _ := ParseDeviceKey([]byte(devices[i].jwk))
			dev, err := s.EnrolFirstOrPendingDevice(context.Background(), "anchat", deviceOwner, key, "")
			if err != nil {
				t.Errorf("enrol %d: %v", i, err)
				return
			}
			states[i] = dev.State
		}(i)
	}
	wg.Wait()
	active := 0
	for _, st := range states {
		if st == DeviceStateActive {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("%d racing first sign-ins were active, want exactly 1 (%v)", active, states)
	}
}

// A pending device's session is a restricted token until a function approves
// the device; its first refresh after the approval is a full session, and it
// stops refreshing as pending if the namespace leaves the policy.
func TestPendingDeviceSession_refreshFollowsTheApproval(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	functionPolicy(t, s)
	enrolUnderFunction(t, s, ed25519Device(t))
	d := p256Device(t)
	dev := enrolUnderFunction(t, s, d)

	access, refresh, _, err := s.IssuePendingDeviceTokens(ctx, deviceOwner, "anchat", dev.ID)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := s.ParseAndVerifyJWT(access)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !claims.PendingDevice() || claims.DeviceState() != CallerDevicePending || claims.Did != dev.ID {
		t.Fatalf("the pending session's token is %+v", claims)
	}

	next, rotated, _, _, err := s.RefreshToken(ctx, refresh, "anchat", proofFor(d, DeviceProofRefresh, "anchat", refresh))
	if err != nil {
		t.Fatalf("a pending device could not refresh its restricted session: %v", err)
	}
	if MintedDeviceState(next) != CallerDevicePending {
		t.Fatalf("a refresh before the approval minted %q, want pending", MintedDeviceState(next))
	}

	if err := s.ApprovePendingDevice(ctx, "anchat", deviceOwner, dev.ID, "function:device-join"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	full, _, _, _, err := s.RefreshToken(ctx, rotated, "anchat", proofFor(d, DeviceProofRefresh, "anchat", rotated))
	if err != nil {
		t.Fatalf("refresh after the approval: %v", err)
	}
	fullClaims, err := s.ParseAndVerifyJWT(full)
	if err != nil || fullClaims.PendingDevice() || fullClaims.DeviceState() != CallerDeviceActive {
		t.Fatalf("the first refresh after the approval is %+v, %v; want a full session", fullClaims, err)
	}
}

func TestPendingDeviceSession_endsWhenTheNamespaceLeavesThePolicy(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	functionPolicy(t, s)
	enrolUnderFunction(t, s, ed25519Device(t))
	d := p256Device(t)
	dev := enrolUnderFunction(t, s, d)
	_, refresh, _, err := s.IssuePendingDeviceTokens(ctx, deviceOwner, "anchat", dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDevicePolicy(ctx, "anchat", DevicePolicyRequired, "0xowner"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := s.RefreshToken(ctx, refresh, "anchat", proofFor(d, DeviceProofRefresh, "anchat", refresh)); !errors.Is(err, ErrDevicePending) {
		t.Errorf("a pending device refreshed under the required policy: %v", err)
	}
}

// Denying ends the restricted session at once: the access token is revoked
// and the refresh token no longer refreshes.
func TestDenyPendingDevice_endsTheRestrictedSession(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	functionPolicy(t, s)
	first := enrolUnderFunction(t, s, ed25519Device(t))
	d := p256Device(t)
	dev := enrolUnderFunction(t, s, d)
	access, refresh, _, err := s.IssuePendingDeviceTokens(ctx, deviceOwner, "anchat", dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := s.ParseAndVerifyJWT(access)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DenyPendingDevice(ctx, "anchat", deviceOwner, dev.ID, "function:device-join", ""); err != nil {
		t.Fatalf("deny: %v", err)
	}
	if !mustRevoked(s, claims) {
		t.Error("a denied device's access token is still accepted")
	}
	if _, _, _, _, err := s.RefreshToken(ctx, refresh, "anchat", proofFor(d, DeviceProofRefresh, "anchat", refresh)); err == nil {
		t.Error("a denied device refreshed its session")
	}
	// Denied again: done already, its sessions ended again.
	if err := s.DenyPendingDevice(ctx, "anchat", deviceOwner, dev.ID, "function:device-join", ""); err != nil {
		t.Errorf("a second denial failed: %v", err)
	}
	// An approval cannot revive it.
	if err := s.ApprovePendingDevice(ctx, "anchat", deviceOwner, dev.ID, "function:device-join"); !errors.Is(err, ErrDeviceRevoked) {
		t.Errorf("approving a denied device: %v, want ErrDeviceRevoked", err)
	}
	// An active device is its account's to revoke, not a function's to deny.
	if err := s.DenyPendingDevice(ctx, "anchat", deviceOwner, first.ID, "function:device-join", ""); !errors.Is(err, ErrDeviceNotPending) {
		t.Errorf("denying an active device: %v, want ErrDeviceNotPending", err)
	}
}

func TestApprovePendingDevice_needsTheFunctionPolicyAndTheAccountsDevice(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	functionPolicy(t, s)
	enrolUnderFunction(t, s, ed25519Device(t))
	dev := enrolUnderFunction(t, s, p256Device(t))

	if err := s.ApprovePendingDevice(ctx, "anchat", "0xb0b", dev.ID, "function:x"); !errors.Is(err, ErrDeviceNotFound) {
		t.Errorf("approving another account's device: %v, want ErrDeviceNotFound", err)
	}
	if err := s.DenyPendingDevice(ctx, "anchat", "0xb0b", dev.ID, "function:x", ""); !errors.Is(err, ErrDeviceNotFound) {
		t.Errorf("denying another account's device: %v, want ErrDeviceNotFound", err)
	}

	if _, err := s.SetDevicePolicy(ctx, "anchat", DevicePolicyApproval, "0xowner"); err != nil {
		t.Fatal(err)
	}
	if err := s.ApprovePendingDevice(ctx, "anchat", deviceOwner, dev.ID, "function:x"); !errors.Is(err, ErrDeviceAdmissionNotFunction) {
		t.Errorf("approving under the approval policy: %v, want ErrDeviceAdmissionNotFunction", err)
	}
	if _, err := s.SetDevicePolicy(ctx, "anchat", DevicePolicyFunction, "0xowner"); err != nil {
		t.Fatal(err)
	}

	if err := s.ApprovePendingDevice(ctx, "anchat", deviceOwner, dev.ID, "function:x"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if got, err := s.Device(ctx, "anchat", dev.ID); err != nil || got.State != DeviceStateActive {
		t.Fatalf("an approved device is %+v, %v", got, err)
	}
	// A retry after a lost reply succeeds and changes nothing.
	if err := s.ApprovePendingDevice(ctx, "anchat", deviceOwner, dev.ID, "function:x"); err != nil {
		t.Errorf("approving an active device again: %v", err)
	}
}

// A pending device's token names its device; one that names none is not a
// token this gateway mints, however it came to be signed.
func TestParseAndVerifyJWT_aPendingTokenNamesADevice(t *testing.T) {
	s, _, _ := realRegistry(t)
	if _, _, err := s.GenerateBoundJWT("anchat", deviceOwner, time.Minute, nil, SessionBinding{Pending: true}); err == nil {
		t.Fatal("minted a pending token with no device")
	}
	now := time.Now().Unix()
	pb, _ := json.Marshal(map[string]any{
		"iss": "orama-gateway", "sub": deviceOwner, "aud": AudiencePendingDevice,
		"iat": now, "nbf": now, "exp": now + 60, "jti": "j1", "namespace": "anchat",
	})
	token, err := s.signRSA(pb)
	if s.preferEdDSA && s.edSigningKey != nil {
		token, err = s.signEdDSA(pb)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ParseAndVerifyJWT(token); err == nil {
		t.Error("a pending token naming no device was accepted")
	}
}
