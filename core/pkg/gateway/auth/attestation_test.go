package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/attest"
	"github.com/Orama-Network/orama/core/pkg/attest/refusal"
	"github.com/Orama-Network/orama/core/pkg/client"
)

// attestedDevice is a thumbprint-shaped device id.
const attestedDevice = "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k"

// fakeVerifier stands in for package attest: it records what it was asked and
// answers what the test set.
type fakeVerifier struct {
	verdict   attest.Verdict
	assertion attest.Assertion
	err       error

	gotBinding   []byte
	gotKey       attest.IOSKey
	gotAssertion []byte
	// onAssert runs inside AssertIOS: a concurrent refresh winning the race.
	onAssert func()
}

func (f *fakeVerifier) AttestIOS(_ attest.Policy, _, _ []byte, hash [32]byte) (attest.Verdict, error) {
	f.gotBinding = hash[:]
	return f.verdict, f.err
}

func (f *fakeVerifier) AssertIOS(_ attest.Policy, key attest.IOSKey, assertion []byte, hash [32]byte) (attest.Assertion, error) {
	f.gotKey, f.gotAssertion, f.gotBinding = key, assertion, hash[:]
	if f.onAssert != nil {
		f.onAssert()
	}
	return f.assertion, f.err
}

func (f *fakeVerifier) AttestAndroid(_ attest.Policy, _ [][]byte, challenge []byte) (attest.Verdict, error) {
	f.gotBinding = challenge
	return f.verdict, f.err
}

const testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func androidPolicy(mode AttestationMode) AttestationPolicy {
	return AttestationPolicy{Mode: mode, Android: AndroidAttestation{
		Package: "chat.anchat", SigningCertDigests: []string{testDigest},
	}}
}

func iosPolicy(mode AttestationMode) AttestationPolicy {
	return AttestationPolicy{Mode: mode, IOS: IOSAttestation{TeamID: "TEAM123", BundleID: "chat.anchat"}}
}

// attestingRegistry is a real registry whose "anchat" namespace has policy.
func attestingRegistry(t *testing.T, policy AttestationPolicy) (*Service, *fakeVerifier) {
	t.Helper()
	s, v, _ := attestingRegistryDB(t, policy)
	return s, v
}

func attestingRegistryDB(t *testing.T, policy AttestationPolicy) (*Service, *fakeVerifier, *sqliteDatabase) {
	t.Helper()
	s, db, _ := realRegistry(t)
	if err := s.SetAttestationPolicy(context.Background(), "anchat", policy, "0xowner"); err != nil {
		t.Fatalf("set the policy: %v", err)
	}
	v := &fakeVerifier{}
	s.SetAttestationVerifier(v)
	return s, v, db
}

