package auth

import (
	"fmt"
	"net/http"
	"time"

	authsvc "github.com/Orama-Network/orama/core/pkg/gateway/auth"
)

// A wallet sign-in that binds a device.
//
// The wallet signs a message naming the device (a `urn:orama:device:<id>`
// resource); the device signs the same message with the key whose thumbprint
// is that id. One spent nonce covers both. Neither signature alone binds
// anything: the wallet's says which device it lets in, the device's says the
// key is really there.

// signInBinding is what a verified sign-in binds.
type signInBinding struct {
	// deviceID is the device the session is bound to, or "" for a session
	// bound to the account alone.
	deviceID string
	// pendingSession is set when deviceID is pending under the function
	// policy: the sign-in yields the device's restricted session.
	pendingSession bool
	// pending is set when the device was enrolled but must be approved from
	// another of the account's devices first: the sign-in yields no session,
	// only the pending login the device waits on.
	pending *authsvc.DeviceAuthorization
}

// bindSignIn decides what a verified sign-in binds, writing any refusal itself.
func (h *Handlers) bindSignIn(w http.ResponseWriter, r *http.Request, in *signedIn, req VerifyRequest) (signInBinding, bool) {
	ctx := r.Context()
	named, err := authsvc.DeviceOf(in.Message)
	if err != nil {
		writeSignInError(w, err)
		return signInBinding{}, false
	}
	policy, err := h.authService.DevicePolicyFor(ctx, in.Namespace, in.Wallet)
	if err != nil {
		h.writeUnavailable(w, "read the namespace's session policy", err)
		return signInBinding{}, false
	}

	if named == "" && len(req.DeviceKey) == 0 {
		if policy != authsvc.DevicePolicyOptional {
			writeDeviceRefusal(w, authsvc.ErrDeviceRequired)
			return signInBinding{}, false
		}
		return signInBinding{}, true
	}

	// The lobby is where a wallet stands before it owns anything; a device
	// enrolled there would be bound to it for good, and a key is one
	// namespace's.
	if authsvc.IsLobbyNamespace(in.Namespace) {
		writeDeviceRefusal(w, fmt.Errorf("%w: the lobby binds no device; sign in to a namespace",
			authsvc.ErrDeviceKeyInvalid))
		return signInBinding{}, false
	}
	key, err := provenDeviceKey(named, req)
	if err != nil {
		writeDeviceRefusal(w, err)
		return signInBinding{}, false
	}
	return h.enrolSignInDevice(w, r, in, key, req.DeviceLabel, policy)
}

// provenDeviceKey is the device key a sign-in presents, once it is shown to be
// the one the wallet signed for and to have signed the same message.
func provenDeviceKey(named string, req VerifyRequest) (*authsvc.DeviceKey, error) {
	if named == "" || len(req.DeviceKey) == 0 {
		return nil, fmt.Errorf("%w: send device_key with a challenge that names its device_id, or neither",
			authsvc.ErrDeviceKeyInvalid)
	}
	key, err := authsvc.ParseDeviceKey(req.DeviceKey)
	if err != nil {
		return nil, err
	}
	if key.ID() != named {
		return nil, fmt.Errorf("%w: the message names device %s and the key is device %s",
			authsvc.ErrDeviceKeyInvalid, named, key.ID())
	}
	if err := key.Verify([]byte(req.Message), req.DeviceSignature); err != nil {
		return nil, err
	}
	return key, nil
}

// enrolSignInDevice records the device and says what it may have.
//
// Under the approval policy a device beyond the account's first starts
// pending, and the wallet signature buys it a user code to show on a device
// already signed in — not a session. Under the function policy a device beyond
// the first the account ever had starts pending too, and the signature buys it
// the restricted session a pending device holds until a function approves it.
func (h *Handlers) enrolSignInDevice(w http.ResponseWriter, r *http.Request, in *signedIn, key *authsvc.DeviceKey, label string, policy authsvc.DevicePolicy) (signInBinding, bool) {
	ctx := r.Context()
	if policy == authsvc.DevicePolicyFunction {
		device, err := h.authService.EnrolFirstOrPendingDevice(ctx, in.Namespace, in.Wallet, key, label)
		if err != nil {
			if !writeDeviceRefusal(w, err) {
				h.writeUnavailable(w, "enrol the device", err)
			}
			return signInBinding{}, false
		}
		return signInBinding{deviceID: device.ID, pendingSession: device.State == authsvc.DeviceStatePending}, true
	}
	state := authsvc.DeviceStateActive
	if policy == authsvc.DevicePolicyApproval {
		hasOne, err := h.authService.HasActiveDevice(ctx, in.Namespace, in.Wallet)
		if err != nil {
			h.writeUnavailable(w, "read the account's devices", err)
			return signInBinding{}, false
		}
		if hasOne {
			state = authsvc.DeviceStatePending
		}
	}

	device, err := h.authService.EnrolDevice(ctx, in.Namespace, in.Wallet, key, label, state, "")
	if err != nil {
		if !writeDeviceRefusal(w, err) {
			h.writeUnavailable(w, "enrol the device", err)
		}
		return signInBinding{}, false
	}
	if device.State == authsvc.DeviceStateActive {
		return signInBinding{deviceID: device.ID}, true
	}

	pending, err := h.authService.StartDeviceLink(ctx, in.Namespace, in.Wallet, key, label)
	if err != nil {
		if !writeDeviceRefusal(w, err) {
			h.writeUnavailable(w, "start the device approval", err)
		}
		return signInBinding{}, false
	}
	return signInBinding{pending: pending}, true
}

// requireNoDevicePolicy refuses, and writes the refusal, when the wallet's
// credential would be bound to no device in a namespace whose policy requires
// one — an API key, or a plain device login approved by a wallet.
func (h *Handlers) requireNoDevicePolicy(w http.ResponseWriter, r *http.Request, namespace, wallet string) bool {
	policy, err := h.authService.DevicePolicyFor(r.Context(), namespace, wallet)
	if err != nil {
		h.writeUnavailable(w, "read the namespace's session policy", err)
		return false
	}
	if policy != authsvc.DevicePolicyOptional {
		writeDeviceRefusal(w, authsvc.ErrDeviceRequired)
		return false
	}
	return true
}

// writePendingDevice answers a sign-in whose device waits for approval.
func writePendingDevice(w http.ResponseWriter, in *signedIn, key string, pending *authsvc.DeviceAuthorization) {
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":      "pending_approval",
		"code":        ErrCodeDevicePending,
		"hint":        "show user_code on one of the account's signed-in devices and approve it there; then poll /v1/auth/device/token with device_code and a device proof",
		"device_id":   key,
		"device_code": pending.DeviceCode,
		"user_code":   pending.UserCode,
		"expires_in":  int(time.Until(pending.ExpiresAt).Seconds()),
		"interval":    pending.Interval,
		"subject":     in.Wallet,
		"namespace":   in.Namespace,
	})
}
