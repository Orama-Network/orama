package auth

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (s *Service) JWKSHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	keys := make([]any, 0, 2)

	// RSA key (RS256)
	if s.signingKey != nil {
		pub := s.signingKey.Public().(*rsa.PublicKey)
		n := pub.N.Bytes()
		eVal := pub.E
		eb := make([]byte, 0)
		for eVal > 0 {
			eb = append([]byte{byte(eVal & 0xff)}, eb...)
			eVal >>= 8
		}
		if len(eb) == 0 {
			eb = []byte{0}
		}
		keys = append(keys, map[string]string{
			"kty": "RSA",
			"use": "sig",
			"alg": "RS256",
			"kid": s.keyID,
			"n":   base64.RawURLEncoding.EncodeToString(n),
			"e":   base64.RawURLEncoding.EncodeToString(eb),
		})
	}

	// Every Ed25519 key this cluster will accept a token from, not only this
	// gateway's own. A client that verifies a token locally needs the key that
	// signed it, and after key separation that is a different key per
	// namespace — a JWKS carrying one of them would make every other token
	// unverifiable.
	//
	// The namespace a key is bound to is published with it. It is not part of
	// the JWK standard, so it goes in as an additional member: a client that
	// does not know about it ignores it, and one that does can refuse a token
	// whose claim disagrees without asking the gateway.
	for _, key := range s.signingKeys.All() {
		keys = append(keys, map[string]string{
			"kty":       "OKP",
			"use":       "sig",
			"alg":       "EdDSA",
			"kid":       key.KID,
			"crv":       "Ed25519",
			"x":         base64.RawURLEncoding.EncodeToString(key.Public),
			"namespace": key.Namespace,
		})
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
}

// Internal types for JWT handling
type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

type JWTClaims struct {
	Iss string `json:"iss"`
	Sub string `json:"sub"`
	Aud string `json:"aud"`
	Iat int64  `json:"iat"`
	Nbf int64  `json:"nbf"`
	Exp int64  `json:"exp"`
	// Jti names this token. Without one a token could not be revoked
	// individually: logging out dropped the refresh token and left the access
	// token valid until it expired. Tokens minted before this have no jti and
	// are covered only by a revocation of their subject.
	Jti       string `json:"jti,omitempty"`
	Namespace string `json:"namespace"`
	// Did is the device the session is bound to: the RFC 7638 thumbprint of a
	// key the device proved it holds when the session was issued. Empty for a
	// session bound to the account alone. Set by the gateway only — a claims
	// provider cannot, and a function reads it through get_caller_device_id.
	Did string `json:"did,omitempty"`
	// Sid names the session, and stays the same across refresh-token
	// rotations, so ending the session refuses every access token it minted.
	Sid string `json:"sid,omitempty"`
	// Att is what the device's attestation proved (feat-941), nil when the
	// session's device has not attested or its namespace does not attest.
	// Set by the gateway only, from a verified proof; a function reads it
	// through get_caller_attestation.
	Att *AttestationClaim `json:"att,omitempty"`
	// Custom holds app-defined claims (e.g. tier, subscription state).
	// Read by serverless functions via the get_caller_claim host call.
	// May be nil if the token has no custom claims.
	Custom map[string]string `json:"custom,omitempty"`
}

// The audiences a session's access token is minted for.
//
// A device still pending under the function policy holds a token for
// AudiencePendingDevice, never for AudienceGateway. The difference is in the
// audience rather than in a claim of its own so that anything which does not
// know about pending devices refuses the token instead of reading it as a full
// session: a gateway from before the policy rejects the audience outright, and
// so does any verifier that checks for AudienceGateway.
const (
	// AudienceGateway is a full session's.
	AudienceGateway = "gateway"
	// AudiencePendingDevice is the restricted session of a pending device: the
	// gateway refuses it everywhere but the routes that admit pending devices
	// (routepolicy.Policy.PendingDevice), and the invoker runs for it only the
	// functions open to pending devices.
	AudiencePendingDevice = "gateway-pending-device"
)