func TestAttestationPolicy_validate(t *testing.T) {
	ok := []AttestationPolicy{
		{Mode: AttestationOff},
		androidPolicy(AttestationMonitor),
		iosPolicy(AttestationEnforce),
	}
	for _, p := range ok {
		if err := p.Normalize().Validate(); err != nil {
			t.Errorf("%+v refused: %v", p, err)
		}
	}
	bad := map[string]AttestationPolicy{
		"no app":            {Mode: AttestationMonitor},
		"half an iOS app":   {Mode: AttestationEnforce, IOS: IOSAttestation{TeamID: "TEAM123"}},
		"no signing digest": {Mode: AttestationEnforce, Android: AndroidAttestation{Package: "chat.anchat"}},
		"a digest too short": {Mode: AttestationEnforce, Android: AndroidAttestation{
			Package: "chat.anchat", SigningCertDigests: []string{"abcd"}}},
		"graphene without a package": {Mode: AttestationOff, Android: AndroidAttestation{AllowGrapheneOS: true}},
		"an unknown mode":            {Mode: "strict"},
		"a bad iOS minimum": {Mode: AttestationEnforce, IOS: IOSAttestation{
			TeamID: "TEAM123", BundleID: "chat.anchat", MinBundleVersion: "1.x"}},
	}
	for name, p := range bad {
		if err := p.Normalize().Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	interval := androidPolicy(AttestationMonitor)
	interval.ReattestInterval = time.Minute
	if err := interval.Normalize().Validate(); err == nil {
		t.Error("a one-minute re-attestation interval was accepted")
	}
}

// A policy is stored whole, read back the same, judged by this gateway at
// once and by another gateway after its reload; the lobby has none.
func TestSetAttestationPolicy_roundTripsAndReachesEveryGateway(t *testing.T) {
	s, db, _ := realRegistry(t)
	ctx := context.Background()
	if p, err := s.AttestationPolicyOf(ctx, "anchat"); err != nil || p.Mode != AttestationOff {
		t.Fatalf("a namespace with no row: %+v, %v", p, err)
	}
	want := androidPolicy(AttestationEnforce)
	want.Android.SigningCertDigests = []string{" " + strings.ToUpper(testDigest) + " "}
	want.Android.AllowGrapheneOS = true
	want.Android.MinVersionCode = 412
	want.ReattestInterval = 48 * time.Hour
	if err := s.SetAttestationPolicy(ctx, "anchat", want, "0xowner"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.AttestationPolicyOf(ctx, "anchat")
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != AttestationEnforce || got.Android.SigningCertDigests[0] != testDigest ||
		!got.Android.AllowGrapheneOS || got.Android.MinVersionCode != 412 || got.ReattestInterval != 48*time.Hour {
		t.Errorf("read back %+v", got)
	}
	if p, err := s.AttestationPolicyFor("anchat"); err != nil || p.Mode != AttestationEnforce {
		t.Errorf("this gateway judges by %+v, %v", p, err)
	}

	other := createTestService(t)
	other.orm = s.orm
	other.SetRqliteClient(&sqliteRqlite{db: db.db})
	if p, err := other.AttestationPolicyFor("anchat"); err != nil || p.Mode != AttestationEnforce || p.Android.MinVersionCode != 412 {
		t.Errorf("another gateway judges by %+v, %v", p, err)
	}
	if err := s.SetAttestationPolicy(ctx, LobbyNamespace, want, "0xowner"); !errors.Is(err, ErrLobbyHasNoAttestationPolicy) {
		t.Errorf("the lobby took a policy: %v", err)
	}
	if err := s.SetAttestationPolicy(ctx, "anchat", AttestationPolicy{Mode: AttestationEnforce}, "0xowner"); !errors.Is(err, ErrInvalidAttestationPolicy) {
		t.Errorf("an enforce policy with no app: %v", err)
	}
}

// A copy past its staleness bound that cannot be reloaded answers an error,
// never "off".
func TestAttestationPolicies_aStaleUnreadableCopyIsNotOff(t *testing.T) {
	p := NewAttestationPolicies(func() client.DatabaseClient { return nil }, nil)
	p.reloadTimeout = 50 * time.Millisecond
	if _, err := p.For("anchat"); !errors.Is(err, ErrAttestationPoliciesUnavailable) {
		t.Errorf("an unreadable registry answered %v", err)
	}
}

func TestJudgeAttestation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0) // 2027-01
	droid := androidPolicy(AttestationEnforce).Normalize()
	droid.Android.MinVersionCode = 400
	patched := droid
	patched.Android.MaxPatchAgeMonths = 6
	apple := iosPolicy(AttestationEnforce).Normalize()
	apple.IOS.MinBundleVersion = "2.1"
	fresh := now.Add(-time.Hour).Unix()
	signed := []string{testDigest}
	android := func(edit func(*AttestationClaim)) *AttestationClaim {
		c := &AttestationClaim{Platform: "android", AppID: "chat.anchat", AppVersion: "412",
			SigningCertDigests: signed, OSPatchLevel: 202612, AttestedAt: fresh}
		if edit != nil {
			edit(c)
		}
		return c
	}
	cases := []struct {
		name   string
		policy AttestationPolicy
		claim  *AttestationClaim
		want   AttestationRefusal
		reason refusal.Reason
	}{
		{"off judges nothing", AttestationPolicy{Mode: AttestationOff}, nil, AttestationAdmitted, ""},
		{"no attestation", droid, nil, AttestationRequired, ""},
		{"a genuine android build", droid, android(nil), AttestationAdmitted, ""},
		{"monitor judges as enforce does", androidPolicy(AttestationMonitor).Normalize(), android(func(c *AttestationClaim) { c.AppID = "evil.anchat" }), AttestationInvalid, refusal.ReasonPackage},
		{"another package", droid, android(func(c *AttestationClaim) { c.AppID = "evil.anchat" }), AttestationInvalid, refusal.ReasonPackage},
		{"an old build", droid, android(func(c *AttestationClaim) { c.AppVersion = "399" }), AttestationUpdateRequired, refusal.ReasonAppVersion},
		{"an attestation past the interval", droid, android(func(c *AttestationClaim) {
			c.AttestedAt = now.Add(-DefaultReattestInterval - time.Minute).Unix()
		}), AttestationRequired, ""},
		{"grapheneos not admitted", droid, android(func(c *AttestationClaim) { c.GrapheneOS = true }), AttestationInvalid, refusal.ReasonBootKey},
		// feat-965: a signing certificate removed from the policy reaches a
		// device that attested with it, and a record that never kept its
		// certificate is not taken on trust.
		{"a signing certificate the policy no longer names", droid, android(func(c *AttestationClaim) {
			c.SigningCertDigests = []string{strings.Repeat("ab", 32)}
		}), AttestationInvalid, refusal.ReasonSigningCert},
		{"one of two certificates no longer named", droid, android(func(c *AttestationClaim) {
			c.SigningCertDigests = []string{testDigest, strings.Repeat("ab", 32)}
		}), AttestationInvalid, refusal.ReasonSigningCert},
		{"a record that kept no certificate", droid, android(func(c *AttestationClaim) { c.SigningCertDigests = nil }), AttestationRequired, ""},
		{"a patch inside the window", patched, android(nil), AttestationAdmitted, ""},
		{"a patch past a narrowed window", patched, android(func(c *AttestationClaim) { c.OSPatchLevel = 202605 }), AttestationRequired, ""},
		{"a record that kept no patch level, under a window", patched, android(func(c *AttestationClaim) { c.OSPatchLevel = 0 }), AttestationRequired, ""},
		{"no patch level and no window", droid, android(func(c *AttestationClaim) { c.OSPatchLevel = 0 }), AttestationAdmitted, ""},
		{"ios on an android-only namespace", droid, &AttestationClaim{Platform: "ios", AppID: "TEAM123.chat.anchat", AttestedAt: fresh}, AttestationInvalid, refusal.ReasonAppID},
		{"a genuine ios build", apple, &AttestationClaim{Platform: "ios", AppID: "TEAM123.chat.anchat", AppVersion: "2.1.3", AttestedAt: fresh}, AttestationAdmitted, ""},
		{"ios 26 signs no version", apple, &AttestationClaim{Platform: "ios", AppID: "TEAM123.chat.anchat", AttestedAt: fresh}, AttestationAdmitted, ""},
		{"an old ios build", apple, &AttestationClaim{Platform: "ios", AppID: "TEAM123.chat.anchat", AppVersion: "2.0.9", AttestedAt: fresh}, AttestationUpdateRequired, refusal.ReasonAppVersion},
		{"a development key", apple, &AttestationClaim{Platform: "ios", AppID: "TEAM123.chat.anchat", Development: true, AttestedAt: fresh}, AttestationInvalid, refusal.ReasonEnvironment},
		{"an unknown platform", apple, &AttestationClaim{Platform: "symbian", AttestedAt: fresh}, AttestationInvalid, refusal.ReasonAppID},
	}
	for _, c := range cases {
		got := JudgeAttestation(c.policy, c.claim, now)
		if got.Refusal != c.want || got.Reason != c.reason {
			t.Errorf("%s: %s/%q (%s), want %s/%q", c.name, got.Refusal, got.Reason, got.Message, c.want, c.reason)
		}
		if (got.Refusal == AttestationAdmitted) != (got.Message == "") {
			t.Errorf("%s: %s with message %q", c.name, got.Refusal, got.Message)
		}
	}
}

