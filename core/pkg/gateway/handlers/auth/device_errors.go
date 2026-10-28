package auth

import (
	"errors"
	"net/http"
	"time"

	authsvc "github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/logging"
	"go.uber.org/zap"
)

// The refusals of device-bound sessions. Each says what went wrong with the
// device, because the client's next move differs: sign again, ask another
// device, or give up on this key for good.
const (
	// ErrCodeDeviceRequired: the namespace requires sessions bound to a
	// device, and the sign-in bound none.
	ErrCodeDeviceRequired = "DEVICE_REQUIRED"
	// ErrCodeDeviceKeyInvalid: the device key is not a P-256 or Ed25519
	// public JWK, or the message and the key name different devices.
	ErrCodeDeviceKeyInvalid = "DEVICE_KEY_INVALID"
	// ErrCodeDeviceSignatureInvalid: the device's signature over the sign-in
	// message does not verify.
	ErrCodeDeviceSignatureInvalid = "DEVICE_SIGNATURE_INVALID"
	// ErrCodeDeviceProofRequired: a device-bound credential was presented
	// without the device's proof.
	ErrCodeDeviceProofRequired = "DEVICE_PROOF_REQUIRED"
	// ErrCodeDeviceProofInvalid: the proof is malformed, reused, or not signed
	// by the device.
	ErrCodeDeviceProofInvalid = "DEVICE_PROOF_INVALID"
	// ErrCodeDeviceProofClockSkew: the proof's time is outside the window
	// around the gateway's clock. The answer's server_time is the gateway's
	// time, in unix seconds.
	ErrCodeDeviceProofClockSkew = "DEVICE_PROOF_CLOCK_SKEW"
	// ErrCodeDeviceRevoked: the device presenting itself was revoked. Its key
	// is done. The answer's revocation is who revoked it and what proves it.
	ErrCodeDeviceRevoked = "DEVICE_REVOKED"
	// ErrCodeDevicePending: the device waits for another device's approval.
	ErrCodeDevicePending = "DEVICE_PENDING"
	// ErrCodeDevicePendingApproval: the caller's session is a pending device's
	// restricted one (the function policy), which reaches only its own session
	// and the functions open to pending devices.
	ErrCodeDevicePendingApproval = "DEVICE_PENDING_APPROVAL"
	// ErrCodeDeviceNotFound: the account has no such device.
	ErrCodeDeviceNotFound = "DEVICE_NOT_FOUND"
	// ErrCodeDeviceKeyTaken: the key is enrolled for another account.
	ErrCodeDeviceKeyTaken = "DEVICE_KEY_TAKEN"
	// ErrCodePolicySweepIncomplete: the session policy is set, but revoking
	// the end users' existing sign-in keys stopped partway. Repeat the request.
	ErrCodePolicySweepIncomplete = "POLICY_SWEEP_INCOMPLETE"
	// ErrCodeSignInClosed: a session of a wallet holding no grant was refused
	// its refresh because the namespace closed sign-in again.
	ErrCodeSignInClosed = "SIGN_IN_CLOSED"
	// ErrCodeDeviceUserCodeMalformed: the user code is not the shape this
	// gateway issues.
	ErrCodeDeviceUserCodeMalformed = "DEVICE_USER_CODE_MALFORMED"
	// ErrCodeDeviceLoginMismatch: the pending login is for another namespace
	// or account, or is the other kind of login (device link vs wallet).
	ErrCodeDeviceLoginMismatch = "DEVICE_LOGIN_MISMATCH"
	// ErrCodeDeviceLinkFunctionPolicy: a device link in a namespace whose
	// functions admit its users' devices (the function policy).
	ErrCodeDeviceLinkFunctionPolicy = "DEVICE_LINK_FUNCTION_POLICY"
)

type deviceRefusal struct {
	status int
	code   string
	hint   string
}

