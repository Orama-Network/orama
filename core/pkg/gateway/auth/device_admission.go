package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Orama-Network/orama/core/pkg/client"
)

// A namespace that admits its users' devices itself (DevicePolicyFunction).
//
// An application with its own admission protocol — a new phone sends a signed
// join request, an existing device answers it and both show a code to compare,
// or an account that lost every device passes a recovery check — needs the new
// device to hold a session while that runs, and needs the gateway to keep the
// device at arm's length until the application says yes. Under the function
// policy a sign-in from a device beyond the account's first enrols it pending
// and hands it a restricted session (AudiencePendingDevice), and one of the
// namespace's functions decides: approve_device makes it active, so its next
// refresh is a full session; deny_device revokes it, which ends the restricted
// session with every socket it holds.
//
// The decision is the namespace's code's, not the device's: a pending session
// reaches only the functions the namespace opened to pending devices, and the
// host calls act through the gateway's registry, never through the caller's
// token. A pending device therefore approves itself only if a function the
// namespace wrote decides that it may — which is what a recovery check is.

var (
	// ErrDeviceNotPending is a deny_device of a device that is not waiting for
	// approval. An active device is revoked by its account or an operator.
	ErrDeviceNotPending = errors.New("this device is not pending approval")
	// ErrDeviceAdmissionNotFunction is approve_device or deny_device in a
	// namespace whose device policy is not function: its devices are admitted
	// by its own rule, not by a function.
	ErrDeviceAdmissionNotFunction = errors.New("the namespace's device policy is not function: its functions do not admit devices")
)

// EnrolFirstOrPendingDevice enrols a device under the function policy: active
// when it is the first device the account has ever enrolled in the namespace,
// pending otherwise.
//
// "First" counts every device the account ever had, revoked ones included. An
// account whose devices were all revoked — lost, or signed out of for good —
// is recovering, and recovery is the application's to check: the device that
// signs in next waits for a function's approval like any other, so a stolen
// seed phrase gets no further after the owner's last device is gone than
// before. Under the approval policy the same account's next device is its
// first again, because nothing but an operator could approve it otherwise.
//
// Which it is is decided in the statement that inserts the row, so two first
// sign-ins racing each other cannot both be first.
func (s *Service) EnrolFirstOrPendingDevice(ctx context.Context, namespace, subject string, key *DeviceKey, label string) (*SessionDevice, error) {
	if s.db == nil {
		return nil, ErrRotationNotConfigured
	}
	_, nsID, err := s.deviceRegistry(ctx, namespace)
	if err != nil {
		return nil, err
	}
	res, err := s.db.Exec(client.WithInternalAuth(ctx),
		`INSERT INTO session_devices(id, namespace_id, subject, public_key, label, state, activated_at)
		 SELECT ?, ?, ?, ?, ?, 'active', datetime('now')
		  WHERE NOT EXISTS (SELECT 1 FROM session_devices WHERE namespace_id = ? AND subject = ?)
		 ON CONFLICT(id) DO NOTHING`,
		key.ID(), nsID, subject, key.JWK(), cleanDeviceLabel(label), nsID, subject)
	if err != nil {
		return nil, fmt.Errorf("enrol device %s: %w", key.ID(), err)
	}
	first, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("enrol device %s: rows affected: %w", key.ID(), err)
	}
	if first == 1 {
		// Not read back: a follower may not have applied the write yet (see
		// EnrolDevice).
		return &SessionDevice{
			ID:        key.ID(),
			Subject:   subject,
			Label:     cleanDeviceLabel(label),
			State:     DeviceStateActive,
			PublicKey: key.JWK(),
		}, nil
	}
	// The account has a device already, or this key is enrolled (this
	// account's, or another's): EnrolDevice settles which.
	return s.EnrolDevice(ctx, namespace, subject, key, label, DeviceStatePending, "")
}

// RequireSessionDevice returns the device if it is the account's and may hold
// a session of some kind: active, or pending. Whether a pending one may is the
// caller's to decide by the namespace's policy.
func (s *Service) RequireSessionDevice(ctx context.Context, namespace, subject, id string) (*SessionDevice, error) {
	d, err := s.Device(ctx, namespace, id)
	if err != nil {
		return nil, err
	}
	switch {
	case d.Subject != subject:
		return nil, ErrDeviceNotFound
	case d.State == DeviceStateRevoked:
		return nil, revokedError(d)
	}
	return d, nil
}

