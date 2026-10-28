package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/Orama-Network/orama/core/pkg/client"
)

// The devices an account's sessions are bound to.
//
// A device is enrolled the first time it signs in, by the signature of its own
// key beside the wallet's (or, linked from another device, by that device's
// approval). Its row is never deleted: revoking a device tombstones it, so the
// key can never hold a session again — not by signing in afresh, and not by
// anyone who copied its public key.

// DeviceState is where a device is in its life.
type DeviceState string

const (
	// DeviceStatePending is a device a wallet signature enrolled in a namespace
	// that requires its approval. Under the approval policy it holds no
	// session; under the function policy it holds a pending device's
	// restricted one (AudiencePendingDevice).
	DeviceStatePending DeviceState = "pending"
	// DeviceStateActive may hold sessions.
	DeviceStateActive DeviceState = "active"
	// DeviceStateRevoked holds nothing, and never will again.
	DeviceStateRevoked DeviceState = "revoked"
)

// maxDeviceLabelLength bounds what a user may call a device.
const maxDeviceLabelLength = 64

var (
	// ErrDeviceNotFound is a device this account does not have.
	ErrDeviceNotFound = errors.New("no such device on this account")
	// ErrDeviceRevoked is a device that was revoked. Its key is done.
	ErrDeviceRevoked = errors.New("this device was revoked; it cannot sign in or hold a session again")
	// ErrDevicePending is a device still waiting to be approved: from another
	// of the account's devices under the approval policy, by the namespace's
	// own functions under the function policy.
	ErrDevicePending = errors.New("this device is waiting to be approved")
	// ErrDevicePendingApproval is a pending device's restricted session asking
	// for something it does not reach: anything but its own session, its own
	// attestation and the functions open to pending devices.
	ErrDevicePendingApproval = errors.New("this session's device is pending approval: until the namespace approves it, " +
		"it reaches only its own session and the functions the namespace opened to pending devices")
	// ErrDeviceBelongsToAnother is a device key already enrolled for a
	// different account or namespace. A key is one installation's, and an
	// installation is one account's.
	ErrDeviceBelongsToAnother = errors.New("this device key is enrolled for another account")
)

// SessionDevice is one enrolled device, described without its key.
type SessionDevice struct {
	ID          string
	Subject     string
	Label       string
	State       DeviceState
	ApprovedBy  string
	PublicKey   string
	CreatedAt   time.Time
	ActivatedAt time.Time
	RevokedAt   time.Time
}

// Key parses the device's stored public key.
func (d *SessionDevice) Key() (*DeviceKey, error) {
	key, err := ParseDeviceKey([]byte(d.PublicKey))
	if err != nil {
		return nil, fmt.Errorf("device %s has an unreadable stored key: %w", d.ID, err)
	}
	return key, nil
}

const sessionDeviceColumns = `id, subject, label, state, COALESCE(approved_by_device, ''), public_key,
	created_at, activated_at, revoked_at`

func sessionDeviceFromRow(row []interface{}) SessionDevice {
	created, _, _ := parseTimestamp(row[6])
	activated, _, _ := parseTimestamp(row[7])
	revoked, _, _ := parseTimestamp(row[8])
	return SessionDevice{
		ID:          getStringVal(row[0]),
		Subject:     getStringVal(row[1]),
		Label:       getStringVal(row[2]),
		State:       DeviceState(getStringVal(row[3])),
		ApprovedBy:  getStringVal(row[4]),
		PublicKey:   getStringVal(row[5]),
		CreatedAt:   created,
		ActivatedAt: activated,
		RevokedAt:   revoked,
	}
}

