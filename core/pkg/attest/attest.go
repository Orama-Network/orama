// Package attest verifies device attestations: proof that a caller is a
// genuine, unmodified build of a namespace's app running on a genuine device.
// It is pure verification with no gateway imports; the gateway (challenges,
// records, enforcement) builds on it.
//
// Two platforms are verified, each against roots compiled into this package:
//   - iOS: an App Attest attestation (once per key) and its assertions (on
//     each refresh), chained to the Apple App Attestation Root CA
//     (attest/apple).
//   - Android, GrapheneOS included: a hardware key-attestation certificate
//     chain to one of Google's attestation roots, checked against Google's
//     revocation list (attest/android, attest/revocation). A device that boots
//     an OS signed with its own key (verifiedBootState SelfSigned) is accepted
//     only when that key is on GrapheneOS's signed list (attest/graphene).
//
// Nothing here depends on Google Play services. Every refusal is a *Error with
// a Kind (what the client is told) and a Reason (which check refused it).
package attest

import (
	"github.com/Orama-Network/orama/core/pkg/attest/refusal"
)

// Platform names the attestation scheme a proof used.
type Platform string

const (
	PlatformIOS     Platform = "ios"
	PlatformAndroid Platform = "android"
)

// Verifier checks attestation proofs against a namespace's Policy. The
// caller owns challenges and device binding: it derives clientDataHash (iOS)
// or challenge (Android) from the server challenge and the device id it is
// binding, and passes the result in. An implementation is safe for
// concurrent use.
type Verifier interface {
	// AttestIOS verifies an App Attest attestation object for keyID (the
	// SHA-256 of the key's public point, as DCAppAttestService returns it).
	// The Verdict carries the public key and counter the caller stores to
	// verify this key's assertions later.
	AttestIOS(p Policy, keyID, attestationObject []byte, clientDataHash [32]byte) (Verdict, error)
	// AssertIOS verifies an App Attest assertion made with a key the caller
	// stored from AttestIOS. The returned counter is strictly greater than
	// key.Counter; the caller stores it with a compare-and-swap on the old
	// value so that a replayed assertion loses the race.
	AssertIOS(p Policy, key IOSKey, assertion []byte, clientDataHash [32]byte) (Assertion, error)
	// AttestAndroid verifies a key-attestation certificate chain, leaf first,
	// each certificate DER-encoded, whose attestationChallenge must equal
	// challenge.
	AttestAndroid(p Policy, chain [][]byte, challenge []byte) (Verdict, error)
}

// Policy is what one namespace accepts. The iOS fields are needed only for
// AttestIOS and AssertIOS, the Android fields only for AttestAndroid.
type Policy struct {
	// AppleTeamID and AppleBundleID form the App ID ("TEAMID.bundle.id")
	// whose SHA-256 every iOS proof must carry as its RP ID hash.
	AppleTeamID   string
	AppleBundleID string
	// AllowDevelopment accepts keys from the App Attest development
	// environment and development-signed builds. Devnet only. Where iOS
	// signs the build's validation category (iOS 27 and later), App Store
	// and TestFlight builds are accepted, development-signed builds only
	// with AllowDevelopment, and every other category is refused.
	AllowDevelopment bool
	// MinIOSBundleVersion is the oldest CFBundleVersion accepted, one to
	// three period-separated integers ("" sets no minimum). Only iOS 27 and
	// later sign the bundle version; an older iOS proves the app genuine but
	// not its version, and is accepted with the version unknown.
	MinIOSBundleVersion string

	// AndroidPackage is the one package name the attested app must have.
	AndroidPackage string
	// AndroidSigningCertDigests are the lowercase-hex SHA-256 digests of the
	// signing certificates the app may be signed with. Every digest the
	// device attests must be one of these.
	AndroidSigningCertDigests []string
	// MinAndroidVersionCode is the oldest versionCode accepted (0 sets no
	// minimum). The device fixes it when the key is generated, so a raised
	// minimum takes effect at the next attestation.
	MinAndroidVersionCode int64
	// MaxPatchAgeMonths refuses an Android OS whose security patch level is
	// more than this many months old (0 sets no limit).
	MaxPatchAgeMonths int
}

