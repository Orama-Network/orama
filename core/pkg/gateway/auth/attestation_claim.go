package auth

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Orama-Network/orama/core/pkg/attest"
	"github.com/Orama-Network/orama/core/pkg/attest/android"
	"github.com/Orama-Network/orama/core/pkg/attest/refusal"
)

// AttestationClaim is what a device's attestation proved, as the access token
// carries it (the `att` claim). Only the gateway sets it — from a verdict of
// package attest, never from anything a client sent — and it is signed into
// the same token as the device it was proved for (`did`), so it cannot be
// moved onto another device's session.
//
// Carrying the verdict in the token is what keeps judging a request off the
// registry: a request is judged by its own token and the namespace's policy,
// both in memory. A raised minimum version, a withdrawn platform, a removed
// signing certificate or a closed development environment reaches every token
// at once through the policy; a revoked device reaches its tokens through the
// revocation list.
type AttestationClaim struct {
	// Platform is attest.PlatformIOS or attest.PlatformAndroid.
	Platform string `json:"p"`
	// AppID is "TEAMID.bundle.id" on iOS and the package name on Android.
	AppID string `json:"app"`
	// AppVersion is the build the platform signed: CFBundleVersion on iOS 27
	// and later, versionCode on Android; "" when it signed none.
	AppVersion string `json:"v,omitempty"`
	// Development marks an iOS key of the App Attest development environment.
	Development bool `json:"dev,omitempty"`
	// GrapheneOS marks an Android device booting GrapheneOS.
	GrapheneOS bool `json:"gos,omitempty"`
	// SigningCertDigests are the lowercase-hex SHA-256 digests of the
	// certificates the attested Android app is signed with. Empty on iOS, and
	// on a token minted from a record that predates them (migration 094),
	// which judgeAndroid does not admit.
	SigningCertDigests []string `json:"sig,omitempty"`
	// OSPatchLevel is the Android OS security patch level the device attested
	// (YYYYMM), 0 when it attested none.
	OSPatchLevel int `json:"pl,omitempty"`
	// AttestedAt is when the device last attested, unix seconds. An Android
	// attestation lasts the policy's ReattestInterval from here.
	AttestedAt int64 `json:"at"`
}

// AttestationRefusal is why a namespace's policy does not admit a caller.
type AttestationRefusal int

const (
	// AttestationAdmitted is no refusal.
	AttestationAdmitted AttestationRefusal = iota
	// AttestationRequired: the caller has not attested, or its attestation
	// has run out or does not show what the policy now asks. The app attests
	// (again) and carries on.
	AttestationRequired
	// AttestationInvalid: the caller's attestation is of something the
	// namespace does not accept — another app, a platform it does not take,
	// a signing certificate, an environment or OS it no longer allows.
	AttestationInvalid
	// AttestationUpdateRequired: the build is older than the namespace
	// accepts.
	AttestationUpdateRequired
)

// String is the refusal's label in counts and logs.
func (r AttestationRefusal) String() string {
	switch r {
	case AttestationRequired:
		return "required"
	case AttestationInvalid:
		return "invalid"
	case AttestationUpdateRequired:
		return "app_update_required"
	default:
		return "admitted"
	}
}

// AttestationJudgement is a policy's decision on one caller.
type AttestationJudgement struct {
	Refusal AttestationRefusal
	// Reason names the check that found the attestation invalid or the build
	// too old; "" when the caller is admitted or must attest (again).
	Reason refusal.Reason
	// Message says why, for the caller; "" when it is admitted.
	Message string
}

func admitted() AttestationJudgement { return AttestationJudgement{} }

func mustAttest(message string) AttestationJudgement {
	return AttestationJudgement{Refusal: AttestationRequired, Message: message}
}

func invalidAttestation(reason refusal.Reason, message string) AttestationJudgement {
	return AttestationJudgement{Refusal: AttestationInvalid, Reason: reason, Message: message}
}

func updateRequired(message string) AttestationJudgement {
	return AttestationJudgement{Refusal: AttestationUpdateRequired, Reason: refusal.ReasonAppVersion, Message: message}
}

// JudgeAttestation decides whether policy admits a caller whose token carries
// claim (nil when it carries none) at now, and says why not.
func JudgeAttestation(policy AttestationPolicy, claim *AttestationClaim, now time.Time) AttestationJudgement {
	if policy.Mode == AttestationOff {
		return admitted()
	}
	if claim == nil {
		return mustAttest("this namespace admits only its own app on an attested device: attest this device, then refresh the session")
	}
	switch attest.Platform(claim.Platform) {
	case attest.PlatformIOS:
		return judgeIOS(policy, claim)
	case attest.PlatformAndroid:
		return judgeAndroid(policy, claim, now)
	default:
		return invalidAttestation(refusal.ReasonAppID, fmt.Sprintf("this session's attestation names an unknown platform %q", claim.Platform))
	}
}

