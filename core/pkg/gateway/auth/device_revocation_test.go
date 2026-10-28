package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// byAccount is a revocation from a session bound to no device.
var byAccount = DeviceRevocation{By: RevokedByAccount}

// revokedRecord is the revocation a refusal of a revoked device carries.
func revokedRecord(t *testing.T, err error) *DeviceRevocationRecord {
	t.Helper()
	var revoked *DeviceRevokedError
	if !errors.As(err, &revoked) || !errors.Is(err, ErrDeviceRevoked) {
		t.Fatalf("err = %v, want a DeviceRevokedError", err)
	}
	if revoked.Revocation == nil {
		t.Fatal("the refusal carries no revocation")
	}
	return revoked.Revocation
}

// Property: a device revoked by another of the account's devices is refused,
// on every path that refuses it, with that device's proof, which verifies with
// the revoker's key over the statement naming the revoked device and the
// namespace — and with the application's evidence, verbatim.
func TestRevokeDevice_byADeviceKeepsItsProofForEveryRefusal(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	phone, lost := ed25519Device(t), p256Device(t)
	revoker := mustEnrol(t, s, deviceOwner, phone, DeviceStateActive)
	mustEnrol(t, s, deviceOwner, lost, DeviceStateActive)

	proof := proofFor(phone, DeviceProofRevoke, "anchat", lost.id)
	evidence := `{"roster_version":7,"account_sig":"c2ln"}`
	if err := s.RevokeDevice(ctx, "anchat", deviceOwner, lost.id, DeviceRevocationByDevice("anchat", revoker, proof, evidence)); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	key, err := ParseDeviceKey([]byte(lost.jwk))
	if err != nil {
		t.Fatal(err)
	}
	refusals := map[string]error{}
	_, refusals["sign-in"] = s.EnrolDevice(ctx, "anchat", deviceOwner, key, "", DeviceStateActive, "")
	_, refusals["active device"] = s.RequireActiveDevice(ctx, "anchat", deviceOwner, lost.id)
	_, refusals["session device"] = s.RequireSessionDevice(ctx, "anchat", deviceOwner, lost.id)
	// Starting a link proves nothing about the caller, so it is refused with
	// no record (feat-976 review): anyone holding a revoked device's public
	// key could otherwise read the revoker's key, proof and evidence.
	_, linkErr := s.StartDeviceLink(ctx, "anchat", "", key, "")
	var shown *DeviceRevokedError
	if !errors.Is(linkErr, ErrDeviceRevoked) || errors.As(linkErr, &shown) {
		t.Errorf("starting a link with a revoked key: %v, want ErrDeviceRevoked with no revocation", linkErr)
	}
	for path, err := range refusals {
		rec := revokedRecord(t, err)
		if rec.DeviceID != lost.id || rec.RevokedBy != RevokedByDevice || rec.RevokedAt == nil {
			t.Errorf("%s: record %+v", path, rec)
		}
		if rec.Evidence == nil || *rec.Evidence != evidence {
			t.Errorf("%s: evidence %v, want it verbatim", path, rec.Evidence)
		}
		p := rec.Proof
		if p == nil || p.DeviceID != phone.id || p.Namespace != "anchat" {
			t.Fatalf("%s: proof %+v", path, p)
		}
		signer, err := ParseDeviceKey(p.PublicKey)
		if err != nil || signer.ID() != p.DeviceID {
			t.Fatalf("%s: the proof's key %s is not device %s: %v", path, p.PublicKey, p.DeviceID, err)
		}
		if err := signer.Verify(DeviceProofMessage(DeviceProofRevoke, p.Namespace, rec.DeviceID, p.IssuedAt, p.ID), p.Signature); err != nil {
			t.Errorf("%s: the stored proof does not verify: %v", path, err)
		}
		if signer.Verify(DeviceProofMessage(DeviceProofRevoke, p.Namespace, phone.id, p.IssuedAt, p.ID), p.Signature) == nil {
			t.Errorf("%s: the proof verifies for another device", path)
		}
		if signer.Verify(DeviceProofMessage(DeviceProofRevoke, "elsewhere", rec.DeviceID, p.IssuedAt, p.ID), p.Signature) == nil {
			t.Errorf("%s: the proof verifies in another namespace", path)
		}
	}
}

