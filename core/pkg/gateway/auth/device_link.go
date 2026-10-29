package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Orama-Network/orama/core/pkg/client"
)

// Linking a device from another device.
//
// The device authorization grant (RFC 8628) already splits a login in two: the
// machine that wants a session waits on a device code, and somebody somewhere
// else approves the short user code it shows. A keyed login is the same flow
// with the waiting device's public key recorded at the start, and a different
// approver: one of the account's own active devices, proving it holds its key,
// rather than a wallet signature.
//
// That one flow serves both things an application needs:
//
//   - approving a new device, in a namespace whose policy says a wallet
//     signature alone does not let a new device in — the sign-in leaves the
//     device pending and hands it a user code to show on a device already
//     signed in;
//   - linking without a seed phrase — a new device with no wallet on it starts
//     a keyed login and an existing device approves it, and the new device is
//     signed in to the account without the wallet ever being involved.
//
// Either way the session collected is bound to the waiting device's key, and
// collecting it takes that device's proof, so the codes alone collect nothing.

// ErrDeviceLinkNeedsDevice is a keyed login presented for a wallet's approval.
// Letting a wallet signature approve it would be the very thing a namespace
// requiring device approval rules out.
var ErrDeviceLinkNeedsDevice = fmt.Errorf("%w: it links a device; approve it from one of the account's signed-in devices", ErrDeviceLoginMismatch)

// ErrDeviceLinkFunctionPolicy is a device link in a namespace whose policy is
// DevicePolicyFunction. There the namespace's own functions decide which of an
// account's devices are admitted; a link approved by another device would
// enrol the new one active, or activate one still pending, past that decision.
var ErrDeviceLinkFunctionPolicy = errors.New("this namespace admits its users' devices from its own functions, not by a device link")

// DeviceLinkAllowed refuses a device link in a namespace under the function
// policy, read fresh, at every step of the link: starting it, approving it and
// collecting its session, so a link started before the policy changed admits
// nothing after.
func (s *Service) DeviceLinkAllowed(ctx context.Context, namespace string) error {
	policy, err := s.DevicePolicyOf(ctx, namespace)
	if err != nil {
		return err
	}
	if policy == DevicePolicyFunction {
		return ErrDeviceLinkFunctionPolicy
	}
	return nil
}

// StartDeviceLink records a keyed pending login. subject is the account when a
// wallet signature already named it — the approval-required sign-in — and ""
// for a device linking with no wallet, which takes its account from whoever
// approves it.
func (s *Service) StartDeviceLink(ctx context.Context, namespace, subject string, key *DeviceKey, label string) (*DeviceAuthorization, error) {
	if key == nil {
		return nil, fmt.Errorf("%w: a device link needs the device's key", ErrDeviceKeyInvalid)
	}
	if strings.TrimSpace(namespace) != "" {
		if err := s.DeviceLinkAllowed(ctx, namespace); err != nil {
			return nil, err
		}
		// Starting a link proves nothing about the caller: anyone who has a
		// device's public key can present it. A revoked key is refused, but
		// shown no revocation, whose proof and evidence are the device's to
		// see; it sees them by signing in with the key, or with its token.
		existing, err := s.Device(ctx, namespace, key.ID())
		switch {
		case err == nil && existing.State == DeviceStateRevoked:
			return nil, ErrDeviceRevoked
		case err != nil && !errors.Is(err, ErrDeviceNotFound):
			return nil, err
		}
	}
	return s.startDeviceAuthorization(ctx, namespace, deviceLink{key: key, label: label, subject: subject})
}

// ApproveDeviceLink approves a keyed pending login from one of the account's
// devices. The caller has verified approver's proof over the user code.
//
// The login must be keyed, for the approver's namespace, and — when a wallet
// signature already named its account — for the approver's account. Approving
// is a CAS, so two approvals of one code cannot both win.
func (s *Service) ApproveDeviceLink(ctx context.Context, userCode, namespace string, approver *SessionDevice) error {
	code, err := NormalizeUserCode(userCode)
	if err != nil {
		return err
	}
	pending, err := s.LookupDeviceAuthorization(ctx, code)
	if err != nil {
		return err
	}
	if pending.DeviceKey == "" {
		return fmt.Errorf("%w: it is not a device link; approve it with a wallet signature (orama auth approve)", ErrDeviceLoginMismatch)
	}
	if pending.Namespace != "" && !strings.EqualFold(pending.Namespace, namespace) {
		return fmt.Errorf("%w: this login asked for namespace %q and the approving device is in %q", ErrDeviceLoginMismatch, pending.Namespace, namespace)
	}
	if pending.Subject != "" && pending.Subject != approver.Subject {
		return fmt.Errorf("%w: it is for another account", ErrDeviceLoginMismatch)
	}
	if err := s.DeviceLinkAllowed(ctx, namespace); err != nil {
		return err
	}
	if s.db == nil {
		return ErrRotationNotConfigured
	}
	res, err := s.db.Exec(client.WithInternalAuth(ctx),
		`UPDATE device_authorizations
		    SET approved_at = datetime('now'), subject = ?, namespace = ?, approved_by_device = ?
		  WHERE user_code = ? AND device_key IS NOT NULL
		    AND (subject IS NULL OR subject = ?)
		    AND approved_at IS NULL AND denied_at IS NULL AND claimed_at IS NULL`,
		approver.Subject, strings.ToLower(namespace), approver.ID, code, approver.Subject)
	if err != nil {
		return fmt.Errorf("approve the device link: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return ErrDeviceCodeUnknown
	}
	return nil
}
