package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	authsvc "github.com/Orama-Network/orama/core/pkg/gateway/auth"
)

// revocationOf is the revocation a DEVICE_REVOKED answer carries.
func revocationOf(t *testing.T, code int, body map[string]any) map[string]any {
	t.Helper()
	if code != http.StatusForbidden || body["code"] != ErrCodeDeviceRevoked {
		t.Fatalf("want 403 %s, got %d %v", ErrCodeDeviceRevoked, code, body)
	}
	rec, ok := body["revocation"].(map[string]any)
	if !ok {
		t.Fatalf("DEVICE_REVOKED without its revocation: %v", body)
	}
	return rec
}

// verifyRevocationProof checks a refusal's proof the way a client does: the
// key is the revoker's, and it signed the revoke statement for this device in
// this namespace.
func verifyRevocationProof(rec map[string]any, device, namespace string) error {
	raw, _ := json.Marshal(rec["proof"])
	var p authsvc.RevocationProof
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	key, err := authsvc.ParseDeviceKey(p.PublicKey)
	if err != nil {
		return err
	}
	if key.ID() != p.DeviceID {
		return errMismatch("the proof's key is not its device")
	}
	if rec["device_id"] != device || p.Namespace != namespace {
		return errMismatch("the proof names another device or namespace")
	}
	return key.Verify(authsvc.DeviceProofMessage(authsvc.DeviceProofRevoke, p.Namespace, device, p.IssuedAt, p.ID), p.Signature)
}

type errMismatch string

func (e errMismatch) Error() string { return string(e) }

// Property: a device revoked by another device is told so on sign-in and on
// refresh with that device's proof, which a client verifies, and with the
// application's evidence verbatim.
func TestDeviceRevoked_signInAndRefreshCarryTheRevokersProof(t *testing.T) {
	f := newFlow(t)
	w := newWallet(t)
	f.member(w.address, authsvc.RoleRuntime)
	phone, laptop := newTestDeviceKey(t), newTestDeviceKey(t)
	_, first := f.signIn(w, phone)
	code, second := f.signIn(w, laptop)
	if code != http.StatusOK {
		t.Fatalf("second device: %d %v", code, second)
	}
	evidence := "eyJyb3N0ZXJfdmVyc2lvbiI6OH0"
	if code, body := f.do(f.h.DeviceByIDHandler, http.MethodDelete, "/v1/auth/devices/"+laptop.id,
		map[string]any{"device_proof": proof(phone, authsvc.DeviceProofRevoke, laptop.id), "evidence": evidence},
		f.claimsOf(first)); code != http.StatusOK {
		t.Fatalf("revoke: %d %v", code, body)
	}

	refreshToken := second["refresh_token"].(string)
	// A finished revocation also revoked the device's refresh tokens, so its
	// refresh is refused for the token (401) before the device is read. A
	// revocation that stopped after the tombstone leaves the token live: then
	// the refresh is refused for the device, and must carry the proof too.
	if code, body := f.do(f.h.RefreshHandler, http.MethodPost, "/v1/auth/refresh", map[string]any{
		"refresh_token": refreshToken, "namespace": flowNamespace,
		"device_proof": proof(laptop, authsvc.DeviceProofRefresh, refreshToken)}, nil); code != http.StatusUnauthorized {
		t.Errorf("a revoked device's refresh token: %d %v, want 401", code, body)
	}
	if _, err := f.db.Exec(`UPDATE refresh_tokens SET revoked_at = NULL, grace_used_at = NULL WHERE device_id = ?`, laptop.id); err != nil {
		t.Fatal(err)
	}
	signIn := func() (int, map[string]any) { return f.signIn(w, laptop) }
	refresh := func() (int, map[string]any) {
		return f.do(f.h.RefreshHandler, http.MethodPost, "/v1/auth/refresh", map[string]any{
			"refresh_token": refreshToken, "namespace": flowNamespace,
			"device_proof": proof(laptop, authsvc.DeviceProofRefresh, refreshToken)}, nil)
	}
	for name, ask := range map[string]func() (int, map[string]any){"sign-in": signIn, "refresh": refresh} {
		code, body := ask()
		rec := revocationOf(t, code, body)
		if rec["revoked_by"] != string(authsvc.RevokedByDevice) || rec["evidence"] != evidence || rec["revoked_at"] == nil {
			t.Errorf("%s: revocation %v", name, rec)
		}
		if err := verifyRevocationProof(rec, laptop.id, flowNamespace); err != nil {
			t.Errorf("%s: the proof does not verify: %v", name, err)
		}
		// Negative: the same proof shown to another device, or tampered with.
		if verifyRevocationProof(rec, phone.id, flowNamespace) == nil {
			t.Errorf("%s: the proof verifies for the revoker's own device", name)
		}
		tampered := map[string]any{}
		for k, v := range rec {
			tampered[k] = v
		}
		p := map[string]any{}
		for k, v := range rec["proof"].(map[string]any) {
			p[k] = v
		}
		p["iat"] = p["iat"].(float64) + 1
		tampered["proof"] = p
		if verifyRevocationProof(tampered, laptop.id, flowNamespace) == nil {
			t.Errorf("%s: a tampered proof verifies", name)
		}
	}
}