// IOSKey is what the caller stored from a verified iOS attestation.
type IOSKey struct {
	// PublicKey is the P-256 public key, X9.62 uncompressed (65 bytes).
	PublicKey []byte
	// Counter is the counter of the last accepted attestation or assertion.
	Counter uint32
}

// SecurityLevel is where an Android key lives and is attested.
type SecurityLevel int

const (
	SecuritySoftware  SecurityLevel = 0
	SecurityTEE       SecurityLevel = 1
	SecurityStrongBox SecurityLevel = 2
)

// BootState is an Android device's verified-boot state as its hardware
// attests it. Only BootVerified and BootSelfSigned are ever accepted.
type BootState int

const (
	BootVerified   BootState = 0
	BootSelfSigned BootState = 1
	BootUnverified BootState = 2
	BootFailed     BootState = 3
)

// Verdict is what an accepted attestation proves.
type Verdict struct {
	Platform Platform
	// AppID is "TEAMID.bundle.id" on iOS and the package name on Android.
	AppID string
	// AppVersion is the build the platform signed: CFBundleVersion on iOS 27
	// and later, versionCode in decimal on Android. "" means unknown (iOS 26
	// and earlier sign no version).
	AppVersion string

	// KeyID and PublicKey (X9.62 uncompressed P-256) identify the iOS App
	// Attest key; Counter is its attested counter (always 0).
	KeyID     []byte
	PublicKey []byte
	Counter   uint32
	// Development is true for a key from the App Attest development
	// environment.
	Development bool
	// ValidationCategory is Apple's signed launch category of the build
	// (4 = App Store); 0 when the OS signs none (iOS 26 and earlier).
	ValidationCategory uint32
	// Receipt is Apple's attestation receipt, kept for a later fraud-metric
	// request to Apple.
	Receipt []byte

	// SecurityLevel is the Android attestation security level (TEE or
	// StrongBox; the KeyMint level is equal).
	SecurityLevel SecurityLevel
	// BootState and BootKey are the attested verified-boot state and the
	// SHA-256 of the key the OS is signed with. The caller pins BootKey per
	// device on first use and refuses a later attestation with another key.
	BootState BootState
	BootKey   []byte
	// GrapheneOS is true when BootKey is on GrapheneOS's signed list.
	GrapheneOS bool
	// SigningCertDigests are the lowercase-hex SHA-256 digests of the
	// certificates the attested app is signed with, every one of them in
	// the policy's AndroidSigningCertDigests when the proof was accepted.
	SigningCertDigests []string
	// OSPatchLevel is YYYYMM; VendorPatchLevel and BootPatchLevel are
	// YYYYMMDD, 0 when the device does not attest them.
	OSPatchLevel     int
	VendorPatchLevel int
	BootPatchLevel   int
}

// Assertion is what an accepted iOS assertion proves.
type Assertion struct {
	// Counter is the assertion's counter, the value the caller stores.
	Counter uint32
	// AppVersion and ValidationCategory are as in Verdict: signed on iOS 27
	// and later, "" and 0 before.
	AppVersion         string
	ValidationCategory uint32
}

// Error is a refused proof; see refusal.Error.
type Error = refusal.Error

// Kind is what a refusal means to the client.
type Kind = refusal.Kind

const (
	// KindNone: not a refusal but the server's own error (answer 500).
	KindNone = refusal.KindNone
	// KindInvalid: answer ATTESTATION_INVALID.
	KindInvalid = refusal.KindInvalid
	// KindAppUpdateRequired: answer APP_UPDATE_REQUIRED.
	KindAppUpdateRequired = refusal.KindAppUpdateRequired
)

// Reason labels the check that refused a proof (for counting refusals).
type Reason = refusal.Reason

// KindOf returns the Kind of err, KindNone when err is not a refusal.
func KindOf(err error) Kind {
	if r, ok := refusal.As(err); ok {
		return r.Kind
	}
	return KindNone
}

// ReasonOf returns the Reason of a refusal, "" when err is not one.
func ReasonOf(err error) Reason {
	if r, ok := refusal.As(err); ok {
		return r.Reason
	}
	return ""
}
