package attest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/attest/android"
	"github.com/Orama-Network/orama/core/pkg/attest/apple"
)

type noRevocations struct{}

func (noRevocations) Revoked(string) (bool, error) { return false, nil }

type noKeys struct{}

func (noKeys) Contains([]byte) bool { return false }

func verifierAt(when time.Time) Verifier {
	ios := apple.New()
	ios.Now = func() time.Time { return when }
	droid := android.New(noRevocations{}, noKeys{})
	droid.Now = func() time.Time { return when }
	return New(ios, droid)
}

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAttestIOS_refusesApplesSampleForItsOSExecutableCategory(t *testing.T) {
	obj, err := os.ReadFile("apple/testdata/apple_guide_attestation.cbor")
	if err != nil {
		t.Fatal(err)
	}
	// The sample's clientDataHash is the raw 24-byte challenge, which this
	// API's fixed 32 bytes cannot carry; the category refusal comes after
	// the nonce check, so check it through the platform verifier.
	ios := apple.New()
	ios.Now = func() time.Time { return time.Date(2026, 4, 21, 18, 13, 12, 0, time.UTC) }
	a, err := ios.VerifyAttestation(apple.Expect{AppID: "1234567890.com.example.myapp"}, b64(t, "zgSY9YSD+7TaDXssY6WlOPVS1K3Lmk+pFhlcSWE+ZV0="), obj, []byte("example_server_challenge"))
	if err != nil {
		t.Fatal(err)
	}
	p := Policy{AppleTeamID: "1234567890", AppleBundleID: "com.example.myapp"}
	err = p.checkIOSBuild(a.Extensions)
	if KindOf(err) != KindInvalid || ReasonOf(err) != "validation_category" {
		t.Fatalf("category 1 (an OS executable): %v", err)
	}
}

func TestAttestAndAssertIOS_realIOS14DeviceHasNoSignedVersion(t *testing.T) {
	raw, err := os.ReadFile("apple/testdata/ios14_4.json")
	if err != nil {
		t.Fatal(err)
	}
	var vec struct {
		Attestation struct {
			AttestationBase64, TeamIdentifier, BundleIdentifier, Timestamp string
			KeyIDBase64                                                    string `json:"keyIdBase64"`
			ClientDataHash                                                 string `json:"clientDataHashSha256Base64"`
		}
		Assertion struct {
			AssertionBase64 string
			ClientDataHash  string `json:"clientDataHashSha256Base64"`
		}
	}
	if err := json.Unmarshal(raw, &vec); err != nil {
		t.Fatal(err)
	}
	when, _ := time.Parse(time.RFC3339, vec.Attestation.Timestamp)
	v := verifierAt(when)
	p := Policy{
		AppleTeamID: vec.Attestation.TeamIdentifier, AppleBundleID: vec.Attestation.BundleIdentifier,
		AllowDevelopment: true, MinIOSBundleVersion: "99",
	}
	var cdh [32]byte
	copy(cdh[:], b64(t, vec.Attestation.ClientDataHash))
	got, err := v.AttestIOS(p, b64(t, vec.Attestation.KeyIDBase64), b64(t, vec.Attestation.AttestationBase64), cdh)
	if err != nil {
		t.Fatalf("an iOS 14 build is accepted with its version unknown: %v", err)
	}
	if got.Platform != PlatformIOS || got.AppVersion != "" || !got.Development || got.AppID != p.appleAppID() {
		t.Fatalf("%+v", got)
	}

	copy(cdh[:], b64(t, vec.Assertion.ClientDataHash))
	a, err := v.AssertIOS(p, IOSKey{PublicKey: got.PublicKey, Counter: got.Counter}, b64(t, vec.Assertion.AssertionBase64), cdh)
	if err != nil || a.Counter != 1 || a.AppVersion != "" {
		t.Fatalf("assertion %+v, %v", a, err)
	}
	if _, err := v.AssertIOS(p, IOSKey{PublicKey: got.PublicKey, Counter: a.Counter}, b64(t, vec.Assertion.AssertionBase64), cdh); KindOf(err) != KindInvalid || ReasonOf(err) != "counter" {
		t.Fatalf("a replayed assertion: %v", err)
	}
}

func androidVector(t *testing.T) (chain [][]byte, challenge []byte, digest string, created time.Time) {
	t.Helper()
	raw, err := os.ReadFile("android/testdata/caiman_sdk36_TEE_EC_RKP.pem")
	if err != nil {
		t.Fatal(err)
	}
	for {
		var block *pem.Block
		block, raw = pem.Decode(raw)
		if block == nil {
			break
		}
		chain = append(chain, block.Bytes)
	}
	sig := b64(t, "EDk47kU35Z6O55L2VFBPuDRvxrNG0LvEQV/DOfz8jsE=")
	return chain, []byte("d688d763-6118-4ca6-94b2-e6cd9ed7e4e4"), hex.EncodeToString(sig), time.UnixMilli(1758900680964)
}

func TestAttestAndroid_realPixelAndTheVersionGate(t *testing.T) {
	chain, challenge, digest, created := androidVector(t)
	v := verifierAt(created)
	p := Policy{AndroidPackage: "com.google.android.attestation", AndroidSigningCertDigests: []string{digest}}
	got, err := v.AttestAndroid(p, chain, challenge)
	if err != nil {
		t.Fatal(err)
	}
	if got.Platform != PlatformAndroid || got.AppVersion != "0" || got.BootState != BootVerified || got.GrapheneOS || got.SecurityLevel != SecurityTEE || got.OSPatchLevel != 202511 {
		t.Fatalf("%+v", got)
	}
	// The gateway records the attested signing digests to judge them again
	// when the policy changes (feat-965): they are reported as the policy
	// names them.
	if len(got.SigningCertDigests) != 1 || got.SigningCertDigests[0] != digest {
		t.Errorf("signing digests %v, want [%s]", got.SigningCertDigests, digest)
	}

	p.MinAndroidVersionCode = 1
	_, err = v.AttestAndroid(p, chain, challenge)
	if KindOf(err) != KindAppUpdateRequired || ReasonOf(err) != "app_version" {
		t.Fatalf("versionCode 0 under a minimum of 1: %v", err)
	}
}