// Property: an operator's revocation says so and carries no proof, only the
// evidence it attached; an operator cannot attach a device proof, and evidence
// past the bound revokes nothing.
func TestDeviceRevoked_anOperatorsRevocationIsUnproven(t *testing.T) {
	f := newFlow(t)
	w := newWallet(t)
	f.member(w.address, authsvc.RoleRuntime)
	lost := newTestDeviceKey(t)
	if code, body := f.signIn(w, lost); code != http.StatusOK {
		t.Fatalf("sign-in: %d %v", code, body)
	}
	operator := &authsvc.JWTClaims{Sub: "0xowner", Namespace: flowNamespace}
	path := "/v1/namespace/devices/" + lost.id
	if code, body := f.do(f.h.NamespaceDeviceByIDHandler, http.MethodDelete, path,
		map[string]any{"evidence": strings.Repeat("e", authsvc.MaxRevocationEvidenceBytes+1)}, operator); code != http.StatusBadRequest {
		t.Errorf("evidence past the bound: %d %v", code, body)
	}
	if code, body := f.do(f.h.NamespaceDeviceByIDHandler, http.MethodDelete, path,
		map[string]any{"device_proof": proof(lost, authsvc.DeviceProofRevoke, lost.id)}, operator); code != http.StatusBadRequest {
		t.Errorf("an operator's revocation with a device proof: %d %v", code, body)
	}
	if code, body := f.signIn(w, lost); code != http.StatusOK {
		t.Fatalf("a refused revocation revoked the device: %d %v", code, body)
	}
	if code, body := f.do(f.h.NamespaceDeviceByIDHandler, http.MethodDelete, path,
		map[string]any{"evidence": "roster-v3"}, operator); code != http.StatusOK {
		t.Fatalf("operator revoke: %d %v", code, body)
	}
	code, body := f.signIn(w, lost)
	rec := revocationOf(t, code, body)
	if rec["revoked_by"] != string(authsvc.RevokedByOperator) || rec["proof"] != nil || rec["evidence"] != "roster-v3" {
		t.Errorf("an operator's revocation reads %v", rec)
	}
	if _, present := rec["proof"]; !present {
		t.Error("the answer leaves proof out instead of saying null")
	}
}

// Property: a revocation from a session bound to no device (the wallet's,
// under optional) is the account's word with no signature over it.
func TestDeviceRevoked_aWalletSessionsRevocationIsUnproven(t *testing.T) {
	f := newFlow(t)
	w := newWallet(t)
	f.member(w.address, authsvc.RoleRuntime)
	lost := newTestDeviceKey(t)
	if code, body := f.signIn(w, lost); code != http.StatusOK {
		t.Fatalf("sign-in: %d %v", code, body)
	}
	_, unbound := f.signIn(w, nil)
	if code, body := f.do(f.h.DeviceByIDHandler, http.MethodDelete, "/v1/auth/devices/"+lost.id, nil, f.claimsOf(unbound)); code != http.StatusOK {
		t.Fatalf("revoke: %d %v", code, body)
	}
	code, body := f.signIn(w, lost)
	rec := revocationOf(t, code, body)
	if rec["revoked_by"] != string(authsvc.RevokedByAccount) || rec["proof"] != nil || rec["evidence"] != nil {
		t.Errorf("a wallet session's revocation reads %v", rec)
	}
}

// Property: a device link whose approver was revoked before the new device
// collected it answers access_denied, not DEVICE_REVOKED: the refusal is of
// the approval, and the new device's own key is healthy.
func TestDeviceTokenHandler_aRevokedApproverIsNotTheClaimersRevocation(t *testing.T) {
	f := newFlow(t)
	w := newWallet(t)
	f.member(w.address, authsvc.RoleRuntime)
	if _, err := f.svc.SetDevicePolicy(context.Background(), flowNamespace, authsvc.DevicePolicyApproval, "0xowner"); err != nil {
		t.Fatalf("policy: %v", err)
	}
	phone, laptop := newTestDeviceKey(t), newTestDeviceKey(t)
	_, first := f.signIn(w, phone)
	code, pending := f.signIn(w, laptop)
	if code != http.StatusAccepted {
		t.Fatalf("second device: %d %v", code, pending)
	}
	userCode, deviceCode := pending["user_code"].(string), pending["device_code"].(string)
	if code, body := f.do(f.h.DeviceByIDHandler, http.MethodPost, "/v1/auth/devices/approve",
		map[string]any{"user_code": userCode, "device_proof": proof(phone, authsvc.DeviceProofApprove, userCode)},
		f.claimsOf(first)); code != http.StatusOK {
		t.Fatalf("approve: %d %v", code, body)
	}
	operator := &authsvc.JWTClaims{Sub: "0xowner", Namespace: flowNamespace}
	if code, body := f.do(f.h.NamespaceDeviceByIDHandler, http.MethodDelete, "/v1/namespace/devices/"+phone.id, nil, operator); code != http.StatusOK {
		t.Fatalf("revoke the approver: %d %v", code, body)
	}
	code, body := f.do(f.h.DeviceTokenHandler, http.MethodPost, "/v1/auth/device/token",
		map[string]any{"device_code": deviceCode, "device_proof": proof(laptop, authsvc.DeviceProofClaim, deviceCode)}, nil)
	if code != http.StatusBadRequest || body["error"] != authsvc.ErrDeviceAccessDenied.Error() || body["code"] == ErrCodeDeviceRevoked {
		t.Errorf("collecting a link a revoked device approved: %d %v, want 400 access_denied", code, body)
	}
}