// A challenge is for one device, once, and a device holds only a handful.
func TestAttestationChallenge_isSingleUseAndForOneDevice(t *testing.T) {
	s, _ := attestingRegistry(t, androidPolicy(AttestationMonitor))
	ctx := context.Background()
	challenge, expires, err := s.IssueAttestationChallenge(ctx, "anchat", attestedDevice)
	if err != nil {
		t.Fatal(err)
	}
	if until := time.Until(expires); until <= 0 || until > AttestationChallengeTTL {
		t.Errorf("the challenge expires in %s", until)
	}
	_, nsID, _ := s.deviceRegistry(ctx, "anchat")
	if err := s.consumeAttestationChallenge(ctx, nsID, "another-device", challenge); !errors.Is(err, ErrNonceInvalid) {
		t.Errorf("another device consumed it: %v", err)
	}
	if err := s.consumeAttestationChallenge(ctx, nsID, attestedDevice, challenge); err != nil {
		t.Fatalf("its device could not: %v", err)
	}
	if err := s.consumeAttestationChallenge(ctx, nsID, attestedDevice, challenge); !errors.Is(err, ErrNonceInvalid) {
		t.Errorf("it was consumed twice: %v", err)
	}
	for range maxOutstandingNonces {
		if _, _, err := s.IssueAttestationChallenge(ctx, "anchat", attestedDevice); err != nil {
			t.Fatal(err)
		}
	}
	var tooMany *ErrTooManyOutstandingNonces
	if _, _, err := s.IssueAttestationChallenge(ctx, "anchat", attestedDevice); !errors.As(err, &tooMany) {
		t.Errorf("past the limit: %v", err)
	}
	if _, _, err := s.IssueAttestationChallenge(ctx, "anchat", ""); !errors.Is(err, ErrAttestationNeedsDevice) {
		t.Errorf("a session with no device got a challenge: %v", err)
	}
}

