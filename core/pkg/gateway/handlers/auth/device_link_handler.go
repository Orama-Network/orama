package auth

import (
	"context"
	"fmt"

	authsvc "github.com/Orama-Network/orama/core/pkg/gateway/auth"
)

// The waiting device's half of a device link (see authsvc.StartDeviceLink).

// startPendingLogin starts a pending login: a device link when the request
// carries a device key, the plain RFC 8628 login otherwise. It returns the
// device id of a link, "" for a plain login.
func (h *Handlers) startPendingLogin(ctx context.Context, req DeviceAuthorizationRequest) (*authsvc.DeviceAuthorization, string, error) {
	if len(req.DeviceKey) == 0 {
		pending, err := h.authService.StartDeviceAuthorization(ctx, req.Namespace)
		return pending, "", err
	}
	key, err := authsvc.ParseDeviceKey(req.DeviceKey)
	if err != nil {
		return nil, "", err
	}
	pending, err := h.authService.StartDeviceLink(ctx, req.Namespace, "", key, req.DeviceLabel)
	if err != nil {
		return nil, "", err
	}
	return pending, key.ID(), nil
}

// claimCheck is what an approved login must still show when it is collected,
// run before it is collected so that a failure leaves it collectable. It
// records the device the session will be bound to in *deviceID.
//
//   - A plain login is bound to no device, so it is refused if the namespace
//     now requires one of this account — the policy may have changed since
//     the wallet approved it.
//   - A device link needs the linked device's proof over this device code,
//     and the device that approved it must still be active: revoking a stolen
//     approver must reach the device it let in. The linked device becomes
//     one of the account's active devices here, unless the namespace now
//     admits its devices from its functions (DeviceLinkAllowed).
func (h *Handlers) claimCheck(ctx context.Context, client, deviceCode string, proof *authsvc.DeviceProof, deviceID, namespace *string) func(*authsvc.ClaimedDeviceAuthorization) error {
	return func(claimed *authsvc.ClaimedDeviceAuthorization) error {
		*namespace = claimed.Namespace
		// Before anything is enrolled or the code spent: a wallet the
		// namespace no longer lets in, or one past its network's new-wallet
		// ceiling, gets no device here, and keeps its code.
		if err := h.authService.AdmitSignIn(ctx, claimed.Subject, claimed.Namespace, client); err != nil {
			return err
		}
		if claimed.DeviceKey == "" {
			policy, err := h.authService.DevicePolicyFor(ctx, claimed.Namespace, claimed.Subject)
			if err != nil {
				return err
			}
			if policy != authsvc.DevicePolicyOptional {
				return authsvc.ErrDeviceRequired
			}
			return nil
		}
		if err := h.authService.DeviceLinkAllowed(ctx, claimed.Namespace); err != nil {
			return err
		}
		key, err := authsvc.ParseDeviceKey([]byte(claimed.DeviceKey))
		if err != nil {
			return fmt.Errorf("the pending device link holds an unreadable key: %w", err)
		}
		if err := h.authService.VerifyDeviceProof(ctx, key, authsvc.DeviceProofClaim, claimed.Namespace, deviceCode, proof); err != nil {
			return err
		}
		// The approver's state is no refusal of the device collecting: told
		// DEVICE_REVOKED or DEVICE_PENDING here, a healthy new device would
		// take its own key for revoked or pending. The approval is void, which
		// is RFC 8628's access_denied.
		if _, err := h.authService.RequireActiveDevice(ctx, claimed.Namespace, claimed.Subject, claimed.ApprovedByDevice); err != nil {
			if authsvc.IsDeviceError(err) {
				return authsvc.ErrDeviceAccessDenied
			}
			return fmt.Errorf("read the device that approved this link: %w", err)
		}
		device, err := h.authService.EnrolDevice(ctx, claimed.Namespace, claimed.Subject, key,
			claimed.DeviceLabel, authsvc.DeviceStateActive, claimed.ApprovedByDevice)
		if err != nil {
			return err
		}
		if device.State != authsvc.DeviceStateActive {
			return authsvc.ErrDevicePending
		}
		*deviceID = device.ID
		return nil
	}
}