// Device returns one of a namespace's devices, whatever its state.
func (s *Service) Device(ctx context.Context, namespace, id string) (*SessionDevice, error) {
	db, nsID, err := s.deviceRegistry(ctx, namespace)
	if err != nil {
		return nil, err
	}
	res, err := db.Query(client.WithInternalAuth(ctx),
		`SELECT `+sessionDeviceColumns+` FROM session_devices WHERE id = ? AND namespace_id = ? LIMIT 1`,
		id, nsID)
	if err != nil {
		return nil, fmt.Errorf("read device %s: %w", id, err)
	}
	if res == nil || len(res.Rows) == 0 || len(res.Rows[0]) < 9 {
		return nil, ErrDeviceNotFound
	}
	d := sessionDeviceFromRow(res.Rows[0])
	return &d, nil
}

// ListDevices returns an account's devices, revoked ones included: a device
// the user revoked is one they want to see stayed revoked.
func (s *Service) ListDevices(ctx context.Context, namespace, subject string) ([]SessionDevice, error) {
	db, nsID, err := s.deviceRegistry(ctx, namespace)
	if err != nil {
		return nil, err
	}
	res, err := db.Query(client.WithInternalAuth(ctx),
		`SELECT `+sessionDeviceColumns+` FROM session_devices
		  WHERE namespace_id = ? AND subject = ? ORDER BY created_at DESC`,
		nsID, subject)
	if err != nil {
		return nil, fmt.Errorf("read the devices: %w", err)
	}
	if res == nil {
		return nil, nil
	}
	out := make([]SessionDevice, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) >= 9 {
			out = append(out, sessionDeviceFromRow(row))
		}
	}
	return out, nil
}

// EnrolDevice records a device for an account, or finds it already there.
//
// state is what a new device starts as. A device already enrolled keeps its
// state, except that a pending one asked to be active is activated — which is
// what an approval does. A device enrolled for anybody else, or revoked, is
// refused.
func (s *Service) EnrolDevice(ctx context.Context, namespace, subject string, key *DeviceKey, label string, state DeviceState, approvedBy string) (*SessionDevice, error) {
	if s.db == nil {
		return nil, ErrRotationNotConfigured
	}
	_, nsID, err := s.deviceRegistry(ctx, namespace)
	if err != nil {
		return nil, err
	}
	activated := any(nil)
	if state == DeviceStateActive {
		activated = time.Now().UTC().Format(sqliteTime)
	}
	res, err := s.db.Exec(client.WithInternalAuth(ctx),
		`INSERT INTO session_devices(id, namespace_id, subject, public_key, label, state, approved_by_device, activated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`,
		key.ID(), nsID, subject, key.JWK(), cleanDeviceLabel(label), string(state), nullable(approvedBy), activated,
	)
	if err != nil {
		return nil, fmt.Errorf("enrol device %s: %w", key.ID(), err)
	}
	// The write is acknowledged by the leader. Reading the new row back goes
	// through the local node (level=none), and a follower applies the log
	// after that acknowledgement. A miss there is not "this key is someone
	// else's": the row this statement just stored is this account's device.
	stored, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("enrol device %s: rows affected: %w", key.ID(), err)
	}
	if stored == 1 {
		now := time.Now().UTC()
		enrolled := &SessionDevice{
			ID:         key.ID(),
			Subject:    subject,
			Label:      cleanDeviceLabel(label),
			State:      state,
			ApprovedBy: approvedBy,
			PublicKey:  key.JWK(),
			CreatedAt:  now,
		}
		if state == DeviceStateActive {
			enrolled.ActivatedAt = now
		}
		return enrolled, nil
	}

	d, err := s.Device(ctx, namespace, key.ID())
	if errors.Is(err, ErrDeviceNotFound) {
		return nil, ErrDeviceBelongsToAnother
	}
	if err != nil {
		return nil, err
	}
	return s.settleEnrolment(ctx, subject, d, state, approvedBy)
}

