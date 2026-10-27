// Package android verifies Android hardware key attestation: a certificate
// chain from a key the app generated in the device's TEE or StrongBox up to
// one of Google's attestation roots, whose leaf describes the key, the app
// that generated it and the device's boot state (the KeyDescription
// extension, AOSP "Key and ID attestation").
//
// Both Google roots are compiled in and pinned by public key: the RSA-4096
// root (subject serialNumber=f92009e853b6b045) and the P-384 "Key
// Attestation CA1" that signs chains since 2026-02-01. Nothing here uses
// Google Play services, so GrapheneOS and other OSes without them verify
// like any other.
package android

import (
	"bytes"
	"crypto/x509"
	_ "embed"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	"github.com/Orama-Network/orama/core/pkg/attest/refusal"
)

//go:embed google_roots.pem
var rootsPEM []byte

// RevocationList answers whether an attestation certificate is revoked.
type RevocationList interface {
	// Revoked reports whether the certificate with this serial (lowercase
	// hex, no leading zeros) is revoked or suspended. It errors when no list
	// has been loaded, which is the server's problem, not a refusal.
	Revoked(serialHex string) (bool, error)
}

// BootKeyList is the set of verified-boot keys of OSes signed with their own
// key (GrapheneOS) that are accepted with verifiedBootState SelfSigned.
type BootKeyList interface {
	Contains(verifiedBootKey []byte) bool
}

// Verifier verifies Android key attestation chains.
type Verifier struct {
	// RootKeys are the DER SubjectPublicKeyInfo of the trusted roots.
	RootKeys [][]byte
	// Now is the time intermediate certificates must be valid at.
	Now func() time.Time
	// Revocation is Google's attestation revocation list.
	Revocation RevocationList
	// SelfSignedKeys are the accepted keys for SelfSigned boot.
	SelfSignedKeys BootKeyList
}

// New returns a Verifier pinning Google's attestation roots.
func New(revocation RevocationList, selfSigned BootKeyList) *Verifier {
	return &Verifier{RootKeys: googleRootKeys(), Now: time.Now, Revocation: revocation, SelfSignedKeys: selfSigned}
}

func googleRootKeys() [][]byte {
	var keys [][]byte
	rest := rootsPEM
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			panic(fmt.Sprintf("android: a compiled-in Google root does not parse: %v", err))
		}
		keys = append(keys, cert.RawSubjectPublicKeyInfo)
	}
	if len(keys) == 0 {
		panic("android: no compiled-in Google roots")
	}
	return keys
}

// Expect is what an attestation must show.
type Expect struct {
	// Challenge is the attestationChallenge the key was generated with.
	Challenge []byte
	// Package is the one package name the app must have.
	Package string
	// SigningDigests is the set of accepted SHA-256 signing-certificate
	// digests, keyed by the raw digest bytes.
	SigningDigests map[string]bool
	// MaxPatchAgeMonths refuses an OS patch level older than this many
	// months (0: no limit).
	MaxPatchAgeMonths int
}

// Verdict is what a verified attestation proves.
type Verdict struct {
	SecurityLevel SecurityLevel
	Challenge     []byte
	Package       string
	VersionCode   int64
	// SigningDigests are the attested signing-certificate digests.
	SigningDigests [][]byte
	DeviceLocked   bool
	BootState      BootState
	BootKey        []byte
	// SelfSignedKeyListed is true when BootKey is on the SelfSigned list.
	SelfSignedKeyListed bool
	OSVersion           int64
	OSPatchLevel        int64
	VendorPatchLevel    int64
	BootPatchLevel      int64
}

