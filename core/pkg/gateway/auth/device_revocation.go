package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

// What a revoked device is told about its revocation.
//
// A client answered DEVICE_REVOKED may erase itself — its data, its session,
// its keys. On the gateway's word alone, a misbehaving gateway could erase a
// healthy device by saying so. So the tombstone keeps who revoked the device
// and what proves it, written once by the statement that tombstones the row,
// and every DEVICE_REVOKED refusal carries that record. The client checks it
// with keys it already trusts; the gateway's own word is the record's
// revoked_by, which proves nothing.
//
// Two kinds of proof can be in it:
//
//   - the revoking device's `revoke` proof (DeviceProofMessage over the revoked
//     device's id, in the namespace it signed), with that device's public key.
//     It names the revoked device, so it cannot be shown to another one, and
//     the namespace, so it cannot be carried to another namespace. It proves
//     the revocation to a client that trusts the revoking device's key: its
//     own key (a device that revoked itself), or one the application knows to
//     be the account's.
//   - evidence the revoker attached for the application, such as an
//     account-signed roster entry. It is opaque to the gateway, stored and
//     returned verbatim: the application that wrote it is the one that can
//     check it, and what it must bind (the account, the revoked device) is
//     the application's to decide.

// RevokedBy is who revoked a device.
type RevokedBy string

const (
	// RevokedByDevice is a device of the account, which signed a revoke
	// proof for the revoked device's id.
	RevokedByDevice RevokedBy = "device"
	// RevokedByAccount is a session of the account bound to no device: the
	// wallet's own, under the optional policy. Nothing signed the revocation
	// itself.
	RevokedByAccount RevokedBy = "account"
	// RevokedByOperator is a namespace operator (/v1/namespace/devices).
	RevokedByOperator RevokedBy = "operator"
	// RevokedByFunction is one of the namespace's functions (deny_device).
	RevokedByFunction RevokedBy = "function"
	// RevokedByUnrecorded is a device revoked before the gateway kept who
	// revoked it (migration 100).
	RevokedByUnrecorded RevokedBy = "unrecorded"
)

// MaxRevocationEvidenceBytes bounds what a revoker may attach to a revocation.
// It is stored with the tombstone for as long as the tombstone lives, and an
// account-signed roster entry of a few dozen devices fits well inside it.
const MaxRevocationEvidenceBytes = 16 << 10

// ErrRevocationEvidenceInvalid is evidence that is too large or not UTF-8.
var ErrRevocationEvidenceInvalid = fmt.Errorf("revocation evidence is at most %d bytes of UTF-8 text", MaxRevocationEvidenceBytes)

// RevocationProof is a revoking device's proof, as the gateway verified it:
// the statement is DeviceProofMessage(DeviceProofRevoke, Namespace, <the
// revoked device's id>, IssuedAt, ID), signed by PublicKey, whose thumbprint
// is DeviceID.
type RevocationProof struct {
	Namespace string          `json:"namespace"`
	DeviceID  string          `json:"device_id"`
	PublicKey json.RawMessage `json:"public_key"`
	IssuedAt  int64           `json:"iat"`
	ID        string          `json:"id"`
	Signature string          `json:"sig"`
}

// DeviceRevocation is how a device is being revoked, as the revoker states it.
type DeviceRevocation struct {
	By RevokedBy
	// Proof is the revoking device's verified proof, when By is device.
	Proof *RevocationProof
	// Evidence is opaque to the gateway; "" is none.
	Evidence string
}

// DeviceRevocationByDevice is a revocation by one of the account's devices:
// revoker is the device the caller's session is bound to, proof the revoke
// proof it signed in namespace, already verified.
func DeviceRevocationByDevice(namespace string, revoker *SessionDevice, proof *DeviceProof, evidence string) DeviceRevocation {
	return DeviceRevocation{
		By: RevokedByDevice,
		Proof: &RevocationProof{
			Namespace: namespace,
			DeviceID:  revoker.ID,
			PublicKey: json.RawMessage(revoker.PublicKey),
			IssuedAt:  proof.IssuedAt,
			ID:        proof.ID,
			Signature: proof.Signature,
		},
		Evidence: evidence,
	}
}

// validate refuses a revocation that is not one of the kinds, a device's
// without its proof, a proof on any other kind, or evidence past the bound.
func (r DeviceRevocation) validate() error {
	switch r.By {
	case RevokedByDevice:
		if r.Proof == nil {
			return errors.New("a revocation by a device carries the device's proof")
		}
	case RevokedByAccount, RevokedByOperator, RevokedByFunction:
		if r.Proof != nil {
			return fmt.Errorf("a revocation by %s carries no device proof", r.By)
		}
	default:
		return fmt.Errorf("unknown revoker kind %q", r.By)
	}
	return ValidRevocationEvidence(r.Evidence)
}

// ValidRevocationEvidence checks evidence against what the tombstone keeps.
func ValidRevocationEvidence(evidence string) error {
	if len(evidence) > MaxRevocationEvidenceBytes || !utf8.ValidString(evidence) {
		return ErrRevocationEvidenceInvalid
	}
	return nil
}

// stored is the revocation's columns: revoked_by, revocation_proof,
// revocation_evidence.
func (r DeviceRevocation) stored() (any, any, any, error) {
	if err := r.validate(); err != nil {
		return nil, nil, nil, err
	}
	var proof any
	if r.Proof != nil {
		raw, err := json.Marshal(r.Proof)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("encode the revocation proof: %w", err)
		}
		proof = string(raw)
	}
	return string(r.By), proof, nullable(r.Evidence), nil
}

// DeviceRevocationRecord is the revocation a DEVICE_REVOKED refusal carries.
// Proof and Evidence are null when there is none.
type DeviceRevocationRecord struct {
	DeviceID  string           `json:"device_id"`
	RevokedAt *string          `json:"revoked_at"`
	RevokedBy RevokedBy        `json:"revoked_by"`
	Proof     *RevocationProof `json:"proof"`
	Evidence  *string          `json:"evidence"`
}

// revocationRecord reads a tombstone's stored revocation. A stored proof that
// cannot be read is reported as none: the client is then told the
// revocation is unproven, which is what it is.
func revocationRecord(id string, revokedAt time.Time, by, proof, evidence string) *DeviceRevocationRecord {
	rec := &DeviceRevocationRecord{DeviceID: id, RevokedBy: RevokedBy(by)}
	if rec.RevokedBy == "" {
		rec.RevokedBy = RevokedByUnrecorded
	}
	if !revokedAt.IsZero() {
		at := revokedAt.UTC().Format(time.RFC3339)
		rec.RevokedAt = &at
	}
	if proof != "" {
		var p RevocationProof
		if json.Unmarshal([]byte(proof), &p) == nil {
			rec.Proof = &p
		}
	}
	if evidence != "" {
		rec.Evidence = &evidence
	}
	return rec
}

// DeviceRevokedError is ErrDeviceRevoked naming the device and how it was
// revoked. errors.Is(err, ErrDeviceRevoked) holds for it.
type DeviceRevokedError struct {
	Revocation *DeviceRevocationRecord
}

func (e *DeviceRevokedError) Error() string { return ErrDeviceRevoked.Error() }

func (e *DeviceRevokedError) Unwrap() error { return ErrDeviceRevoked }

// revokedError is the refusal of a revoked device d.
func revokedError(d *SessionDevice) error {
	return &DeviceRevokedError{Revocation: d.Revocation}
}