// The states of the device a token is bound to, as a function reads them
// (get_caller_device_state).
const (
	CallerDevicePending = "pending"
	CallerDeviceActive  = "active"
)

// PendingDevice reports whether the token is a pending device's restricted
// session.
func (c *JWTClaims) PendingDevice() bool {
	return c != nil && c.Aud == AudiencePendingDevice
}

// DeviceState is the state the token's device was in when it was minted:
// CallerDevicePending, CallerDeviceActive, or "" for a token bound to no
// device. An active device's token stops when the device is revoked, so the
// state it names holds for as long as the token is accepted; a pending device
// approved since says "pending" until its next refresh.
func (c *JWTClaims) DeviceState() string {
	switch {
	case c == nil || c.Did == "":
		return ""
	case c.PendingDevice():
		return CallerDevicePending
	default:
		return CallerDeviceActive
	}
}

// MintedDeviceState is DeviceState of an access token this gateway has just
// minted and is about to hand back, read without verifying it: it is for
// telling the client what it was given (a refresh answers it beside the
// token), never for deciding what a presented token may do — that is
// ParseAndVerifyJWT's.
func MintedDeviceState(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims JWTClaims
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.DeviceState()
}

// AccountIDClaim is the custom claim a namespace sets, through its
// auth-claims-provider function, to name the account a session belongs to
// (e.g. AnChat's users.user_id). The subject is the credential that signed in —
// a wallet — and one account may sign in with several; the account is who the
// user is to the namespace.
const AccountIDClaim = "account_id"

// AccountIdentity is who the caller is to the namespace: the account the
// namespace named in the AccountIDClaim custom claim, or the subject when the
// token carries no such claim. The claim is signed into the token at mint time
// by the gateway, so a client cannot choose it.
func (c *JWTClaims) AccountIdentity() string {
	if account := c.Custom[AccountIDClaim]; account != "" {
		return account
	}
	return c.Sub
}

// revocationSubjectKeys returns the names a token's subject may have been
// revoked under.
//
// A wallet subject is revoked under itself. A token exchanged from an API key
// carries the raw key as its subject, while RevokeKey only ever has the hash —
// so the hash is the other name to look under.
func (s *Service) revocationSubjectKeys(subject string) []string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return nil
	}
	keys := []string{subject}
	if hashed := s.HashAPIKey(subject); hashed != "" && hashed != subject {
		keys = append(keys, hashed)
	}
	return keys
}

