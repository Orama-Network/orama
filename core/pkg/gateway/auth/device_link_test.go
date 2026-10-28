package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth/siw"
)

// Seedless linking: a device with no wallet on it is signed in to the account by
// one of the account's devices, and the session it collects is bound to its
// own key.
func TestDeviceLink_anActiveDeviceSignsANewOneIn(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	phone, laptop := p256Device(t), ed25519Device(t)
	approver := mustEnrol(t, s, deviceOwner, phone, DeviceStateActive)

	laptopKey, _ := ParseDeviceKey([]byte(laptop.jwk))
	pending, err := s.StartDeviceLink(ctx, "anchat", "", laptopKey, "laptop")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// A wallet signature does not approve a device link.
	if err := s.ApproveDeviceAuthorization(ctx, pending.UserCode, deviceOwner, "anchat"); !errors.Is(err, ErrDeviceLinkNeedsDevice) {
		t.Fatalf("a wallet approved a device link: %v", err)
	}
	if err := s.ApproveDeviceLink(ctx, pending.UserCode, "anchat", approver); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// The device code alone does not collect it, and does not use it up.
	withoutKey := func(c *ClaimedDeviceAuthorization) error {
		key, _ := ParseDeviceKey([]byte(c.DeviceKey))
		return s.VerifyDeviceProof(ctx, key, DeviceProofClaim, c.Namespace, pending.DeviceCode, nil)
	}
	if _, err := s.ClaimDeviceAuthorization(ctx, pending.DeviceCode, withoutKey); !errors.Is(err, ErrDeviceProofRequired) {
		t.Fatalf("a device link was collected without the device: %v", err)
	}
	withKey := func(c *ClaimedDeviceAuthorization) error {
		key, _ := ParseDeviceKey([]byte(c.DeviceKey))
		return s.VerifyDeviceProof(ctx, key, DeviceProofClaim, c.Namespace, pending.DeviceCode,
			proofFor(laptop, DeviceProofClaim, c.Namespace, pending.DeviceCode))
	}
	claimed, err := s.ClaimDeviceAuthorization(ctx, pending.DeviceCode, withKey)
	if err != nil {
		t.Fatalf("the linked device could not collect its session: %v", err)
	}
	if claimed.Subject != deviceOwner || claimed.ApprovedByDevice != phone.id || claimed.DeviceLabel != "laptop" {
		t.Errorf("claimed %+v", claimed)
	}
}