// ApprovePendingDevice makes one of an account's pending devices active, for
// one of the namespace's functions (approve_device). approver names who
// decided, for the audit record ("function:<name>").
//
// subject is normalised as every row keyed by a wallet is (NormalizeWallet),
// so a function may name the account by the address its own records hold.
//
// It is a compare-and-swap on the pending state, so it cannot revive a device
// revoked in between, and approving a device already active changes nothing
// and succeeds: a function that retries after a lost reply must not be told
// it failed. The device's pending access tokens stay pending until they
// expire; its next refresh is a full session.
func (s *Service) ApprovePendingDevice(ctx context.Context, namespace, subject, id, approver string) error {
	subject = NormalizeWallet(subject)
	nsID, err := s.functionAdmission(ctx, namespace)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(client.WithInternalAuth(ctx),
		`UPDATE session_devices SET state = 'active', activated_at = datetime('now')
		  WHERE id = ? AND namespace_id = ? AND subject = ? AND state = 'pending'`,
		id, nsID, subject)
	if err != nil {
		return fmt.Errorf("approve device %s: %w", id, err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		d, err := s.RequireSessionDevice(ctx, namespace, subject, id)
		if err != nil {
			return err
		}
		if d.State != DeviceStateActive {
			return fmt.Errorf("approve device %s: it is %s", id, d.State)
		}
		return nil
	}
	s.recordAdmission(ctx, namespace, subject, id, approver, AuditDeviceLoginApproved)
	return nil
}

// DenyPendingDevice revokes one of an account's pending devices, for one of
// the namespace's functions (deny_device): the device is tombstoned, its
// restricted session ends and its access tokens and sockets stop on every
// gateway within the revocation list's staleness. It refuses a device that is
// not pending — an active device is its account's to revoke, or an operator's.
func (s *Service) DenyPendingDevice(ctx context.Context, namespace, subject, id, approver string) error {
	subject = NormalizeWallet(subject)
	nsID, err := s.functionAdmission(ctx, namespace)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(client.WithInternalAuth(ctx),
		`UPDATE session_devices SET state = 'revoked', revoked_at = datetime('now'), revoked_by = ?
		  WHERE id = ? AND namespace_id = ? AND subject = ? AND state = 'pending'`,
		string(RevokedByFunction), id, nsID, subject)
	if err != nil {
		return fmt.Errorf("deny device %s: %w", id, err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		d, err := s.Device(ctx, namespace, id)
		switch {
		case err != nil:
			return err
		case d.Subject != subject:
			return ErrDeviceNotFound
		case d.State == DeviceStateRevoked:
			// Denied before, or revoked: finish ending its sessions, which a
			// denial that failed half way may have left.
			return s.endDeviceSessions(ctx, id)
		default:
			return ErrDeviceNotPending
		}
	}
	if err := s.endDeviceSessions(ctx, id); err != nil {
		return err
	}
	s.recordAdmission(ctx, namespace, subject, id, approver, AuditDeviceLoginDenied)
	return nil
}

// functionAdmission is the namespace's id in the registry when its functions
// admit devices. The policy is read from the registry, not the cache: an
// approval issues a credential's worth of authority.
func (s *Service) functionAdmission(ctx context.Context, namespace string) (interface{}, error) {
	if s.db == nil {
		return nil, ErrRotationNotConfigured
	}
	if IsLobbyNamespace(sessionNamespace(namespace)) {
		return nil, ErrDeviceAdmissionNotFunction
	}
	policy, err := s.DevicePolicyOf(ctx, namespace)
	if err != nil {
		return nil, err
	}
	if policy != DevicePolicyFunction {
		return nil, ErrDeviceAdmissionNotFunction
	}
	_, nsID, err := s.deviceRegistry(ctx, namespace)
	return nsID, err
}

// recordAdmission writes a function's decision on a device to the audit record.
func (s *Service) recordAdmission(ctx context.Context, namespace, subject, id, approver, action string) {
	s.Audit().Record(ctx, AuditEvent{
		Namespace: namespace,
		Actor:     strings.TrimSpace(approver),
		Action:    action,
		Result:    AuditSuccess,
		Resource:  id,
		Metadata:  map[string]string{"subject": subject, "by": "function"},
	})
}