// newTokenID mints the name a token is revoked by.
//
// 128 bits from crypto/rand: it only has to be unique, and a guessable one
// would let somebody revoke a session they do not hold.
func newTokenID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("could not generate a token id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// ParseAndVerifyJWT verifies a JWT created by this gateway using kid-based key
// selection. It accepts both RS256 (legacy) and EdDSA (new) tokens.
//
// Security (C3 fix): The key is selected by kid, then cross-checked against alg
// to prevent algorithm confusion attacks. Only RS256 and EdDSA are accepted.
func (s *Service) ParseAndVerifyJWT(token string) (*JWTClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid token format")
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("invalid header encoding")
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid payload encoding")
	}
	sb, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("invalid signature encoding")
	}

	var header jwtHeader
	if err := json.Unmarshal(hb, &header); err != nil {
		return nil, errors.New("invalid header json")
	}

	// Explicit algorithm allowlist — reject everything else before verification
	if header.Alg != "RS256" && header.Alg != "EdDSA" {
		return nil, errors.New("unsupported algorithm")
	}

	signingInput := parts[0] + "." + parts[1]

	// Key selection by kid (not alg) — prevents algorithm confusion (C3 fix)
	//
	// bound is the namespace the selected key may sign for. It is checked
	// against the claim after the claims are parsed: a namespace gateway's key
	// signs only for its own tenant, which is what stops one compromised
	// namespace gateway minting a token for another.
	var bound SigningKey
	edKey, edFound := s.signingKeys.Lookup(header.Kid)

	switch {
	case header.Kid != "" && edFound:
		// EdDSA key matched by kid — cross-check alg
		if header.Alg != "EdDSA" {
			return nil, errors.New("algorithm mismatch for key")
		}
		if !ed25519.Verify(edKey.Public, []byte(signingInput), sb) {
			return nil, errors.New("invalid signature")
		}
		bound = edKey

	case header.Kid != "" && header.Kid == s.keyID && s.signingKey != nil:
		// RSA key matched by kid — cross-check alg
		if header.Alg != "RS256" {
			return nil, errors.New("algorithm mismatch for key")
		}
		sum := sha256.Sum256([]byte(signingInput))
		pub := s.signingKey.Public().(*rsa.PublicKey)
		if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sb); err != nil {
			return nil, errors.New("invalid signature")
		}

	default:
		// A token with no kid, or one naming a key this gateway does not have.
		// There used to be a branch here that accepted a token with no kid at
		// all, verifying it against the RSA key — so a token that named no key
		// selected one by omission, which is the shape of an algorithm
		// confusion attack. This gateway has always put a kid in what it mints.
		return nil, errors.New("unknown key ID")
	}

	// Parse claims
	var claims JWTClaims
	if err := json.Unmarshal(pb, &claims); err != nil {
		return nil, errors.New("invalid claims json")
	}
	// Validate issuer
	if claims.Iss != "orama-gateway" {
		return nil, errors.New("invalid issuer")
	}
	// A key bound to a namespace signs only for that namespace. Without this
	// the binding is a label: the signature is what a verifier trusts, and a
	// namespace gateway that could sign any claim would be back where it
	// started.
	if !bound.Binds(claims.Namespace) {
		return nil, fmt.Errorf("the key %s signs for %q, and this token claims %q",
			bound.KID, bound.Namespace, claims.Namespace)
	}
	// A signature that verifies says the gateway minted this token, not that
	// the token is still good. Revoking a key used to stop the key and leave
	// its tokens working for the rest of their lifetime.
	denied, err := s.revocations.Denies(&claims, s.revocationSubjectKeys(claims.Sub))
	if err != nil {
		return nil, err
	}
	if denied {
		return nil, ErrTokenRevoked
	}
	// Validate registered claims
	now := time.Now().Unix()
	const skew = int64(60) // allow small clock skew ±60s
	if claims.Nbf != 0 && now+skew < claims.Nbf {
		return nil, errors.New("token not yet valid")
	}
	if claims.Exp != 0 && now-skew > claims.Exp {
		return nil, ErrTokenExpired
	}
	if claims.Iat != 0 && claims.Iat-skew > now {
		return nil, errors.New("invalid iat")
	}
	switch claims.Aud {
	case AudienceGateway:
	case AudiencePendingDevice:
		// Only a device is ever pending: a pending token bound to none is not
		// one this gateway mints.
		if strings.TrimSpace(claims.Did) == "" {
			return nil, errors.New("invalid audience: a pending device's token names no device")
		}
	default:
		return nil, errors.New("invalid audience")
	}
	// A workload's subject names the namespace it belongs to. A token whose
	// claim says another is a token no gateway mints.
	if err := checkWorkloadNamespace(&claims); err != nil {
		return nil, err
	}
	return &claims, nil
}

// AccessTokenLifetime is how long a minted access token is good for. It is the
// window a rotation has to leave the outgoing key verifiable for, and the
// window the previous cluster-derived key is accepted across an upgrade.
const AccessTokenLifetime = 15 * time.Minute

// MaxTokenLifetime bounds every access token this cluster mints: a session's
// lasts AccessTokenLifetime, and one exchanged from a key or held by a workload
// lasts up to an hour. It is what a gateway may assume about a token whose
// times it was not told — the most it can have left, and the earliest it can
// have been issued.
const MaxTokenLifetime = maxExchangedTokenLifetime