func androidVerdict(bootKey byte) attest.Verdict {
	return attest.Verdict{Platform: attest.PlatformAndroid, AppID: "chat.anchat", AppVersion: "412",
		BootState: attest.BootVerified, BootKey: []byte{bootKey, 2, 3},
		SigningCertDigests: []string{testDigest}, OSPatchLevel: 202609}
}

// feat-965: the record keeps the signing certificates and the patch level the
// device attested, every refresh's token carries them, and a certificate
// removed from the policy afterwards refuses that token — in monitor as in
// enforce — without the device attesting again.
func TestAttestAndroid_aNarrowedPolicyReachesTheRefreshedToken(t *testing.T) {
	s, v := attestingRegistry(t, androidPolicy(AttestationMonitor))
	ctx := context.Background()
	v.verdict = androidVerdict(1)
	challenge, _, _ := s.IssueAttestationChallenge(ctx, "anchat", attestedDevice)
	if _, err := s.AttestAndroid(ctx, "anchat", attestedDevice, challenge, [][]byte{{1}}); err != nil {
		t.Fatalf("attest: %v", err)
	}
	att, err := s.attestationForToken(ctx, "anchat", attestedDevice, nil)
	if err != nil || att == nil {
		t.Fatalf("the refresh carried %+v, %v", att, err)
	}
	if !reflect.DeepEqual(att.SigningCertDigests, []string{testDigest}) || att.OSPatchLevel != 202609 {
		t.Errorf("the refresh carried digests %v and patch level %d", att.SigningCertDigests, att.OSPatchLevel)
	}
	before, _ := s.AttestationPolicyFor("anchat")
	if got := JudgeAttestation(before, att, time.Now()); got.Refusal != AttestationAdmitted {
		t.Fatalf("the policy it attested under refuses it: %+v", got)
	}

	rotated := androidPolicy(AttestationMonitor)
	rotated.Android.SigningCertDigests = []string{strings.Repeat("ab", 32)}
	if err := s.SetAttestationPolicy(ctx, "anchat", rotated, "0xowner"); err != nil {
		t.Fatal(err)
	}
	after, _ := s.AttestationPolicyFor("anchat")
	att, err = s.attestationForToken(ctx, "anchat", attestedDevice, nil)
	if err != nil || att == nil {
		t.Fatalf("the refresh after the rotation carried %+v, %v", att, err)
	}
	if got := JudgeAttestation(after, att, time.Now()); got.Refusal != AttestationInvalid || got.Reason != refusal.ReasonSigningCert {
		t.Errorf("a token of the removed certificate is judged %+v", got)
	}
}

