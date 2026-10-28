package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/hashicorp/golang-lru/v2/expirable"
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
	// RevokedByFunction is one of the namespace's functions (deny_device,
	// deny_device_with_evidence).
	RevokedByFunction RevokedBy = "function"
	// RevokedByUnrecorded is a tombstone with no record of who revoked it:
	// a device revoked before the registry kept one (migration 100).
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

// DeviceTokenRevokedError is ErrTokenRevoked for a token refused because the
// device it is bound to was revoked. errors.Is(err, ErrTokenRevoked) holds
// for it, so a refusal that does not tell the two apart still refuses it.
// Claims are the refused token's, verified but for the revocation: they name
// the device whose revocation the refusal shows (Service.DeviceRevocationRecord).
type DeviceTokenRevokedError struct {
	Claims *JWTClaims
}

func (e *DeviceTokenRevokedError) Error() string {
	return "token revoked: the device it is bound to was revoked"
}

func (e *DeviceTokenRevokedError) Unwrap() error { return ErrTokenRevoked }

// ErrDeviceNotRevoked is a device the revocation list refuses whose registry
// row is not there: no such device, or no such namespace any more. The
// registry holds no revocation to show, so the token is refused as revoked
// (AUTH_REVOKED), never as a revoked device.
var ErrDeviceNotRevoked = errors.New("the registry holds no revoked device of this id")

// ErrDeviceRevocationUnconfirmed is a device the revocation list refuses whose
// registry row says it is not revoked. The list entry is written only after
// the tombstone commits, and a tombstone is never undone, so the two disagree
// only while this node's read of the registry lags the write: retryable.
var ErrDeviceRevocationUnconfirmed = errors.New("the revocation list and the registry disagree on this device")

// DeviceRevocationRecord is the revocation of a device whose token the
// revocation list refused, read from its tombstone. It is read only for a
// request already refused for its device, never on a healthy one.
//
// A device is told DEVICE_REVOKED only when its row says revoked: a row that
// is not there is ErrDeviceNotRevoked, a row that is not revoked
// ErrDeviceRevocationUnconfirmed, and a registry that cannot be read any
// other error, for the caller to answer as retryable.
//
// A tombstone's record never changes (the first revocation's is kept), so a
// record read is remembered (revocationRecordCache): a revoked device retrying
// its token costs one read, not one per request.
func (s *Service) DeviceRevocationRecord(ctx context.Context, namespace, id string) (*DeviceRevocationRecord, error) {
	if rec, ok := s.revocationRecords.get(namespace, id); ok {
		return rec, nil
	}
	d, err := s.Device(ctx, namespace, id)
	switch {
	case errors.Is(err, ErrDeviceNotFound), errors.Is(err, ErrNoNamespace):
		return nil, fmt.Errorf("device %s of %q: %w", id, namespace, ErrDeviceNotRevoked)
	case err != nil:
		return nil, fmt.Errorf("read the revocation of device %s: %w", id, err)
	case d.State != DeviceStateRevoked:
		return nil, fmt.Errorf("device %s is %s: %w", id, d.State, ErrDeviceRevocationUnconfirmed)
	}
	s.revocationRecords.add(namespace, id, d.Revocation)
	return d.Revocation, nil
}

// DeviceRevoked reports whether a token the revocation list refused was
// refused because its device is revoked, as the registry confirms: what the
// socket sweeper closes a socket with CloseDeviceRevoked for. A device the
// registry does not confirm, or cannot be read for, is not: its socket is
// closed as revoked (4403), and the client's next sign-in tells it the rest.
func (s *Service) DeviceRevoked(claims *JWTClaims) bool {
	if claims == nil || !s.revocations.DeniesDevice(claims.Namespace, claims.Did) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), deviceRevocationReadTimeout)
	defer cancel()
	_, err := s.DeviceRevocationRecord(ctx, claims.Namespace, claims.Did)
	return err == nil
}

// deviceRevocationReadTimeout bounds the registry read the socket sweeper
// makes to confirm a device's revocation, so one slow read cannot hold a
// sweep: a close-frame write is given a second (wssession), and this the same.
const deviceRevocationReadTimeout = time.Second

// revocationRecordCacheSize bounds the device revocations a gateway
// remembers. A record is at most the evidence bound and a proof, about
// 17 KiB, so the cache holds at most about 17 MiB; past it the least recently
// asked are read again.
const revocationRecordCacheSize = 1024

// revocationRecordCache remembers device revocations read from the registry.
// Only a confirmed revocation is kept, and for as long as the list entry that
// refuses the device's tokens lives (MaxDeviceIssuedLifetime): it never
// changes. A device not found, not revoked, or a read that failed is not kept,
// so it is asked again on the next refusal. The zero value (nil) remembers
// nothing.
type revocationRecordCache struct {
	lru *expirable.LRU[string, *DeviceRevocationRecord]
}

func newRevocationRecordCache() *revocationRecordCache {
	return &revocationRecordCache{
		lru: expirable.NewLRU[string, *DeviceRevocationRecord](revocationRecordCacheSize, nil, MaxDeviceIssuedLifetime),
	}
}

func (c *revocationRecordCache) get(namespace, id string) (*DeviceRevocationRecord, bool) {
	if c == nil {
		return nil, false
	}
	return c.lru.Get(deviceRevocationKey(namespace, id))
}

func (c *revocationRecordCache) add(namespace, id string, rec *DeviceRevocationRecord) {
	if c == nil || rec == nil {
		return
	}
	c.lru.Add(deviceRevocationKey(namespace, id), rec)
}

// revokedError is the refusal of a revoked device d.
func revokedError(d *SessionDevice) error {
	return &DeviceRevokedError{Revocation: d.Revocation}
}
