package auth

import (
	"encoding/json"

	authsvc "github.com/Orama-Network/orama/core/pkg/gateway/auth"
)

// ChallengeRequest is the request body for challenge generation.
//
// ChainType decides which of the two message grammars the gateway renders —
// "Ethereum account" or "Solana account" in the header line — and therefore
// which signature scheme will verify the answer. Empty means ETH.
type ChallengeRequest struct {
	Wallet    string `json:"wallet"`
	Purpose   string `json:"purpose"`
	Namespace string `json:"namespace"`
	ChainType string `json:"chain_type"`
	// DeviceID names the device the sign-in will bind: the RFC 7638
	// thumbprint of its key. It is written into the signed message, so the
	// wallet approves which device it lets in. Optional.
	DeviceID string `json:"device_id"`
}

// VerifyRequest is the request body for signature verification.
//
// Message is the sign-in message the wallet signed, verbatim. Everything the
// gateway needs is inside it — the wallet, the nonce, the namespace, the domain
// it was signed for and when it expires — and reading any of those from beside
// it in the request body would mean acting on a field the user never saw and
// the signature does not cover.
//
// A sign-in that binds a device adds the device's public JWK and its signature
// over the same message: the wallet signed for the device the message names,
// and the device proves it holds that key.
type VerifyRequest struct {
	Message         string          `json:"message"`
	Signature       string          `json:"signature"`
	DeviceKey       json.RawMessage `json:"device_key,omitempty"`
	DeviceSignature string          `json:"device_signature,omitempty"`
	DeviceLabel     string          `json:"device_label,omitempty"`
}

// APIKeyRequest is the request body for API key generation. See VerifyRequest
// for why the message is the whole credential.
type APIKeyRequest struct {
	Message   string `json:"message"`
	Signature string `json:"signature"`
	Plan      string `json:"plan"`
}

// RefreshRequest is the request body for token refresh
//
// A session bound to a device is refreshed with the device's proof over the
// refresh token (see authsvc.DeviceProofMessage).
type RefreshRequest struct {
	RefreshToken string               `json:"refresh_token"`
	Namespace    string               `json:"namespace"`
	DeviceProof  *authsvc.DeviceProof `json:"device_proof,omitempty"`
}

// LogoutRequest is the request body for logout/token revocation
type LogoutRequest struct {
	RefreshToken string `json:"refresh_token"`
	Namespace    string `json:"namespace"`
	All          bool   `json:"all"`
	// DeviceProof is, with All from a session bound to a device, that
	// device's proof for logout-all over its own id: an access token lifted
	// off one phone must not sign the account out of every other.
	DeviceProof *authsvc.DeviceProof `json:"device_proof,omitempty"`
}