// A record kept before migration 094 has no signing digest: its token is
// judged "attest again", never admitted on trust, and the next attestation
// records the digests.
func TestAttestationForToken_aRecordWithoutDigestsMustAttestAgain(t *testing.T) {
	s, v, db := attestingRegistryDB(t, androidPolicy(AttestationEnforce))
	ctx := context.Background()
	if _, err := db.db.Exec(`INSERT INTO device_attestations(namespace_id, device_id, platform, app_id, app_version, attested_at)
		VALUES (10, ?, 'android', 'chat.anchat', '412', datetime('now'))`, attestedDevice); err != nil {
		t.Fatalf("seed a row as migration 091 wrote it: %v", err)
	}
	att, err := s.attestationForToken(ctx, "anchat", attestedDevice, nil)
	if err != nil || att == nil {
		t.Fatalf("the refresh carried %+v, %v", att, err)
	}
	policy, _ := s.AttestationPolicyFor("anchat")
	if got := JudgeAttestation(policy, att, time.Now()); got.Refusal != AttestationRequired {
		t.Errorf("a record with no digest is judged %+v", got)
	}

	v.verdict = androidVerdict(0)
	v.verdict.BootKey = nil
	challenge, _, _ := s.IssueAttestationChallenge(ctx, "anchat", attestedDevice)
	if _, err := s.AttestAndroid(ctx, "anchat", attestedDevice, challenge, [][]byte{{1}}); err != nil {
		t.Fatalf("attest again: %v", err)
	}
	att, err = s.attestationForToken(ctx, "anchat", attestedDevice, nil)
	if err != nil || att == nil {
		t.Fatalf("the refresh after attesting again carried %+v, %v", att, err)
	}
	if got := JudgeAttestation(policy, att, time.Now()); got.Refusal != AttestationAdmitted {
		t.Errorf("after attesting again the token is judged %+v", got)
	}
}

// The proof is bound to the challenge and the device; what it proved is
// recorded, the boot key pinned, and the next token of the device carries it.
func TestAttestAndroid_bindsRecordsAndPins(t *testing.T) {
	s, v := attestingRegistry(t, androidPolicy(AttestationEnforce))
	ctx := context.Background()
	v.verdict = androidVerdict(1)

	challenge, _, _ := s.IssueAttestationChallenge(ctx, "anchat", attestedDevice)
	claim, err := s.AttestAndroid(ctx, "anchat", attestedDevice, challenge, [][]byte{{1}})
	if err != nil {
		t.Fatalf("attest: %v", err)
	}
	want := sha256.Sum256([]byte(challenge + attestedDevice))
	if string(v.gotBinding) != string(want[:]) {
		t.Error("the verifier was not handed SHA-256(challenge || device id)")
	}
	if claim.Platform != "android" || claim.AppID != "chat.anchat" || claim.AppVersion != "412" {
		t.Errorf("claim %+v", claim)
	}
	if _, err := s.AttestAndroid(ctx, "anchat", attestedDevice, challenge, [][]byte{{1}}); !errors.Is(err, ErrNonceInvalid) {
		t.Errorf("the challenge was answered twice: %v", err)
	}

	att, err := s.attestationForToken(ctx, "anchat", attestedDevice, nil)
	if err != nil || att == nil || att.AppID != "chat.anchat" {
		t.Errorf("the device's next token carries %+v, %v", att, err)
	}

	v.verdict = androidVerdict(9)
	again, _, _ := s.IssueAttestationChallenge(ctx, "anchat", attestedDevice)
	if _, err := s.AttestAndroid(ctx, "anchat", attestedDevice, again, [][]byte{{1}}); attest.ReasonOf(err) != refusal.ReasonBootKey {
		t.Errorf("an OS signed with another key was accepted: %v", err)
	}
}

func TestAttestAndroid_refusals(t *testing.T) {
	ctx := context.Background()

	s, v := attestingRegistry(t, androidPolicy(AttestationEnforce))
	verdict := androidVerdict(1)
	verdict.GrapheneOS = true
	v.verdict = verdict
	challenge, _, _ := s.IssueAttestationChallenge(ctx, "anchat", attestedDevice)
	if _, err := s.AttestAndroid(ctx, "anchat", attestedDevice, challenge, [][]byte{{1}}); attest.KindOf(err) != attest.KindInvalid {
		t.Errorf("GrapheneOS was accepted where the policy does not admit it: %v", err)
	}

	v.err = refusal.Invalid(refusal.ReasonSigningCert, "re-signed")
	challenge, _, _ = s.IssueAttestationChallenge(ctx, "anchat", attestedDevice)
	if _, err := s.AttestAndroid(ctx, "anchat", attestedDevice, challenge, [][]byte{{1}}); attest.ReasonOf(err) != refusal.ReasonSigningCert {
		t.Errorf("the verifier's refusal was not passed on: %v", err)
	}

	if _, err := s.AttestIOS(ctx, "anchat", attestedDevice, challenge, []byte{1}, []byte{1}); attest.KindOf(err) != attest.KindInvalid {
		t.Errorf("an iOS proof to an Android-only namespace: %v", err)
	}

	off, _ := attestingRegistry(t, AttestationPolicy{Mode: AttestationOff})
	if _, err := off.AttestAndroid(ctx, "anchat", attestedDevice, "c", [][]byte{{1}}); !errors.Is(err, ErrAttestationNotConfigured) {
		t.Errorf("a namespace that does not attest: %v", err)
	}
}

