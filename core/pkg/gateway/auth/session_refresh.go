package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// insertRefreshTokenSQL stores a refresh token: hashed, for thirty days, with
// the device it is bound to (NULL for the account alone) and the id of the
// session it belongs to.
const insertRefreshTokenSQL = `INSERT INTO refresh_tokens(
	namespace_id, subject, token, audience, expires_at, custom_claims, device_id, session_id)
	VALUES (?, ?, ?, ?, datetime('now', '+30 days'), ?, ?, ?)`

const (
	// refreshTokenBytes is the randomness in a refresh token.
	refreshTokenBytes = 32

	// deviceBoundRefreshPrefix marks a refresh token whose session is bound
	// to a device, and deviceBoundRefreshDomain is what its stored hash is
	// taken under. A gateway that predates devices hashes a presented token
	// bare, so it can never find a device-bound session's row — and so can
	// never rotate one without the device's proof, which is what it would
	// otherwise do during a rolling upgrade, quietly turning the session into
	// one bound to the account alone.
	deviceBoundRefreshPrefix = "dv1_"
	deviceBoundRefreshDomain = "orama-device-bound-refresh-v1:"
)

// mintRefreshToken draws a new refresh token, marked when its session is bound
// to a device.
func mintRefreshToken(deviceBound bool) (string, error) {
	buf := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate refresh token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	if deviceBound {
		token = deviceBoundRefreshPrefix + token
	}
	return token, nil
}

// refreshTokenHash is how a refresh token is stored and looked up.
func refreshTokenHash(token string) string {
	if strings.HasPrefix(token, deviceBoundRefreshPrefix) {
		return sha256Hex(deviceBoundRefreshDomain + token)
	}
	return sha256Hex(token)
}

// refreshRow is what a refresh token's row says about its session.
type refreshRow struct {
	subject   string
	custom    map[string]string
	deviceID  string
	sessionID string
}

// readRefreshRow reads `subject, custom_claims, device_id, session_id`.
func readRefreshRow(row []interface{}) refreshRow {
	var out refreshRow
	if len(row) > 0 {
		if v, ok := row[0].(string); ok {
			out.subject = v
		} else {
			b, _ := json.Marshal(row[0])
			_ = json.Unmarshal(b, &out.subject)
		}
	}
	if len(row) > 1 {
		cc, _ := row[1].(string)
		out.custom = unmarshalClaims(cc)
	}
	if len(row) > 3 {
		out.deviceID = getStringVal(row[2])
		out.sessionID = getStringVal(row[3])
	}
	return out
}

// checkSessionRefresh is what a refresh must show beyond the refresh token,
// and whether the session it renews is a pending device's.
//
// A session bound to a device needs the device (checkDeviceRefresh). A session
// bound to none is refused in a namespace whose policy now requires devices, so
// turning the policy on ends the account-level sessions of its end users at
// their next refresh rather than leaving them refreshing for ever. Either kind
// of session of a wallet holding no grant also ends when the namespace closes
// sign-in (checkSignInStillOpen).
func (s *Service) checkSessionRefresh(ctx context.Context, namespace string, session refreshRow, refreshToken string, proof *DeviceProof) (pending bool, err error) {
	if pending, err = s.checkSessionBinding(ctx, namespace, session, refreshToken, proof); err != nil {
		return false, err
	}
	return pending, s.checkSignInStillOpen(ctx, namespace, session.subject)
}

func (s *Service) checkSessionBinding(ctx context.Context, namespace string, session refreshRow, refreshToken string, proof *DeviceProof) (bool, error) {
	if session.deviceID != "" {
		return s.checkDeviceRefresh(ctx, namespace, session, refreshToken, proof)
	}
	policy, err := s.refreshDevicePolicyFor(ctx, namespace, session.subject)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrRefreshTransient, err)
	}
	if policy != DevicePolicyOptional {
		return false, ErrDeviceRequired
	}
	return false, nil
}

// checkDeviceRefresh is what a device-bound session's refresh must show: the
// device is still the account's and active — or pending, in a namespace whose
// policy is function, which renews the restricted session — and it signed for
// this refresh token, now. Whether the device is pending is read from the
// registry, not from the session, so the first refresh after a function
// approved the device is a full session.
//
// A registry that cannot be read is a retryable failure, not a verdict on the
// device — the same line RefreshToken draws for the token itself (bugboard
// #125).
func (s *Service) checkDeviceRefresh(ctx context.Context, namespace string, session refreshRow, refreshToken string, proof *DeviceProof) (bool, error) {
	device, err := s.RequireSessionDevice(ctx, namespace, session.subject, session.deviceID)
	if err != nil {
		if IsDeviceError(err) {
			return false, err
		}
		return false, fmt.Errorf("%w: %v", ErrRefreshTransient, err)
	}
	pending := device.State == DeviceStatePending
	if pending {
		// Only the function policy gives a pending device a session. Under
		// approval it waits for its user code, and a namespace that left the
		// function policy took the pending sessions with it.
		policy, err := s.refreshDevicePolicyFor(ctx, namespace, session.subject)
		if err != nil {
			return false, fmt.Errorf("%w: %v", ErrRefreshTransient, err)
		}
		if policy != DevicePolicyFunction {
			return false, ErrDevicePending
		}
	}
	key, err := device.Key()
	if err != nil {
		return false, err
	}
	if err := s.VerifyDeviceProof(ctx, key, DeviceProofRefresh, namespace, refreshToken, proof); err != nil {
		if errors.Is(err, ErrNonceTransient) {
			return false, fmt.Errorf("%w: %v", ErrRefreshTransient, err)
		}
		return false, err
	}
	return pending, nil
}
