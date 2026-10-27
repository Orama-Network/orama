package gw

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Orama-Network/orama/e2e/harness/wallet"
)

// Auth routes (docs/whitepaper/technical-reference/appendices/i-api-surface.md, "Authentication").
const (
	PathChallenge = "/v1/auth/challenge"
	PathVerify    = "/v1/auth/verify"
	PathAPIKey    = "/v1/auth/api-key"
	PathToken     = "/v1/auth/token"
	PathRefresh   = "/v1/auth/refresh"
	PathLogout    = "/v1/auth/logout"
	PathWhoami    = "/v1/auth/whoami"
	PathSessions  = "/v1/auth/sessions"
	PathDevices   = "/v1/auth/devices"
)

// ChallengeRequest is POST /v1/auth/challenge. An empty Namespace is the lobby.
type ChallengeRequest struct {
	Wallet    string `json:"wallet"`
	Namespace string `json:"namespace,omitempty"`
	ChainType string `json:"chain_type,omitempty"`
	DeviceID  string `json:"device_id,omitempty"`
	Purpose   string `json:"purpose,omitempty"`
}

// ChallengeResponse is the gateway's answer; Message goes back verbatim.
type ChallengeResponse struct {
	Message   string `json:"message"`
	Nonce     string `json:"nonce"`
	Namespace string `json:"namespace"`
	ExpiresAt string `json:"expires_at"`
}

// VerifyRequest is POST /v1/auth/verify.
type VerifyRequest struct {
	Message         string          `json:"message"`
	Signature       string          `json:"signature"`
	DeviceKey       json.RawMessage `json:"device_key,omitempty"`
	DeviceSignature string          `json:"device_signature,omitempty"`
	DeviceLabel     string          `json:"device_label,omitempty"`
}

// Session is what a successful sign-in or refresh returns.
type Session struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Subject      string `json:"subject"`
	Namespace    string `json:"namespace"`
	APIKey       string `json:"api_key,omitempty"`
	DeviceID     string `json:"device_id,omitempty"`
	// Status is "provisioning" on a 202 from a namespace still being built.
	Status string `json:"status,omitempty"`
	// issuedAt is when the request that returned the session was sent, zero
	// for a session the harness did not receive itself. Wall clock only: the
	// token expires by the wall clock, which keeps running while the host
	// sleeps and the monotonic clock does not.
	issuedAt time.Time
}

// refreshAfter is how far into its lifetime a session is refreshed: early
// enough that a request sent just before cannot outlive the access token.
const refreshAfter = 2.0 / 3.0

// Stale reports whether the access token is far enough into its lifetime to be
// refreshed before use. A session with no refresh token, no lifetime or no
// recorded issue time is never stale: there is nothing to refresh it with.
func (s *Session) Stale(now time.Time) bool {
	if s == nil || s.issuedAt.IsZero() || s.ExpiresIn <= 0 || s.RefreshToken == "" {
		return false
	}
	lifetime := time.Duration(s.ExpiresIn) * time.Second
	return now.Round(0).Sub(s.issuedAt) >= time.Duration(float64(lifetime)*refreshAfter)
}

// Challenge asks for a sign-in message.
func (c *Client) Challenge(ctx context.Context, req ChallengeRequest) (*ChallengeResponse, *Response, error) {
	var out ChallengeResponse
	resp, err := c.JSON(ctx, http.MethodPost, PathChallenge, "", req, &out)
	if err != nil {
		return nil, resp, err
	}
	if out.Message == "" {
		return nil, resp, fmt.Errorf("the gateway issued an empty challenge")
	}
	return &out, resp, nil
}

// Verify exchanges a signed message for a session. A 202 (namespace still
// provisioning) is returned as a session with Status set, not as an error.
func (c *Client) Verify(ctx context.Context, req VerifyRequest) (*Session, *Response, error) {
	out := Session{issuedAt: time.Now().Round(0)}
	resp, err := c.JSON(ctx, http.MethodPost, PathVerify, "", req, &out)
	if err != nil {
		return nil, resp, err
	}
	if err := c.protect(out.AccessToken, out.RefreshToken, out.APIKey); err != nil {
		return nil, resp, err
	}
	return &out, resp, nil
}

// APIKey exchanges a signed message for the wallet's API key.
func (c *Client) APIKey(ctx context.Context, message, signature string) (string, *Response, error) {
	var out struct {
		APIKey string `json:"api_key"`
	}
	resp, err := c.JSON(ctx, http.MethodPost, PathAPIKey, "", map[string]string{"message": message, "signature": signature}, &out)
	if err != nil {
		return "", resp, err
	}
	if err := c.protect(out.APIKey); err != nil {
		return "", resp, err
	}
	return out.APIKey, resp, nil
}

// Token exchanges an API key for a short-lived JWT.
func (c *Client) Token(ctx context.Context, apiKey string) (*Session, *Response, error) {
	sent := time.Now().Round(0)
	resp, err := c.Send(ctx, Req{Method: http.MethodPost, Path: PathToken, APIKey: apiKey})
	if err != nil {
		return nil, resp, err
	}
	if resp.Status != http.StatusOK {
		return nil, resp, &StatusError{Method: http.MethodPost, Path: PathToken, Status: resp.Status, Body: string(resp.Body)}
	}
	out := Session{issuedAt: sent}
	if err := resp.Decode(&out); err != nil {
		return nil, resp, err
	}
	if err := c.protect(out.AccessToken); err != nil {
		return nil, resp, err
	}
	return &out, resp, nil
}

