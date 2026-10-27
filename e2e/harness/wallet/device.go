package wallet

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Device key algorithms the gateway accepts.
const (
	AlgEd25519 = "Ed25519"
	AlgES256   = "ES256"
)

// DeviceProofVersion is the first line of every device proof statement.
const DeviceProofVersion = "orama-device-proof-v1"

// Device proof actions (docs/whitepaper/technical-reference/vol1/13-identity.md, "Proving the device on later requests").
const (
	ProofRefresh    = "refresh"
	ProofApprove    = "approve"
	ProofClaim      = "claim"
	ProofRevoke     = "revoke"
	ProofEndSession = "end-session"
	ProofLogoutAll  = "logout-all"
)

const (
	p256CoordBytes = 32
	proofIDBytes   = 24 // 32 base64url characters, inside the gateway's 16-128
)

var b64 = base64.RawURLEncoding

// Device is one installation's key pair.
type Device struct {
	alg   string
	ed    ed25519.PrivateKey
	ec    *ecdsa.PrivateKey
	jwk   string // canonical public JWK: the RFC 7638 members, ordered, no whitespace
	jwkID string
}

// NewEd25519Device generates an Ed25519 device key.
func NewEd25519Device() (*Device, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate an Ed25519 device key: %w", err)
	}
	canonical := `{"crv":"Ed25519","kty":"OKP","x":"` + b64.EncodeToString(pub) + `"}`
	return &Device{alg: AlgEd25519, ed: priv, jwk: canonical, jwkID: Thumbprint(canonical)}, nil
}

// NewES256Device generates a P-256 device key.
func NewES256Device() (*Device, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate a P-256 device key: %w", err)
	}
	pub, err := priv.PublicKey.ECDH()
	if err != nil {
		return nil, fmt.Errorf("failed to encode the P-256 device key: %w", err)
	}
	return newES256(priv, pub), nil
}

func newES256(priv *ecdsa.PrivateKey, pub *ecdh.PublicKey) *Device {
	point := pub.Bytes() // 0x04 || X || Y
	x := b64.EncodeToString(point[1 : 1+p256CoordBytes])
	y := b64.EncodeToString(point[1+p256CoordBytes:])
	canonical := `{"crv":"P-256","kty":"EC","x":"` + x + `","y":"` + y + `"}`
	return &Device{alg: AlgES256, ec: priv, jwk: canonical, jwkID: Thumbprint(canonical)}
}

// Thumbprint is RFC 7638: base64url(SHA-256(canonical JWK)), no padding.
func Thumbprint(canonicalJWK string) string {
	sum := sha256.Sum256([]byte(canonicalJWK))
	return b64.EncodeToString(sum[:])
}

// Alg is AlgEd25519 or AlgES256.
func (d *Device) Alg() string { return d.alg }

// ID is the device id the gateway computes: the key's RFC 7638 thumbprint.
func (d *Device) ID() string { return d.jwkID }

// PublicJWK is the public key as the verify request's device_key.
func (d *Device) PublicJWK() json.RawMessage { return json.RawMessage(d.jwk) }

// PrivateJWK is the Ed25519 private JWK `orama auth login --device-key` reads.
// ES256 devices have none: the CLI accepts Ed25519 files only.
func (d *Device) PrivateJWK() ([]byte, error) {
	if d.alg != AlgEd25519 {
		return nil, fmt.Errorf("private JWK export is Ed25519 only, this device is %s", d.alg)
	}
	pub := d.ed.Public().(ed25519.PublicKey)
	return []byte(`{"crv":"Ed25519","d":"` + b64.EncodeToString(d.ed.Seed()) +
		`","kty":"OKP","x":"` + b64.EncodeToString(pub) + `"}`), nil
}

// Sign signs message and returns base64url: raw Ed25519, or ES256 as the
// 64-byte r||s JOSE uses.
func (d *Device) Sign(message []byte) (string, error) {
	if d.alg == AlgEd25519 {
		return b64.EncodeToString(ed25519.Sign(d.ed, message)), nil
	}
	digest := sha256.Sum256(message)
	r, s, err := ecdsa.Sign(rand.Reader, d.ec, digest[:])
	if err != nil {
		return "", fmt.Errorf("failed to sign with device %s: %w", d.jwkID, err)
	}
	sig := make([]byte, 2*p256CoordBytes)
	r.FillBytes(sig[:p256CoordBytes])
	s.FillBytes(sig[p256CoordBytes:])
	return b64.EncodeToString(sig), nil
}

// SignDER signs an ES256 message in ASN.1 DER, the encoding Android Keystore
// and SecKey produce; the gateway must accept both.
func (d *Device) SignDER(message []byte) (string, error) {
	if d.alg != AlgES256 {
		return "", fmt.Errorf("DER signatures are ES256 only, this device is %s", d.alg)
	}
	digest := sha256.Sum256(message)
	sig, err := ecdsa.SignASN1(rand.Reader, d.ec, digest[:])
	if err != nil {
		return "", fmt.Errorf("failed to DER-sign with device %s: %w", d.jwkID, err)
	}
	return b64.EncodeToString(sig), nil
}

// Proof is the device_proof object a request carries.
type Proof struct {
	IssuedAt  int64  `json:"iat"`
	ID        string `json:"id"`
	Signature string `json:"sig"`
}

// ProofMessage is the exact statement a device signs (docs/whitepaper/technical-reference/vol1/13-identity.md).
func ProofMessage(action, namespace, binding string, issuedAt int64, id string) []byte {
	return []byte(strings.Join([]string{
		DeviceProofVersion, action, namespace, binding, strconv.FormatInt(issuedAt, 10), id,
	}, "\n"))
}

// NewProof makes a fresh single-use proof for action on binding, now.
func (d *Device) NewProof(action, namespace, binding string) (*Proof, error) {
	raw := make([]byte, proofIDBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("failed to draw a proof id: %w", err)
	}
	return d.ProofAt(action, namespace, binding, time.Now(), b64.EncodeToString(raw))
}

// ProofAt makes a proof with a chosen time and id, for stale, future and
// replayed-id negative tests.
func (d *Device) ProofAt(action, namespace, binding string, at time.Time, id string) (*Proof, error) {
	sig, err := d.Sign(ProofMessage(action, namespace, binding, at.Unix(), id))
	if err != nil {
		return nil, err
	}
	return &Proof{IssuedAt: at.Unix(), ID: id, Signature: sig}, nil
}