// settleEnrolment decides what an enrolment found already there means.
func (s *Service) settleEnrolment(ctx context.Context, subject string, d *SessionDevice, want DeviceState, approvedBy string) (*SessionDevice, error) {
	switch {
	case d.Subject != subject:
		return nil, ErrDeviceBelongsToAnother
	case d.State == DeviceStateRevoked:
		return nil, ErrDeviceRevoked
	case d.State == DeviceStatePending && want == DeviceStateActive:
		if err := s.activateDevice(ctx, d.ID, approvedBy); err != nil {
			return nil, err
		}
		// The leader has acknowledged the activation, and reading the row
		// back goes through the local node (level=none), which can still
		// hold the pending row: a stale read there answered an approved
		// device as still pending (stagenet, 2026-09-30).
		activated := *d
		activated.State = DeviceStateActive
		activated.ActivatedAt = time.Now().UTC()
		if approvedBy != "" {
			activated.ApprovedBy = approvedBy
		}
		return &activated, nil
	}
	return d, nil
}

// activateDevice is the compare-and-swap that turns a pending device active.
func (s *Service) activateDevice(ctx context.Context, id, approvedBy string) error {
	res, err := s.db.Exec(client.WithInternalAuth(ctx),
		`UPDATE session_devices
		    SET state = 'active', activated_at = datetime('now'),
		        approved_by_device = COALESCE(?, approved_by_device)
		  WHERE id = ? AND state = 'pending'`,
		nullable(approvedBy), id)
	if err != nil {
		return fmt.Errorf("activate device %s: %w", id, err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("device %s was not pending when it was approved; it may have been revoked", id)
	}
	return nil
}

// HasActiveDevice reports whether an account already has a device that could
// approve another.
func (s *Service) HasActiveDevice(ctx context.Context, namespace, subject string) (bool, error) {
	db, nsID, err := s.deviceRegistry(ctx, namespace)
	if err != nil {
		return false, err
	}
	res, err := db.Query(client.WithInternalAuth(ctx),
		`SELECT 1 FROM session_devices WHERE namespace_id = ? AND subject = ? AND state = 'active' LIMIT 1`,
		nsID, subject)
	if err != nil {
		return false, fmt.Errorf("read the account's devices: %w", err)
	}
	return res != nil && len(res.Rows) > 0, nil
}

// RequireActiveDevice returns the device if it is active and the account's.
func (s *Service) RequireActiveDevice(ctx context.Context, namespace, subject, id string) (*SessionDevice, error) {
	d, err := s.Device(ctx, namespace, id)
	if err != nil {
		return nil, err
	}
	switch {
	case d.Subject != subject:
		return nil, ErrDeviceNotFound
	case d.State == DeviceStateRevoked:
		return nil, ErrDeviceRevoked
	case d.State == DeviceStatePending:
		return nil, ErrDevicePending
	}
	return d, nil
}

// deviceRegistry is the registry and the namespace's id in it.
func (s *Service) deviceRegistry(ctx context.Context, namespace string) (client.DatabaseClient, interface{}, error) {
	db, err := s.deviceDB()
	if err != nil {
		return nil, nil, err
	}
	namespace = sessionNamespace(namespace)
	nsID, err := s.lookupNamespaceID(ctx, namespace)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve the namespace %q: %w", namespace, err)
	}
	if nsID == nil {
		return nil, nil, fmt.Errorf("no namespace %q", namespace)
	}
	return db, nsID, nil
}

// sessionNamespace is the namespace a session call names, read the way
// ResolveNamespaceID reads it: none is the lobby. The CLI refreshes with
// whatever namespace it stored, which may be none.
func sessionNamespace(namespace string) string {
	if ns := strings.TrimSpace(namespace); ns != "" {
		return ns
	}
	return LobbyNamespace
}

// cleanDeviceLabel keeps what a user called a device printable and short.
func cleanDeviceLabel(label string) string {
	cleaned := strings.Map(func(r rune) rune {
		// Format characters include the bidi overrides and zero-width
		// joiners that make one label read as another in a device list.
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, strings.TrimSpace(label))
	if runes := []rune(cleaned); len(runes) > maxDeviceLabelLength {
		cleaned = string(runes[:maxDeviceLabelLength])
	}
	return cleaned
}