// Property: a revocation repeated to finish one that stopped half way keeps
// the first record; an operator cannot replace a device's signed proof with
// none.
func TestRevokeDevice_aRepeatKeepsTheFirstRecord(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	phone, lost := ed25519Device(t), ed25519Device(t)
	revoker := mustEnrol(t, s, deviceOwner, phone, DeviceStateActive)
	mustEnrol(t, s, deviceOwner, lost, DeviceStateActive)
	proof := proofFor(phone, DeviceProofRevoke, "anchat", lost.id)
	if err := s.RevokeDevice(ctx, "anchat", deviceOwner, lost.id, DeviceRevocationByDevice("anchat", revoker, proof, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeDevice(ctx, "anchat", deviceOwner, lost.id, DeviceRevocation{By: RevokedByOperator, Evidence: "later"}); err != nil {
		t.Fatalf("a repeat was refused: %v", err)
	}
	_, err := s.RequireSessionDevice(ctx, "anchat", deviceOwner, lost.id)
	rec := revokedRecord(t, err)
	if rec.RevokedBy != RevokedByDevice || rec.Proof == nil || rec.Proof.Signature != proof.Signature || rec.Evidence != nil {
		t.Errorf("after a repeat the record is %+v, want the first", rec)
	}
}

// Property: what holds no account signature says who revoked it and carries
// no proof — an operator, a session bound to no device, a function, and a
// device revoked before the gateway kept the record — so the client can tell
// it from a proven one.
func TestRevokeDevice_unsignedRevocationsSayWhoAndCarryNoProof(t *testing.T) {
	s, db, _ := realRegistry(t)
	ctx := context.Background()
	if _, err := s.SetDevicePolicy(ctx, "anchat", DevicePolicyFunction, "0xowner"); err != nil {
		t.Fatal(err)
	}
	first, byOperator, byWallet, legacy := ed25519Device(t), ed25519Device(t), ed25519Device(t), ed25519Device(t)
	mustEnrol(t, s, deviceOwner, first, DeviceStateActive)
	for _, d := range []testDevice{byOperator, byWallet, legacy} {
		mustEnrol(t, s, deviceOwner, d, DeviceStateActive)
	}
	pendingKey, _ := ParseDeviceKey([]byte(ed25519Device(t).jwk))
	pending, err := s.EnrolFirstOrPendingDevice(ctx, "anchat", deviceOwner, pendingKey, "")
	if err != nil || pending.State != DeviceStatePending {
		t.Fatalf("pending device: %+v %v", pending, err)
	}
	if err := s.RevokeDevice(ctx, "anchat", deviceOwner, byOperator.id, DeviceRevocation{By: RevokedByOperator, Evidence: "roster-v9"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeDevice(ctx, "anchat", deviceOwner, byWallet.id, byAccount); err != nil {
		t.Fatal(err)
	}
	if err := s.DenyPendingDevice(ctx, "anchat", deviceOwner, pending.ID, "function:join", "join-refused-v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE session_devices SET state = 'revoked', revoked_at = datetime('now') WHERE id = ?`, legacy.id); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]RevokedBy{
		byOperator.id: RevokedByOperator, byWallet.id: RevokedByAccount,
		pending.ID: RevokedByFunction, legacy.id: RevokedByUnrecorded,
	} {
		_, err := s.RequireSessionDevice(ctx, "anchat", deviceOwner, id)
		rec := revokedRecord(t, err)
		if rec.RevokedBy != want || rec.Proof != nil || rec.DeviceID != id {
			t.Errorf("%s: record %+v, want %s with no proof", want, rec, want)
		}
	}
	_, err = s.RequireSessionDevice(ctx, "anchat", deviceOwner, byOperator.id)
	if rec := revokedRecord(t, err); rec.Evidence == nil || *rec.Evidence != "roster-v9" {
		t.Errorf("the operator's evidence was not kept: %v", rec.Evidence)
	}
	_, err = s.RequireSessionDevice(ctx, "anchat", deviceOwner, pending.ID)
	if rec := revokedRecord(t, err); rec.Evidence == nil || *rec.Evidence != "join-refused-v1" {
		t.Errorf("the function's evidence was not kept: %v", rec.Evidence)
	}
}

// Property: a function's evidence is held to the same bound as any other
// revocation's, and a denial refused for it leaves the device pending.
func TestDenyPendingDevice_evidencePastTheBoundDeniesNothing(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	if _, err := s.SetDevicePolicy(ctx, "anchat", DevicePolicyFunction, "0xowner"); err != nil {
		t.Fatal(err)
	}
	mustEnrol(t, s, deviceOwner, ed25519Device(t), DeviceStateActive)
	key, _ := ParseDeviceKey([]byte(ed25519Device(t).jwk))
	pending, err := s.EnrolFirstOrPendingDevice(ctx, "anchat", deviceOwner, key, "")
	if err != nil || pending.State != DeviceStatePending {
		t.Fatalf("pending device: %+v %v", pending, err)
	}
	for name, evidence := range map[string]string{
		"past the bound": strings.Repeat("e", MaxRevocationEvidenceBytes+1),
		"not UTF-8":      "\xff",
	} {
		if err := s.DenyPendingDevice(ctx, "anchat", deviceOwner, pending.ID, "function:join", evidence); !errors.Is(err, ErrRevocationEvidenceInvalid) {
			t.Errorf("evidence %s: %v, want ErrRevocationEvidenceInvalid", name, err)
		}
	}
	if d, err := s.RequireSessionDevice(ctx, "anchat", deviceOwner, pending.ID); err != nil || d.State != DeviceStatePending {
		t.Errorf("a refused denial changed the device: %+v %v", d, err)
	}
}

// Property: a revocation that is not what it claims to be stores nothing and
// revokes nothing.
func TestRevokeDevice_refusesAMalformedRecord(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	lost := ed25519Device(t)
	mustEnrol(t, s, deviceOwner, lost, DeviceStateActive)
	for name, how := range map[string]DeviceRevocation{
		"a device's without its proof": {By: RevokedByDevice},
		"an operator's with a proof":   {By: RevokedByOperator, Proof: &RevocationProof{}},
		"an unknown kind":              {By: "gateway"},
		"no kind":                      {},
		"evidence past the bound":      {By: RevokedByOperator, Evidence: strings.Repeat("e", MaxRevocationEvidenceBytes+1)},
		"evidence that is not UTF-8":   {By: RevokedByOperator, Evidence: "\xff\xfe"},
	} {
		if err := s.RevokeDevice(ctx, "anchat", deviceOwner, lost.id, how); err == nil {
			t.Errorf("%s was stored", name)
		}
	}
	if _, err := s.RequireActiveDevice(ctx, "anchat", deviceOwner, lost.id); err != nil {
		t.Errorf("a refused revocation revoked the device: %v", err)
	}
	if err := s.RevokeDevice(ctx, "anchat", deviceOwner, lost.id,
		DeviceRevocation{By: RevokedByOperator, Evidence: strings.Repeat("e", MaxRevocationEvidenceBytes)}); err != nil {
		t.Errorf("evidence at the bound was refused: %v", err)
	}
}

// feat-976: a token refused because its device was revoked is told apart from
// one refused for its session, so the refusal can carry the device's
// revocation; it is still ErrTokenRevoked to whatever does not tell them
// apart.
func TestParseAndVerifyJWT_aRevokedDevicesTokenNamesItsDevice(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	lost := ed25519Device(t)
	mustEnrol(t, s, deviceOwner, lost, DeviceStateActive)
	onDevice, _, err := s.GenerateBoundJWT("anchat", deviceOwner, AccessTokenLifetime, nil, SessionBinding{DeviceID: lost.id})
	if err != nil {
		t.Fatal(err)
	}
	unbound, _, err := s.GenerateBoundJWT("anchat", deviceOwner, AccessTokenLifetime, nil, SessionBinding{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeDevice(ctx, "anchat", deviceOwner, lost.id,
		DeviceRevocation{By: RevokedByAccount, Evidence: "roster-v8"}); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	_, err = s.ParseAndVerifyJWT(onDevice)
	var revoked *DeviceTokenRevokedError
	if !errors.As(err, &revoked) || !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("err = %v, want a DeviceTokenRevokedError that is ErrTokenRevoked", err)
	}
	if revoked.Claims.Did != lost.id || revoked.Claims.Namespace != "anchat" || !s.DeviceRevoked(revoked.Claims) {
		t.Errorf("claims %+v do not name the revoked device", revoked.Claims)
	}
	rec, err := s.DeviceRevocationRecord(ctx, "anchat", lost.id)
	if err != nil || rec.RevokedBy != RevokedByAccount || rec.Evidence == nil || *rec.Evidence != "roster-v8" {
		t.Errorf("record %+v (%v), want the account's revocation with its evidence", rec, err)
	}

	claims, err := s.ParseAndVerifyJWT(unbound)
	if err != nil {
		t.Fatalf("a token bound to no device was refused: %v", err)
	}
	if err := s.RevokeSession(ctx, claims); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ParseAndVerifyJWT(unbound); !errors.Is(err, ErrTokenRevoked) || errors.As(err, &revoked) {
		t.Errorf("a revoked session's token: %v, want plain ErrTokenRevoked", err)
	}
	if s.DeviceRevoked(claims) {
		t.Error("a token bound to no device reported its device revoked")
	}
}

// feat-976 review: the list alone decided DEVICE_REVOKED, so a device whose
// row was not revoked, or not there, was told it was, as "unrecorded". The
// registry decides now: no row, or no namespace, is ErrDeviceNotRevoked; a row
// that is not revoked is ErrDeviceRevocationUnconfirmed; neither gives a
// record.
func TestDeviceRevocationRecord_onlyARevokedRowHasOne(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	healthy := ed25519Device(t)
	mustEnrol(t, s, deviceOwner, healthy, DeviceStateActive)

	for name, tc := range map[string]struct {
		namespace, id string
		want          error
	}{
		"no such device":    {"anchat", "gone", ErrDeviceNotRevoked},
		"no such namespace": {"deleted-namespace", healthy.id, ErrDeviceNotRevoked},
		"an active device":  {"anchat", healthy.id, ErrDeviceRevocationUnconfirmed},
	} {
		if rec, err := s.DeviceRevocationRecord(ctx, tc.namespace, tc.id); rec != nil || !errors.Is(err, tc.want) {
			t.Errorf("%s: record %+v, err %v, want %v", name, rec, err, tc.want)
		}
	}
	// The socket sweeper asks the same: a device the registry does not
	// confirm is not closed as a revoked device.
	if err := s.revocations.RevokeDevice(ctx, "anchat", healthy.id); err != nil {
		t.Fatal(err)
	}
	if s.DeviceRevoked(&JWTClaims{Namespace: "anchat", Did: healthy.id}) {
		t.Error("the sweeper would tell a device whose row is active that it was revoked")
	}
}

// feat-976 review: every refusal of a revoked device read the registry. A
// tombstone's record never changes, so it is read once; what is not a record
// is read again.
func TestDeviceRevocationRecord_aRecordIsReadOnce(t *testing.T) {
	s, db, _ := realRegistry(t)
	ctx := context.Background()
	lost, pending := ed25519Device(t), ed25519Device(t)
	mustEnrol(t, s, deviceOwner, lost, DeviceStateActive)
	if err := s.RevokeDevice(ctx, "anchat", deviceOwner, lost.id, DeviceRevocation{By: RevokedByAccount, Evidence: "v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeviceRevocationRecord(ctx, "anchat", lost.id); err != nil {
		t.Fatal(err)
	}
	mustEnrol(t, s, deviceOwner, pending, DeviceStateActive)
	if _, err := s.DeviceRevocationRecord(ctx, "anchat", pending.id); !errors.Is(err, ErrDeviceRevocationUnconfirmed) {
		t.Fatalf("an active device: %v", err)
	}
	if _, err := db.db.Exec(`UPDATE session_devices SET state = 'revoked', revoked_by = 'operator', revoked_at = datetime('now') WHERE id = ?`, pending.id); err != nil {
		t.Fatal(err)
	}
	if rec, err := s.DeviceRevocationRecord(ctx, "anchat", pending.id); err != nil || rec.RevokedBy != RevokedByOperator {
		t.Errorf("a device revoked after it was asked about: %+v %v; a result that was no record was kept", rec, err)
	}

	if _, err := db.db.Exec(`DROP TABLE session_devices`); err != nil {
		t.Fatal(err)
	}
	rec, err := s.DeviceRevocationRecord(ctx, "anchat", lost.id)
	if err != nil || rec.Evidence == nil || *rec.Evidence != "v1" {
		t.Errorf("a record already read: %+v %v, want it without a read", rec, err)
	}
}

// feat-976 review: revocation was checked before expiry, so a revoked
// device's expired tokens, presented for the seven days its list entry lives,
// each cost a read of the registry. An expired token is answered as expired;
// the refresh it prompts says the device was revoked.
func TestParseAndVerifyJWT_anExpiredTokenOfARevokedDeviceIsExpired(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	lost := ed25519Device(t)
	mustEnrol(t, s, deviceOwner, lost, DeviceStateActive)
	token, _, err := s.GenerateBoundJWT("anchat", deviceOwner, -2*time.Minute, nil, SessionBinding{DeviceID: lost.id})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeDevice(ctx, "anchat", deviceOwner, lost.id, byAccount); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ParseAndVerifyJWT(token); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("an expired token of a revoked device: %v, want ErrTokenExpired", err)
	}
}
