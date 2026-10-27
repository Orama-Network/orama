package attest

import (
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/Orama-Network/orama/core/pkg/attest/android"
	"github.com/Orama-Network/orama/core/pkg/attest/apple"
	"github.com/Orama-Network/orama/core/pkg/attest/refusal"
)

// Apple's launch validation categories (LightweightCodeRequirements
// ValidationCategory) an iOS build may carry.
const (
	categoryTestFlight  uint32 = 2
	categoryDevelopment uint32 = 3
	categoryAppStore    uint32 = 4
)

// New returns the Verifier backed by the platform verifiers. In production:
//
//	attest.New(apple.New(), android.New(revocationRefresher, grapheneRefresher))
func New(ios *apple.Verifier, droid *android.Verifier) Verifier {
	return &verifier{ios: ios, droid: droid}
}

type verifier struct {
	ios   *apple.Verifier
	droid *android.Verifier
}

func (v *verifier) AttestIOS(p Policy, keyID, attestationObject []byte, clientDataHash [32]byte) (Verdict, error) {
	if err := p.ValidateIOS(); err != nil {
		return Verdict{}, err
	}
	a, err := v.ios.VerifyAttestation(p.appleExpect(), keyID, attestationObject, clientDataHash[:])
	if err != nil {
		return Verdict{}, err
	}
	if err := p.checkIOSBuild(a.Extensions); err != nil {
		return Verdict{}, err
	}
	return Verdict{
		Platform:           PlatformIOS,
		AppID:              p.appleAppID(),
		AppVersion:         a.BundleVersion,
		KeyID:              keyID,
		PublicKey:          a.PublicKey,
		Development:        a.Development,
		ValidationCategory: a.ValidationCategory,
		Receipt:            a.Receipt,
	}, nil
}

func (v *verifier) AssertIOS(p Policy, key IOSKey, assertion []byte, clientDataHash [32]byte) (Assertion, error) {
	if err := p.ValidateIOS(); err != nil {
		return Assertion{}, err
	}
	a, err := v.ios.VerifyAssertion(p.appleExpect(), key.PublicKey, key.Counter, assertion, clientDataHash[:])
	if err != nil {
		return Assertion{}, err
	}
	if err := p.checkIOSBuild(a.Extensions); err != nil {
		return Assertion{}, err
	}
	return Assertion{Counter: a.Counter, AppVersion: a.BundleVersion, ValidationCategory: a.ValidationCategory}, nil
}

func (v *verifier) AttestAndroid(p Policy, chain [][]byte, challenge []byte) (Verdict, error) {
	if err := p.ValidateAndroid(); err != nil {
		return Verdict{}, err
	}
	digests, err := p.signingDigests()
	if err != nil {
		return Verdict{}, err
	}
	d, err := v.droid.Verify(chain, android.Expect{
		Challenge:         challenge,
		Package:           p.AndroidPackage,
		SigningDigests:    digests,
		MaxPatchAgeMonths: p.MaxPatchAgeMonths,
	})
	if err != nil {
		return Verdict{}, err
	}
	if p.MinAndroidVersionCode > 0 && d.VersionCode < p.MinAndroidVersionCode {
		return Verdict{}, refusal.UpdateRequired("versionCode %d is below the minimum %d", d.VersionCode, p.MinAndroidVersionCode)
	}
	return Verdict{
		Platform:           PlatformAndroid,
		AppID:              d.Package,
		AppVersion:         strconv.FormatInt(d.VersionCode, 10),
		SecurityLevel:      SecurityLevel(d.SecurityLevel),
		BootState:          BootState(d.BootState),
		BootKey:            d.BootKey,
		GrapheneOS:         d.BootState == android.BootSelfSigned,
		SigningCertDigests: hexDigests(d.SigningDigests),
		OSPatchLevel:       int(d.OSPatchLevel),
		VendorPatchLevel:   int(d.VendorPatchLevel),
		BootPatchLevel:     int(d.BootPatchLevel),
	}, nil
}

// hexDigests writes raw SHA-256 digests the way a policy names them.
func hexDigests(raw [][]byte) []string {
	out := make([]string, len(raw))
	for i, d := range raw {
		out[i] = hex.EncodeToString(d)
	}
	return out
}

func (p Policy) appleExpect() apple.Expect {
	return apple.Expect{AppID: p.appleAppID(), AllowDevelopment: p.AllowDevelopment}
}

// checkIOSBuild applies the policy to what iOS 27 and later sign about the
// build. Nothing signed (iOS 26 and earlier) passes: the build is proved
// genuine, its version unknown.
func (p Policy) checkIOSBuild(ext apple.Extensions) error {
	if ext.ValidationCategory != 0 && !p.iosCategoryAccepted(ext.ValidationCategory) {
		return refusal.Invalid(refusal.ReasonValidationCategory, "the build's validation category is %d", ext.ValidationCategory)
	}
	if p.MinIOSBundleVersion == "" || ext.BundleVersion == "" {
		return nil
	}
	have, err := parseBundleVersion(ext.BundleVersion)
	if err != nil {
		return refusal.Invalid(refusal.ReasonAppVersionMalformed, "%w", err)
	}
	want, err := parseBundleVersion(p.MinIOSBundleVersion)
	if err != nil {
		return fmt.Errorf("minimum iOS bundle version: %w", err)
	}
	if have.less(want) {
		return refusal.UpdateRequired("bundle version %s is below the minimum %s", ext.BundleVersion, p.MinIOSBundleVersion)
	}
	return nil
}

// iosCategoryAccepted: App Store and TestFlight builds always, builds signed
// with a development identity only where development is allowed.
func (p Policy) iosCategoryAccepted(c uint32) bool {
	switch c {
	case categoryAppStore, categoryTestFlight:
		return true
	case categoryDevelopment:
		return p.AllowDevelopment
	default:
		return false
	}
}