// SessionBinding is what an access token is bound to beyond its account: the
// device that holds the session, and the session itself. Either may be empty.
type SessionBinding struct {
	DeviceID  string
	SessionID string
	// Attestation is what the device's attestation proved, or nil.
	Attestation *AttestationClaim
	// Pending mints the restricted session of a device still pending under
	// the function policy (AudiencePendingDevice). It needs a DeviceID.
	Pending bool
}

// GenerateJWT mints a signed access token. `custom` carries additive
// app-defined claims (e.g. the namespace's account_id from the claims-provider
// hook, bugboard #548) under the top-level "custom" object — read back via
// JWTClaims.Custom / oh.GetCallerClaim. Pass nil for none. Reserved claims
// (sub/iss/aud/iat/nbf/exp/jti/namespace/did/sid) are always gateway-controlled
// and cannot be overridden by `custom` (the claims-provider path sanitizes
// them out upstream).
func (s *Service) GenerateJWT(ns, subject string, ttl time.Duration, custom map[string]string) (string, int64, error) {
	return s.GenerateBoundJWT(ns, subject, ttl, custom, SessionBinding{})
}

// GenerateBoundJWT mints an access token carrying its session binding.
func (s *Service) GenerateBoundJWT(ns, subject string, ttl time.Duration, custom map[string]string, binding SessionBinding) (string, int64, error) {
	audience := AudienceGateway
	if binding.Pending {
		if binding.DeviceID == "" {
			return "", 0, errors.New("a pending session is a device's: it needs a device id")
		}
		audience = AudiencePendingDevice
	}
	now := time.Now().UTC()
	exp := now.Add(ttl)
	jti, err := newTokenID()
	if err != nil {
		return "", 0, err
	}
	payload := map[string]any{
		"iss":       "orama-gateway",
		"sub":       subject,
		"aud":       audience,
		"iat":       now.Unix(),
		"nbf":       now.Unix(),
		"exp":       exp.Unix(),
		"jti":       jti,
		"namespace": ns,
	}
	if custom = withoutReservedCustomClaims(custom); len(custom) > 0 {
		payload["custom"] = custom
	}
	if binding.DeviceID != "" {
		payload["did"] = binding.DeviceID
	}
	if binding.SessionID != "" {
		payload["sid"] = binding.SessionID
	}
	if binding.Attestation != nil && binding.DeviceID != "" {
		payload["att"] = binding.Attestation
	}
	pb, err := json.Marshal(payload)
	if err != nil {
		return "", 0, fmt.Errorf("encode token claims: %w", err)
	}
	// Prefer EdDSA when available
	var token string
	if s.preferEdDSA && s.edSigningKey != nil {
		token, err = s.signEdDSA(pb)
	} else {
		token, err = s.signRSA(pb)
	}
	if err != nil {
		return "", 0, err
	}
	return token, exp.Unix(), nil
}

// reservedCustomClaims are the binding's names. They are dropped from the
// custom claims however those arrived — the claims provider refuses them, and
// custom claims stored with a session before it did are replayed on every
// rotation — so get_caller_claim("did") can never name a device the caller
// did not prove it holds.
var reservedCustomClaims = []string{"did", "sid", "jti", "att"}

func withoutReservedCustomClaims(custom map[string]string) map[string]string {
	clean := custom
	for _, key := range reservedCustomClaims {
		if _, present := custom[key]; !present {
			continue
		}
		if len(clean) == len(custom) {
			clean = make(map[string]string, len(custom))
			for k, v := range custom {
				clean[k] = v
			}
		}
		delete(clean, key)
	}
	return clean
}

func (s *Service) signEdDSA(payload []byte) (string, error) {
	if s.edSigningKey == nil {
		return "", errors.New("EdDSA signing key unavailable")
	}
	hb, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": s.edKeyID})
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(payload)
	sig := ed25519.Sign(s.edSigningKey, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (s *Service) signRSA(payload []byte) (string, error) {
	if s.signingKey == nil {
		return "", errors.New("signing key unavailable")
	}
	hb, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": s.keyID})
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(payload)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.signingKey, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
