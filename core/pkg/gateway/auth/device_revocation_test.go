package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
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
	_, refusals["device link"] = s.StartDeviceLink(ctx, "anchat", "", key, "")
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
	if err := s.DenyPendingDevice(ctx, "anchat", deviceOwner, pending.ID, "function:join"); err != nil {
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
		"an operator's with a proof":    {By: RevokedByOperator, Proof: &RevocationProof{}},
		"an unknown kind":               {By: "gateway"},
		"no kind":                       {},
		"evidence past the bound":       {By: RevokedByOperator, Evidence: strings.Repeat("e", MaxRevocationEvidenceBytes+1)},
		"evidence that is not UTF-8":    {By: RevokedByOperator, Evidence: "\xff\xfe"},
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