// An iOS device proves itself again on every refresh: its assertion is
// checked against the stored key, its counter moves once, and a replay of the
// same counter leaves the token without an attestation.
func TestAttestationForToken_iosAssertsOncePerRefresh(t *testing.T) {
	s, v, db := attestingRegistryDB(t, iosPolicy(AttestationEnforce))
	ctx := context.Background()
	v.verdict = attest.Verdict{Platform: attest.PlatformIOS, AppID: "TEAM123.chat.anchat",
		KeyID: []byte("key"), PublicKey: []byte("public")}
	challenge, _, _ := s.IssueAttestationChallenge(ctx, "anchat", attestedDevice)
	if _, err := s.AttestIOS(ctx, "anchat", attestedDevice, challenge, []byte("key"), []byte("object")); err != nil {
		t.Fatalf("attest: %v", err)
	}

	if att, err := s.attestationForToken(ctx, "anchat", attestedDevice, nil); err != nil || att != nil {
		t.Errorf("an iOS refresh with no assertion carried %+v, %v", att, err)
	}

	v.assertion = attest.Assertion{Counter: 1, AppVersion: "2.3"}
	proof := &DeviceProof{ID: "proof-id-0123456789", AppAttest: "YXNzZXJ0aW9u"}
	att, err := s.attestationForToken(ctx, "anchat", attestedDevice, proof)
	if err != nil || att == nil || att.AppVersion != "2.3" {
		t.Fatalf("a valid assertion carried %+v, %v", att, err)
	}
	want := sha256.Sum256([]byte(proof.ID + attestedDevice))
	if string(v.gotBinding) != string(want[:]) || string(v.gotKey.PublicKey) != "public" || v.gotKey.Counter != 0 {
		t.Errorf("the assertion was checked against %+v, binding %x", v.gotKey, v.gotBinding)
	}

	// Two refreshes verified against the same counter: the one whose
	// compare-and-swap comes second loses, and its token carries nothing.
	v.assertion = attest.Assertion{Counter: 2}
	v.onAssert = func() {
		if _, err := db.db.Exec(`UPDATE device_attestations SET ios_counter = 2`); err != nil {
			t.Fatal(err)
		}
	}
	if att, err := s.attestationForToken(ctx, "anchat", attestedDevice, proof); err != nil || att != nil {
		t.Errorf("the losing assertion carried %+v, %v", att, err)
	}
}

// The token minted for an attestation carries it, and a verified token reads
// it back; an attestation without a device is never minted.
func TestReissueAttested_roundTripsTheClaim(t *testing.T) {
	s := createTestService(t)
	att := &AttestationClaim{Platform: "android", AppID: "chat.anchat", AppVersion: "412",
		SigningCertDigests: []string{testDigest}, OSPatchLevel: 202609, AttestedAt: time.Now().Unix()}
	token, _, err := s.ReissueAttested(&JWTClaims{Sub: "0xwallet", Namespace: "anchat", Did: attestedDevice, Sid: "s"}, att)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := s.ParseAndVerifyJWT(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Att == nil || !reflect.DeepEqual(claims.Att, att) || claims.Did != attestedDevice || claims.Sid != "s" {
		t.Errorf("read back %+v / %+v", claims, claims.Att)
	}
	if _, _, err := s.ReissueAttested(&JWTClaims{Sub: "0xwallet", Namespace: "anchat"}, att); !errors.Is(err, ErrAttestationNeedsDevice) {
		t.Errorf("an attestation without a device: %v", err)
	}
}