// New-device approval: the wallet signature named the account, and only one of
// that account's devices may approve.
func TestApproveDeviceLink_onlyForTheAccountTheWalletNamed(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	stranger := mustEnrol(t, s, "0xb0b", p256Device(t), DeviceStateActive)
	newKey, _ := ParseDeviceKey([]byte(p256Device(t).jwk))

	pending, err := s.StartDeviceLink(ctx, "anchat", deviceOwner, newKey, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	err = s.ApproveDeviceLink(ctx, pending.UserCode, "anchat", stranger)
	if err == nil || !strings.Contains(err.Error(), "another account") {
		t.Errorf("another account's device approved this one's new device: %v", err)
	}
	if err := s.ApproveDeviceLink(ctx, pending.UserCode, "elsewhere", stranger); err == nil {
		t.Error("a device in another namespace approved the link")
	}
}

// An unkeyed login keeps working exactly as before, and a device cannot approve
// one: that is the wallet's.
func TestApproveDeviceLink_refusesAPlainLogin(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	approver := mustEnrol(t, s, deviceOwner, p256Device(t), DeviceStateActive)
	pending, err := s.StartDeviceAuthorization(ctx, "anchat")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := s.ApproveDeviceLink(ctx, pending.UserCode, "anchat", approver); err == nil {
		t.Error("a device approved a login that binds no device")
	}
}

func TestVerifyDeviceProof_refusals(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	d := p256Device(t)
	key, _ := ParseDeviceKey([]byte(d.jwk))
	stale := time.Now().Add(-2 * DeviceProofWindow).Unix()

	for name, proof := range map[string]*DeviceProof{
		"none":            nil,
		"a short id":      {IssuedAt: time.Now().Unix(), ID: "short", Signature: "x"},
		"a stale proof":   {IssuedAt: stale, ID: "abcdefghijklmnopq", Signature: d.sign(DeviceProofMessage(DeviceProofRefresh, "anchat", "t", stale, "abcdefghijklmnopq"))},
		"another action":  proofFor(d, DeviceProofApprove, "anchat", "t"),
		"another binding": proofFor(d, DeviceProofRefresh, "anchat", "u"),
		"another place":   proofFor(d, DeviceProofRefresh, "elsewhere", "t"),
	} {
		if err := s.VerifyDeviceProof(ctx, key, DeviceProofRefresh, "anchat", "t", proof); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A proof refused for its time says so, apart from one refused for its
// signature or its id, because the fix is the device's clock (bug-961). The
// time is checked before anything is spent, so the device's next proof, made
// on the gateway's clock, is accepted; a replay of that one is still refused.
func TestVerifyDeviceProof_aProofOutsideTheWindowIsAClockRefusal(t *testing.T) {
	s, _, _ := realRegistry(t)
	ctx := context.Background()
	d := p256Device(t)
	key, _ := ParseDeviceKey([]byte(d.jwk))
	const tenMinutes = 10 * time.Minute

	for name, offset := range map[string]time.Duration{"ahead": tenMinutes, "behind": -tenMinutes} {
		iat := time.Now().Add(offset).Unix()
		id := "skewed-" + name + "-proof-id"
		skewed := &DeviceProof{IssuedAt: iat, ID: id, Signature: d.sign(DeviceProofMessage(DeviceProofRefresh, "anchat", "t", iat, id))}
		err := s.VerifyDeviceProof(ctx, key, DeviceProofRefresh, "anchat", "t", skewed)
		if !errors.Is(err, ErrDeviceProofClockSkew) || errors.Is(err, ErrDeviceProofInvalid) {
			t.Errorf("a clock %s by ten minutes: %v, want only ErrDeviceProofClockSkew", name, err)
		}
	}

	fresh := proofFor(d, DeviceProofRefresh, "anchat", "t")
	if err := s.VerifyDeviceProof(ctx, key, DeviceProofRefresh, "anchat", "t", fresh); err != nil {
		t.Fatalf("the proof made on the gateway's clock: %v", err)
	}
	if err := s.VerifyDeviceProof(ctx, key, DeviceProofRefresh, "anchat", "t", fresh); !errors.Is(err, ErrDeviceProofInvalid) || errors.Is(err, ErrDeviceProofClockSkew) {
		t.Errorf("a replayed proof: %v, want ErrDeviceProofInvalid", err)
	}
}

func TestDeviceOf_readsTheDeviceTheWalletSignedFor(t *testing.T) {
	id := "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k"
	m := &siw.Message{Resources: []string{namespaceResourcePrefix + "anchat", deviceResourcePrefix + id}}
	if got, err := DeviceOf(m); err != nil || got != id {
		t.Errorf("DeviceOf = %q, %v", got, err)
	}
	if got, err := DeviceOf(&siw.Message{Resources: []string{namespaceResourcePrefix + "anchat"}}); err != nil || got != "" {
		t.Errorf("a message naming no device = %q, %v", got, err)
	}
	two := &siw.Message{Resources: []string{deviceResourcePrefix + id, deviceResourcePrefix + id}}
	if _, err := DeviceOf(two); !errors.Is(err, ErrChallengeMessage) {
		t.Errorf("a message naming two devices: %v", err)
	}
	bad := &siw.Message{Resources: []string{deviceResourcePrefix + "not-a-thumbprint"}}
	if _, err := DeviceOf(bad); !errors.Is(err, ErrChallengeMessage) {
		t.Errorf("a message naming a malformed device: %v", err)
	}
}

func TestCreateChallenge_namesTheDeviceInTheSignedBytes(t *testing.T) {
	s, _, _ := realRegistry(t)
	address, _ := ethWallet(t)
	id := p256Device(t).id
	c, err := s.CreateChallenge(context.Background(), ChallengeParams{
		Wallet: address, Namespace: "anchat", Chain: siw.Ethereum,
		Domain: "gw.example", URI: "https://gw.example", DeviceID: id,
	})
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	if !strings.Contains(c.Message, deviceResourcePrefix+id) {
		t.Errorf("the device is not in the message the wallet signs:\n%s", c.Message)
	}
	if _, err := s.CreateChallenge(context.Background(), ChallengeParams{
		Wallet: address, Namespace: "anchat", Chain: siw.Ethereum,
		Domain: "gw.example", URI: "https://gw.example", DeviceID: "nope",
	}); !errors.Is(err, ErrDeviceKeyInvalid) {
		t.Errorf("a malformed device id was put in a challenge: %v", err)
	}
}

// The TypeScript SDK signs the same bytes (sdk/tests/unit/auth/device.test.ts
// pins the same string). A drift of one character is a refresh that never
// verifies.
func TestDeviceProofMessage_isTheStatementClientsSign(t *testing.T) {
	got := string(DeviceProofMessage(DeviceProofRefresh, "anchat", "refresh-1", 1800000000, "abcdefghijklmnop"))
	if want := "orama-device-proof-v1\nrefresh\nanchat\nrefresh-1\n1800000000\nabcdefghijklmnop"; got != want {
		t.Errorf("proof statement = %q, want %q", got, want)
	}
}

func TestRevocationList_refusesARevokedDeviceOrSessionWhenever(t *testing.T) {
	s, _ := serviceWithRevocations(t)
	ctx := context.Background()
	later := time.Now().Add(time.Hour).Unix() // minted after the revocation
	if err := s.revocations.RevokeDevice(ctx, "anchat", "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k"); err != nil {
		t.Fatalf("revoke device: %v", err)
	}
	if err := s.revocations.RevokeSessionID(ctx, "session-1"); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	for name, c := range map[string]*JWTClaims{
		"the device":  {Sub: "0xa", Namespace: "anchat", Did: "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k", Iat: later},
		"the session": {Sub: "0xa", Sid: "session-1", Iat: later},
	} {
		if !mustRevoked(s, c) {
			t.Errorf("a token of %s was accepted after it was revoked", name)
		}
	}
	if mustRevoked(s, &JWTClaims{Sub: "0xa", Namespace: "anchat", Did: "another", Sid: "session-2", Iat: later}) {
		t.Error("a token of another device and session was refused")
	}
	// The entry names its namespace: the same key enrolled in another
	// namespace is another device (feat-976 review).
	if mustRevoked(s, &JWTClaims{Sub: "0xa", Namespace: "other", Did: "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k", Iat: later}) {
		t.Error("revoking a device in one namespace refused the same key in another")
	}
	if err := s.revocations.RevokeSessionID(ctx, ""); err == nil {
		t.Error("an empty session id was revoked")
	}
}

// A gateway before 0.3.13 revoked a device under "device:<id>", naming no
// namespace. An entry it wrote, during a rolling upgrade or in the seven days
// before, still refuses the device's tokens.
func TestRevocationList_honoursAnUnscopedDeviceEntry(t *testing.T) {
	s, _ := serviceWithRevocations(t)
	ctx := context.Background()
	if err := s.revocations.RevokeSubject(ctx, "device:legacy-device", "device revoked", MaxDeviceIssuedLifetime); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour).Unix()
	if !mustRevoked(s, &JWTClaims{Sub: "0xa", Namespace: "anchat", Did: "legacy-device", Iat: later}) {
		t.Error("an unscoped entry no longer refuses its device's tokens")
	}
	if !s.revocations.DeniesDevice("anchat", "legacy-device") {
		t.Error("an unscoped entry is not reported as the device's")
	}
}

// feat-976 review: during a rolling upgrade a gateway before 0.3.13 matches a
// device only on the subject "device:<id>", so a device revoked through a
// newer gateway kept working on the older ones until its tokens expired.
// RevokeDevice writes one row carrying both keys: the legacy subject, for
// them, and the scoped key as its jti, which a newer gateway reloading the
// list reads as the device's entry in its own namespace only (feat-978).
func TestRevokeDevice_writesTheLegacyEntryForOlderGateways(t *testing.T) {
	s, db := serviceWithRevocations(t)
	ctx := context.Background()
	const id = "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k"
	if err := s.revocations.RevokeDevice(ctx, "AnChat", id); err != nil {
		t.Fatal(err)
	}
	db.mu.Lock()
	rows := append([]revocation(nil), db.rows...)
	db.mu.Unlock()
	if len(rows) != 1 {
		t.Fatalf("revoking a device wrote %d rows, want one carrying both keys: %+v", len(rows), rows)
	}
	if want := strings.ToLower("device:" + id); rows[0].subject != want {
		t.Errorf("subject = %q, want the legacy %q a gateway before 0.3.13 matches on", rows[0].subject, want)
	}
	if want := strings.ToLower("device:anchat:" + id); rows[0].jti != want {
		t.Errorf("jti = %q, want the scoped %q", rows[0].jti, want)
	}

	// Another gateway of this release, reading the row from the registry.
	reloaded := NewRevocationList(registryOf(&revocationNet{db: db}), nil)
	if err := reloaded.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour).Unix()
	denies := func(namespace string) bool {
		denied, err := reloaded.Denies(&JWTClaims{Sub: "0xa", Namespace: namespace, Did: id, Iat: later}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return denied
	}
	if !denies("anchat") || !reloaded.DeniesDevice("anchat", id) {
		t.Error("a reloaded list does not refuse the revoked device in its namespace")
	}
	if denies("other") || reloaded.DeniesDevice("other", id) {
		t.Error("the legacy half of the row refuses the same key in another namespace on this release")
	}
}