// Refresh rotates a session. proof is required for a device-bound session.
func (c *Client) Refresh(ctx context.Context, refreshToken, namespace string, proof *wallet.Proof) (*Session, *Response, error) {
	body := map[string]any{"refresh_token": refreshToken, "namespace": namespace}
	if proof != nil {
		body["device_proof"] = proof
	}
	out := Session{issuedAt: time.Now().Round(0)}
	resp, err := c.JSON(ctx, http.MethodPost, PathRefresh, "", body, &out)
	if err != nil {
		return nil, resp, err
	}
	if err := c.protect(out.AccessToken, out.RefreshToken); err != nil {
		return nil, resp, err
	}
	return &out, resp, nil
}

// Logout ends the session of refreshToken, or every session of the wallet with all.
func (c *Client) Logout(ctx context.Context, accessToken, refreshToken, namespace string, all bool) (*Response, error) {
	body := map[string]any{"refresh_token": refreshToken, "namespace": namespace, "all": all}
	return c.JSON(ctx, http.MethodPost, PathLogout, accessToken, body, nil)
}

// LogoutAll ends every session of the wallet from a session bound to a
// device, with that device's logout-all proof (nil sends none).
func (c *Client) LogoutAll(ctx context.Context, accessToken, refreshToken, namespace string, proof *wallet.Proof) (*Response, error) {
	body := map[string]any{"refresh_token": refreshToken, "namespace": namespace, "all": true}
	if proof != nil {
		body["device_proof"] = proof
	}
	return c.JSON(ctx, http.MethodPost, PathLogout, accessToken, body, nil)
}

// LogoutEverywhere ends every session of the wallet whose session s is: with
// the device's logout-all proof when s is bound to device, which the gateway
// requires, and without one when device is nil.
func (c *Client) LogoutEverywhere(ctx context.Context, s *Session, namespace string, device *wallet.Device) (*Response, error) {
	if device == nil {
		return c.Logout(ctx, s.AccessToken, s.RefreshToken, namespace, true)
	}
	proof, err := device.NewProof(wallet.ProofLogoutAll, namespace, device.ID())
	if err != nil {
		return nil, err
	}
	return c.LogoutAll(ctx, s.AccessToken, s.RefreshToken, namespace, proof)
}

// Whoami returns the caller as the gateway sees it.
func (c *Client) Whoami(ctx context.Context, token string) (map[string]any, *Response, error) {
	var out map[string]any
	resp, err := c.JSON(ctx, http.MethodGet, PathWhoami, token, nil, &out)
	return out, resp, err
}

// SessionView is one entry of GET /v1/auth/sessions.
type SessionView struct {
	ID        int64  `json:"id"`
	Subject   string `json:"subject"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	DeviceID  string `json:"device_id"`
}

// Sessions lists the wallet's live sessions.
func (c *Client) Sessions(ctx context.Context, token string) ([]SessionView, *Response, error) {
	var out struct {
		Sessions []SessionView `json:"sessions"`
	}
	resp, err := c.JSON(ctx, http.MethodGet, PathSessions, token, nil, &out)
	return out.Sessions, resp, err
}

// EndSession ends one session; proof is needed when token is device-bound.
func (c *Client) EndSession(ctx context.Context, token string, id int64, proof *wallet.Proof) (*Response, error) {
	return c.JSON(ctx, http.MethodDelete, PathSessions+"/"+strconv.FormatInt(id, 10), token, proofBody(proof), nil)
}

// DeviceView is one entry of GET /v1/auth/devices.
type DeviceView struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	RevokedAt string `json:"revoked_at"`
	Current   bool   `json:"current"`
}

// Devices lists the account's devices.
func (c *Client) Devices(ctx context.Context, token string) ([]DeviceView, *Response, error) {
	var out struct {
		Devices []DeviceView `json:"devices"`
	}
	resp, err := c.JSON(ctx, http.MethodGet, PathDevices, token, nil, &out)
	return out.Devices, resp, err
}

// RevokeDevice revokes one device; proof is needed when token is device-bound.
func (c *Client) RevokeDevice(ctx context.Context, token, deviceID string, proof *wallet.Proof) (*Response, error) {
	return c.JSON(ctx, http.MethodDelete, PathDevices+"/"+deviceID, token, proofBody(proof), nil)
}

func proofBody(proof *wallet.Proof) any {
	if proof == nil {
		return nil
	}
	return map[string]any{"device_proof": proof}
}

// protect registers freshly minted credentials with the evidence redactor
// (and through it the run's token registry), so they are masked even where no
// pattern would recognise them, in this process and in the runner's.
func (c *Client) protect(values ...string) error {
	red := c.rec.Redactor()
	if red == nil {
		return nil
	}
	if err := red.Add(values...); err != nil {
		return fmt.Errorf("failed to register minted credentials for redaction: %w", err)
	}
	return nil
}