func judgeIOS(policy AttestationPolicy, claim *AttestationClaim) AttestationJudgement {
	if !policy.AcceptsIOS() {
		return invalidAttestation(refusal.ReasonAppID, "this namespace admits no iOS build")
	}
	if claim.AppID != policy.IOS.TeamID+"."+policy.IOS.BundleID {
		return invalidAttestation(refusal.ReasonAppID, "this session attested another app than the one this namespace admits")
	}
	if claim.Development && !policy.IOS.AllowDevelopment {
		return invalidAttestation(refusal.ReasonEnvironment, "this namespace does not admit development builds")
	}
	if min := policy.IOS.MinBundleVersion; min != "" && claim.AppVersion != "" {
		older, err := bundleVersionOlder(claim.AppVersion, min)
		if err != nil {
			return invalidAttestation(refusal.ReasonAppVersionMalformed, "this session's attested build version cannot be read: "+err.Error())
		}
		if older {
			return updateRequired("build " + claim.AppVersion + " is older than this namespace admits (" + min + "): update the app")
		}
	}
	return admitted()
}

// judgeAndroid applies the policy as it is now to everything an Android
// attestation proved that the policy can narrow after the device attested:
// the package, GrapheneOS, the build, the signing certificates and the OS
// patch level; then the attestation's age.
//
// What the claim cannot show is not admitted. A claim with no signing digest
// (minted from a record kept before they were) and, under a patch window, a
// claim with no patch level must attest again: the new attestation records
// them. A patch level past the window is "attest again" too, not invalid: it
// is the level the device had when it attested, and an attestation made now
// shows the one it has, which the verifier then judges.
func judgeAndroid(policy AttestationPolicy, claim *AttestationClaim, now time.Time) AttestationJudgement {
	if !policy.AcceptsAndroid() {
		return invalidAttestation(refusal.ReasonPackage, "this namespace admits no Android build")
	}
	if claim.AppID != policy.Android.Package {
		return invalidAttestation(refusal.ReasonPackage, "this session attested another app than the one this namespace admits")
	}
	if claim.GrapheneOS && !policy.Android.AllowGrapheneOS {
		return invalidAttestation(refusal.ReasonBootKey, "this namespace does not admit GrapheneOS")
	}
	if min := policy.Android.MinVersionCode; min > 0 {
		code, err := strconv.ParseInt(claim.AppVersion, 10, 64)
		if err != nil {
			return invalidAttestation(refusal.ReasonAppVersionMalformed, "this session's attested versionCode cannot be read")
		}
		if code < min {
			return updateRequired(fmt.Sprintf("versionCode %d is older than this namespace admits (%d): update the app", code, min))
		}
	}
	if len(claim.SigningCertDigests) == 0 {
		return mustAttest("this device's attestation does not record the app's signing certificate: attest it again, then refresh the session")
	}
	for _, d := range claim.SigningCertDigests {
		if !slices.Contains(policy.Android.SigningCertDigests, d) {
			return invalidAttestation(refusal.ReasonSigningCert, "the app is signed with a certificate this namespace does not admit ("+d+")")
		}
	}
	if months := policy.Android.MaxPatchAgeMonths; months > 0 {
		if age, ok := android.PatchAgeMonths(int64(claim.OSPatchLevel), now); !ok || age > int64(months) {
			return mustAttest("this device's attested security patch is older than this namespace accepts: attest it again, then refresh the session")
		}
	}
	if now.After(time.Unix(claim.AttestedAt, 0).Add(policy.ReattestInterval)) {
		return mustAttest("this device's attestation is older than this namespace accepts: attest it again, then refresh the session")
	}
	return admitted()
}

// bundleVersionMaxParts is how many period-separated integers a
// CFBundleVersion has at most.
const bundleVersionMaxParts = 3

// bundleVersionOlder reports whether CFBundleVersion v is an older build than
// min: one to three period-separated integers, compared part by part, a
// missing trailing part reading as zero. It is the order package attest
// applies when it verifies, applied again here because a minimum raised after
// a device attested must reach that device's tokens too.
func bundleVersionOlder(v, min string) (bool, error) {
	a, err := parseBundleVersion(v)
	if err != nil {
		return false, err
	}
	b, err := parseBundleVersion(min)
	if err != nil {
		return false, err
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i], nil
		}
	}
	return false, nil
}

func parseBundleVersion(s string) ([bundleVersionMaxParts]uint64, error) {
	var out [bundleVersionMaxParts]uint64
	parts := strings.Split(s, ".")
	if len(parts) > bundleVersionMaxParts {
		return out, fmt.Errorf("bundle version %q has more than %d parts", s, bundleVersionMaxParts)
	}
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil || part == "" || part[0] == '+' {
			return out, fmt.Errorf("bundle version %q is not period-separated integers", s)
		}
		out[i] = n
	}
	return out, nil
}
