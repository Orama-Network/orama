package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Orama-Network/orama/core/pkg/client"
)

// Revoking one device ends that device's access and nothing else.
//
// Three things make it so, and each covers what the one before cannot reach:
//
//   - the device's row is tombstoned, so its key can never sign in, refresh or
//     approve again;
//   - its refresh tokens are revoked, including the reuse-grace slot a
//     just-rotated one would otherwise still have;
//   - its id goes on the revocation list, which every gateway applies to every
//     request and, through the socket sweeper, to every open WebSocket — so an
//     access token already minted for it, and a socket opened with one, stop
//     within the list's staleness rather than when they expire.
//
// The account's other devices are untouched: none of them carries this id.

// RevokeDevice revokes one of an account's devices.
//
// It is safe to repeat. A device already revoked still has its sessions and
// its revocation-list entry re-asserted, so a revocation that failed half way
// is finished by trying again rather than refused as already done.
func (s *Service) RevokeDevice(ctx context.Context, namespace, subject, id string) error {
	if s.db == nil {
		return ErrRotationNotConfigured
	}
	d, err := s.Device(ctx, namespace, id)
	if err != nil {
		return err
	}
	if d.Subject != subject {
		return ErrDeviceNotFound
	}
	if _, err := s.db.Exec(client.WithInternalAuth(ctx),
		`UPDATE session_devices SET state = 'revoked', revoked_at = datetime('now')
		  WHERE id = ? AND state != 'revoked'`, id); err != nil {
		return fmt.Errorf("tombstone device %s: %w", id, err)
	}
	return s.endDeviceSessions(ctx, id)
}

// endDeviceSessions revokes a tombstoned device's refresh tokens, the
// reuse-grace slot of a just-rotated one included, and puts its id on the
// revocation list, so its access tokens and sockets stop on every gateway
// within the list's staleness.
func (s *Service) endDeviceSessions(ctx context.Context, id string) error {
	if _, err := s.db.Exec(client.WithInternalAuth(ctx),
		`UPDATE refresh_tokens
		    SET revoked_at = COALESCE(revoked_at, datetime('now')), grace_used_at = datetime('now')
		  WHERE device_id = ? AND grace_used_at IS NULL`, id); err != nil {
		return fmt.Errorf("end the sessions of device %s: %w", id, err)
	}
	if err := s.revocations.RevokeDevice(ctx, id); err != nil {
		return fmt.Errorf("refuse the access tokens of device %s: %w", id, err)
	}
	return nil
}

// RevokedDevices reports which of ids are revoked devices.
//
// It is what the push dispatcher asks before it sends: a registration made from
// a device that has since been revoked must not be woken, whether or not its
// row has been cleaned up yet.
//
// An id that is not one of the namespace's devices is reported revoked: a
// registration naming one was not made from a device this namespace issued a
// session to, and the ids come from the tenant's own database.
func (s *Service) RevokedDevices(ctx context.Context, namespace string, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	db, nsID, err := s.deviceRegistry(ctx, namespace)
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(ids); start += revokedDevicesBatch {
		batch := ids[start:min(start+revokedDevicesBatch, len(ids))]
		live, err := liveDevices(ctx, db, nsID, batch)
		if err != nil {
			return nil, err
		}
		for _, id := range batch {
			if !live[id] {
				out[id] = true
			}
		}
	}
	return out, nil
}

// revokedDevicesBatch keeps one lookup's placeholders well inside SQLite's
// variable limit.
const revokedDevicesBatch = 200

// liveDevices reports which of ids are the namespace's devices that are not
// revoked.
func liveDevices(ctx context.Context, db client.DatabaseClient, nsID interface{}, ids []string) (map[string]bool, error) {
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, nsID)
	for _, id := range ids {
		args = append(args, id)
	}
	res, err := db.Query(client.WithInternalAuth(ctx),
		`SELECT id FROM session_devices WHERE namespace_id = ? AND state != 'revoked' AND id IN (?`+
			strings.Repeat(", ?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("read which devices are revoked: %w", err)
	}
	live := map[string]bool{}
	if res != nil {
		for _, row := range res.Rows {
			if len(row) > 0 {
				live[getStringVal(row[0])] = true
			}
		}
	}
	return live, nil
}

// IsDeviceError reports whether err is one of the device refusals, which a
// handler answers with the device's own code rather than a generic one.
func IsDeviceError(err error) bool {
	for _, target := range []error{
		ErrDeviceNotFound, ErrDeviceRevoked, ErrDevicePending, ErrDevicePendingApproval, ErrDeviceBelongsToAnother,
		ErrDeviceKeyInvalid, ErrDeviceSignatureInvalid, ErrDeviceProofRequired, ErrDeviceProofInvalid, ErrDeviceProofClockSkew,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