// Verify verifies a key attestation chain, leaf first, each certificate
// DER, against want.
func (v *Verifier) Verify(chain [][]byte, want Expect) (Verdict, error) {
	var out Verdict
	now := v.Now()
	certs, err := verifyChain(chain, v.RootKeys, now)
	if err != nil {
		return out, err
	}
	for _, c := range certs {
		revoked, err := v.Revocation.Revoked(serialHex(c))
		if err != nil {
			return out, fmt.Errorf("check the attestation chain against the revocation list: %w", err)
		}
		if revoked {
			return out, refusal.Invalid(refusal.ReasonRevoked, "certificate %q (serial %s) is revoked", c.Subject, serialHex(c))
		}
	}

	ext, _ := keyDescriptionExtension(certs[0])
	kd, err := ParseKeyDescription(ext)
	if err != nil {
		return out, refusal.Invalid(refusal.ReasonMalformed, "key description: %w", err)
	}
	if kd.AttestationSecurityLevel < SecurityTEE || kd.KeyMintSecurityLevel != kd.AttestationSecurityLevel {
		return out, refusal.Invalid(refusal.ReasonSecurityLevel, "attestation security level %d and KeyMint security level %d, want equal and TEE or StrongBox", kd.AttestationSecurityLevel, kd.KeyMintSecurityLevel)
	}
	if !bytes.Equal(kd.AttestationChallenge, want.Challenge) {
		return out, refusal.Invalid(refusal.ReasonChallenge, "the key was not generated with this challenge")
	}

	app := kd.SoftwareEnforced.ApplicationID
	if app == nil {
		return out, refusal.Invalid(refusal.ReasonPackage, "the key description names no application")
	}
	if len(app.Packages) != 1 || app.Packages[0].Name != want.Package {
		return out, refusal.Invalid(refusal.ReasonPackage, "the key belongs to %s, want exactly %s", packageNames(app.Packages), want.Package)
	}
	if len(app.SignatureDigests) == 0 {
		return out, refusal.Invalid(refusal.ReasonSigningCert, "the key description names no signing certificate")
	}
	for _, d := range app.SignatureDigests {
		if !want.SigningDigests[string(d)] {
			return out, refusal.Invalid(refusal.ReasonSigningCert, "the app is signed with certificate %x, which the policy does not accept", d)
		}
	}

	rot := kd.HardwareEnforced.RootOfTrust
	if rot == nil {
		return out, refusal.Invalid(refusal.ReasonBootState, "the secure hardware attests no root of trust")
	}
	if !rot.DeviceLocked {
		return out, refusal.Invalid(refusal.ReasonDeviceUnlocked, "the bootloader is unlocked")
	}
	listed := v.SelfSignedKeys.Contains(rot.VerifiedBootKey)
	switch rot.VerifiedBootState {
	case BootVerified:
	case BootSelfSigned:
		if !listed {
			return out, refusal.Invalid(refusal.ReasonBootKey, "the OS is signed with key %x, which is not on the accepted list", rot.VerifiedBootKey)
		}
	default:
		return out, refusal.Invalid(refusal.ReasonBootState, "verified boot state %d is neither Verified nor SelfSigned", rot.VerifiedBootState)
	}
	if err := checkPatchAge(kd.HardwareEnforced.OSPatchLevel, want.MaxPatchAgeMonths, now); err != nil {
		return out, err
	}

	return Verdict{
		SecurityLevel:       kd.AttestationSecurityLevel,
		Challenge:           kd.AttestationChallenge,
		Package:             app.Packages[0].Name,
		VersionCode:         app.Packages[0].Version,
		SigningDigests:      app.SignatureDigests,
		DeviceLocked:        rot.DeviceLocked,
		BootState:           rot.VerifiedBootState,
		BootKey:             rot.VerifiedBootKey,
		SelfSignedKeyListed: listed,
		OSVersion:           kd.HardwareEnforced.OSVersion,
		OSPatchLevel:        kd.HardwareEnforced.OSPatchLevel,
		VendorPatchLevel:    kd.HardwareEnforced.VendorPatchLevel,
		BootPatchLevel:      kd.HardwareEnforced.BootPatchLevel,
	}, nil
}

// checkPatchAge refuses an OS patch level (YYYYMM) more than maxMonths
// months before now.
func checkPatchAge(level int64, maxMonths int, now time.Time) error {
	if maxMonths == 0 {
		return nil
	}
	age, ok := PatchAgeMonths(level, now)
	if !ok {
		return refusal.Invalid(refusal.ReasonPatchLevel, "the secure hardware attests OS patch level %d, not YYYYMM", level)
	}
	if age > int64(maxMonths) {
		return refusal.Invalid(refusal.ReasonPatchLevel, "OS patch level %d is %d months old, more than %d", level, age, maxMonths)
	}
	return nil
}

// PatchAgeMonths is how many calendar months an OS patch level (YYYYMM) is
// before now, ok false when level is not of that form. The gateway applies it
// again to a recorded patch level, so that a patch window narrowed after a
// device attested reaches that device's tokens too.
func PatchAgeMonths(level int64, now time.Time) (int64, bool) {
	year, month := level/100, level%100
	if level <= 0 || month < 1 || month > 12 {
		return 0, false
	}
	return (int64(now.Year())*12 + int64(now.Month())) - (year*12 + month), true
}

func packageNames(pkgs []PackageInfo) string {
	if len(pkgs) == 0 {
		return "no package"
	}
	names := make([]string, len(pkgs))
	for i, p := range pkgs {
		names[i] = p.Name
	}
	return strings.Join(names, ", ")
}
