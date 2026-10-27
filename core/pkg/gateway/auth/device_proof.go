package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Orama-Network/orama/core/pkg/client"
)

// A device proves it holds its key by signing a statement of what it is doing,
// now.
//
// The statement names the action (refresh a session, approve a link, collect a
// linked session), the namespace, the credential it is being presented with,
// when it was made, and a random id. The id is spent in the nonce table the
// sign-in challenges already use, so a proof captured in transit is good once,
// and the time bounds how long "once" can wait. It is DPoP's shape without
// DPoP's HTTP-method binding: every place a proof is taken is one endpoint.

const (
	// DeviceProofWindow is how far a proof's time may be from this gateway's.
	// It covers clock drift between a phone and the cluster, not a slow
	// network: a proof is made immediately before the request that carries it.
	DeviceProofWindow = 60 * time.Second

	// deviceProofVersion prefixes the signed statement, so a later format
	// cannot be confused with this one.
	deviceProofVersion = "orama-device-proof-v1"

	// deviceProofNoncePrefix is how a spent proof id is filed in the nonce
	// table: under the device, where a wallet's challenges are under the
	// wallet. A device id cannot collide with a wallet address.
	deviceProofNoncePrefix = "device:"
)

// The actions a device proves possession for. Each is a different statement,
// so a proof made for one cannot be presented for another.
const (
	DeviceProofRefresh    = "refresh"
	DeviceProofApprove    = "approve"
	DeviceProofClaim      = "claim"
	DeviceProofRevoke     = "revoke"
	DeviceProofEndSession = "end-session"
)

// deviceProofIDPattern is what a proof id may be: enough randomness to be
// unique, in characters that need no escaping anywhere it is stored.
var deviceProofIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

var (
	// ErrDeviceProofRequired is a device-bound credential presented without
	// the device's proof.
	ErrDeviceProofRequired = errors.New("this session is bound to a device: send device_proof signed by the device's key")

	// ErrDeviceProofInvalid is a proof that is malformed, already spent, or
	// not signed by the device's key.
	ErrDeviceProofInvalid = errors.New("the device proof is malformed, already used, or not signed by the device")

	// ErrDeviceProofClockSkew is a proof whose time is outside the window
	// around this gateway's clock. It is its own refusal because the fix is
	// the device's clock, not its key: the answer carries the gateway's time,
	// and a client that stamps its proofs on that time is accepted.
	ErrDeviceProofClockSkew = errors.New("the device proof's time is outside the window around this gateway's clock")
)

// DeviceProof is a device's signature over one action.
type DeviceProof struct {
	// IssuedAt is when the device made the proof, unix seconds.
	IssuedAt int64 `json:"iat"`
	// ID is random and single-use.
	ID string `json:"id"`
	// Signature is base64url, over DeviceProofMessage.
	Signature string `json:"sig"`
	// AppAttest is, on a refresh from an iOS device whose namespace attests,
	// an App Attest assertion (standard base64) over AttestationBinding(ID,
	// device id): the device proving again, once per refresh, that it is the
	// genuine app. The proof's own freshness and single use are the
	// assertion's.
	AppAttest string `json:"app_attest,omitempty"`
}

// DeviceProofMessage is the exact text a device signs for a proof.
//
// binding is the credential the proof travels with — the refresh token, the
// user code, the device code — so a proof cannot be moved onto another one.
func DeviceProofMessage(action, namespace, binding string, issuedAt int64, id string) []byte {
	return []byte(strings.Join([]string{
		deviceProofVersion,
		action,
		namespace,
		binding,
		strconv.FormatInt(issuedAt, 10),
		id,
	}, "\n"))
}

// VerifyDeviceProof checks a proof and spends it.
//
// The signature is checked before the id is spent, so a stranger cannot burn a
// device's proof ids by presenting garbage under them.
func (s *Service) VerifyDeviceProof(ctx context.Context, key *DeviceKey, action, namespace, binding string, p *DeviceProof) error {
	if p == nil {
		return ErrDeviceProofRequired
	}
	if !deviceProofIDPattern.MatchString(p.ID) {
		return fmt.Errorf("%w: id must be 16-128 characters of [A-Za-z0-9_-]", ErrDeviceProofInvalid)
	}
	now := time.Now()
	if skew := now.Sub(time.Unix(p.IssuedAt, 0)); skew > DeviceProofWindow || skew < -DeviceProofWindow {
		return fmt.Errorf("%w: iat is %s from this gateway's clock, beyond %s",
			ErrDeviceProofClockSkew, skew.Round(time.Second), DeviceProofWindow)
	}
	if err := key.Verify(DeviceProofMessage(action, namespace, binding, p.IssuedAt, p.ID), p.Signature); err != nil {
		return fmt.Errorf("%w: %v", ErrDeviceProofInvalid, err)
	}
	return s.spendDeviceProof(ctx, key.ID(), namespace, action, p.ID, now)
}

// spendDeviceProof records a proof id as used, once. The insert is the lock: a
// second presentation of the same id finds the row and inserts nothing.
func (s *Service) spendDeviceProof(ctx context.Context, deviceID, namespace, action, id string, now time.Time) error {
	if s.db == nil {
		return ErrNonceConsumeNotConfigured
	}
	nsID, err := s.lookupNamespaceID(ctx, s.nonceNamespace(namespace))
	if err != nil {
		return fmt.Errorf("%w: resolve namespace: %v", ErrNonceTransient, err)
	}
	if nsID == nil {
		return fmt.Errorf("%w: no namespace %q", ErrDeviceProofInvalid, namespace)
	}
	// Kept for twice the window: past it the time check refuses the proof on
	// its own, and the nonce reaper removes the row.
	expires := now.Add(2 * DeviceProofWindow).UTC().Format(sqliteTime)
	res, err := s.db.Exec(client.WithInternalAuth(ctx),
		`INSERT INTO nonces(namespace_id, wallet, nonce, purpose, expires_at, used_at)
		 VALUES (?, ?, ?, ?, ?, datetime('now'))
		 ON CONFLICT(namespace_id, wallet, nonce) DO NOTHING`,
		nsID, deviceProofNoncePrefix+deviceID, id, "device-proof:"+action, expires)
	if err != nil {
		return fmt.Errorf("%w: record the proof: %v", ErrNonceTransient, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: rows affected: %v", ErrNonceTransient, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: this proof was already used", ErrDeviceProofInvalid)
	}
	return nil
}