func TestVerifier_refusesAnIncompletePolicyAsTheServersError(t *testing.T) {
	chain, challenge, _, created := androidVector(t)
	v := verifierAt(created)
	_, err := v.AttestAndroid(Policy{AndroidPackage: "com.google.android.attestation"}, chain, challenge)
	if err == nil || KindOf(err) != KindNone {
		t.Fatalf("a policy without signing digests: %v", err)
	}
	_, err = v.AttestIOS(Policy{}, nil, nil, [32]byte{})
	if err == nil || KindOf(err) != KindNone {
		t.Fatalf("a policy without an App ID: %v", err)
	}
}

func TestCheckIOSBuild_categoriesAndMinimumVersion(t *testing.T) {
	base := Policy{AppleTeamID: "T", AppleBundleID: "b"}
	dev := base
	dev.AllowDevelopment = true
	min := base
	min.MinIOSBundleVersion = "1.4"
	for _, c := range []struct {
		name string
		p    Policy
		ext  apple.Extensions
		want Kind
	}{
		{"App Store", base, apple.Extensions{ValidationCategory: 4}, KindNone},
		{"TestFlight", base, apple.Extensions{ValidationCategory: 2}, KindNone},
		{"development build on production", base, apple.Extensions{ValidationCategory: 3}, KindInvalid},
		{"development build on devnet", dev, apple.Extensions{ValidationCategory: 3}, KindNone},
		{"ad-hoc", dev, apple.Extensions{ValidationCategory: 5}, KindInvalid},
		{"Developer ID", dev, apple.Extensions{ValidationCategory: 6}, KindInvalid},
		{"nothing signed (iOS 26)", min, apple.Extensions{}, KindNone},
		{"at the minimum", min, apple.Extensions{BundleVersion: "1.4.0", ValidationCategory: 4}, KindNone},
		{"above the minimum", min, apple.Extensions{BundleVersion: "1.10", ValidationCategory: 4}, KindNone},
		{"below the minimum", min, apple.Extensions{BundleVersion: "1.3.9", ValidationCategory: 4}, KindAppUpdateRequired},
		{"signed version not numeric", min, apple.Extensions{BundleVersion: "1.4-beta", ValidationCategory: 4}, KindInvalid},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.p.checkIOSBuild(c.ext)
			if KindOf(err) != c.want || (c.want == KindNone && err != nil) {
				t.Fatalf("got %v (%s), want %s", err, KindOf(err), c.want)
			}
		})
	}
}

func TestPolicy_validate(t *testing.T) {
	digest := hex.EncodeToString(make([]byte, sha256.Size))
	if err := (Policy{AppleTeamID: "T", AppleBundleID: "b", MinIOSBundleVersion: "2.1.0"}).ValidateIOS(); err != nil {
		t.Errorf("a complete iOS policy: %v", err)
	}
	if err := (Policy{AndroidPackage: "p", AndroidSigningCertDigests: []string{digest}, MinAndroidVersionCode: 3, MaxPatchAgeMonths: 6}).ValidateAndroid(); err != nil {
		t.Errorf("a complete Android policy: %v", err)
	}
	for name, err := range map[string]error{
		"no team id":               Policy{AppleBundleID: "b"}.ValidateIOS(),
		"team id with a period":    Policy{AppleTeamID: "T.x", AppleBundleID: "b"}.ValidateIOS(),
		"four-part minimum":        Policy{AppleTeamID: "T", AppleBundleID: "b", MinIOSBundleVersion: "1.2.3.4"}.ValidateIOS(),
		"non-numeric minimum":      Policy{AppleTeamID: "T", AppleBundleID: "b", MinIOSBundleVersion: "1.x"}.ValidateIOS(),
		"empty part":               Policy{AppleTeamID: "T", AppleBundleID: "b", MinIOSBundleVersion: "1..2"}.ValidateIOS(),
		"no package":               Policy{AndroidSigningCertDigests: []string{digest}}.ValidateAndroid(),
		"no digest":                Policy{AndroidPackage: "p"}.ValidateAndroid(),
		"uppercase digest":         Policy{AndroidPackage: "p", AndroidSigningCertDigests: []string{"AB" + digest[2:]}}.ValidateAndroid(),
		"short digest":             Policy{AndroidPackage: "p", AndroidSigningCertDigests: []string{"abcd"}}.ValidateAndroid(),
		"negative minimum version": Policy{AndroidPackage: "p", AndroidSigningCertDigests: []string{digest}, MinAndroidVersionCode: -1}.ValidateAndroid(),
		"negative patch window":    Policy{AndroidPackage: "p", AndroidSigningCertDigests: []string{digest}, MaxPatchAgeMonths: -1}.ValidateAndroid(),
	} {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestKindOf_andReasonOf(t *testing.T) {
	if KindOf(errors.New("plain")) != KindNone || ReasonOf(errors.New("plain")) != "" {
		t.Fatal("a plain error is not a refusal")
	}
	if KindOf(nil) != KindNone {
		t.Fatal("nil is not a refusal")
	}
}