// deviceRefusals is checked in order; the first error it matches decides.
var deviceRefusals = []struct {
	err     error
	refusal deviceRefusal
}{
	{authsvc.ErrDeviceRequired, deviceRefusal{http.StatusForbidden, ErrCodeDeviceRequired,
		"generate a device key, ask for a challenge naming it (device_id), and send device_key and device_signature"}},
	{authsvc.ErrDeviceKeyInvalid, deviceRefusal{http.StatusBadRequest, ErrCodeDeviceKeyInvalid,
		"send the public JWK of a P-256 or Ed25519 key, and a challenge whose device_id is that key's thumbprint"}},
	{authsvc.ErrDeviceSignatureInvalid, deviceRefusal{http.StatusUnauthorized, ErrCodeDeviceSignatureInvalid,
		"sign the sign-in message, byte for byte, with the device key as well as the wallet"}},
	{authsvc.ErrDeviceProofRequired, deviceRefusal{http.StatusUnauthorized, ErrCodeDeviceProofRequired,
		"sign a device proof for this request with the device key and send it as device_proof"}},
	{authsvc.ErrDeviceProofInvalid, deviceRefusal{http.StatusUnauthorized, ErrCodeDeviceProofInvalid,
		"make a fresh proof: a new id, the current time, signed by the device key over this request's credential"}},
	{authsvc.ErrDeviceProofClockSkew, deviceRefusal{http.StatusUnauthorized, ErrCodeDeviceProofClockSkew,
		"this device's clock is off: stamp the proof's iat on the gateway's clock (server_time here, or any answer's Date header) and sign a new proof"}},
	{authsvc.ErrDeviceRevoked, deviceRefusal{http.StatusForbidden, ErrCodeDeviceRevoked,
		"this device key can never sign in again; generate a new key and link it from a device that is still signed in"}},
	{authsvc.ErrDevicePendingApproval, deviceRefusal{http.StatusForbidden, ErrCodeDevicePendingApproval,
		"finish the application's own admission of this device (its functions open to pending devices), then refresh the session"}},
	{authsvc.ErrDevicePending, deviceRefusal{http.StatusForbidden, ErrCodeDevicePending,
		"approve this device: from one of the account's signed-in devices, or, under the function policy, through the application"}},
	{authsvc.ErrDeviceNotFound, deviceRefusal{http.StatusNotFound, ErrCodeDeviceNotFound,
		"list this account's devices with GET /v1/auth/devices"}},
	{authsvc.ErrSignInClosed, deviceRefusal{http.StatusForbidden, ErrCodeSignInClosed,
		"this wallet holds no grant here (never invited, or its grant was revoked, expired or disabled) and the namespace is not open to other wallets; sign in again once it is, or ask its owner for an invitation"}},
	{authsvc.ErrDeviceLinkFunctionPolicy, deviceRefusal{http.StatusForbidden, ErrCodeDeviceLinkFunctionPolicy,
		"sign in with the wallet and this device's key; the device is pending until the application's own functions admit it"}},
	{authsvc.ErrDeviceBelongsToAnother, deviceRefusal{http.StatusForbidden, ErrCodeDeviceKeyTaken,
		"a device key belongs to one account; generate a new key for this one"}},
}

// writeUnavailable answers 503 without the database's own words, which name
// tables and hosts, and puts them in the log for the operator instead.
func (h *Handlers) writeUnavailable(w http.ResponseWriter, what string, err error) {
	if h.logger != nil {
		h.logger.ComponentWarn(logging.ComponentGeneral, "could not "+what, zap.Error(err))
	}
	writeError(w, http.StatusServiceUnavailable, "could not "+what+"; retry")
}

// writeDeviceRefusal answers a device refusal and reports whether err was one.
// Anything else is left for the caller to answer.
func writeDeviceRefusal(w http.ResponseWriter, err error) bool {
	for _, candidate := range deviceRefusals {
		if !errors.Is(err, candidate.err) {
			continue
		}
		if candidate.refusal.status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Bearer realm="gateway", charset="UTF-8"`)
		}
		body := map[string]any{
			"error": err.Error(),
			"code":  candidate.refusal.code,
			"hint":  candidate.refusal.hint,
		}
		// A proof refused for its time is fixed by the device's clock: the
		// answer says what this gateway's clock reads.
		if errors.Is(err, authsvc.ErrDeviceProofClockSkew) {
			body["server_time"] = time.Now().Unix()
		}
		// A revoked device is shown its revocation: who revoked it and what
		// proves it, for the client to check with keys it already trusts
		// before it acts on the refusal (authsvc.DeviceRevocationRecord).
		var revoked *authsvc.DeviceRevokedError
		if errors.As(err, &revoked) {
			body["revocation"] = revoked.Revocation
		}
		writeJSON(w, candidate.refusal.status, body)
		return true
	}
	return false
}
