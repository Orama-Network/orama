package serverless

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/serverless"
)

const judgeDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// policyMap is an AttestationPolicySource from a map; a namespace not in it
// cannot be read.
type policyMap map[string]auth.AttestationPolicy

func (m policyMap) AttestationPolicyFor(namespace string) (auth.AttestationPolicy, error) {
	p, ok := m[namespace]
	if !ok {
		return auth.AttestationPolicy{}, auth.ErrAttestationPoliciesUnavailable
	}
	return p, nil
}

// What a function is handed carries everything the policy judges: a token's
// claim survives the trip into the runtime and back.
func TestCallerAttestation_roundTripsTheClaim(t *testing.T) {
	claim := &auth.AttestationClaim{Platform: "android", AppID: "chat.anchat", AppVersion: "412",
		GrapheneOS: true, SigningCertDigests: []string{judgeDigest}, OSPatchLevel: 202609, AttestedAt: 1_790_000_000}
	if got := attestationClaim(callerAttestation(claim)); !reflect.DeepEqual(got, claim) {
		t.Errorf("round trip %+v, want %+v", got, claim)
	}
	ios := &auth.AttestationClaim{Platform: "ios", AppID: "TEAM.chat.anchat", Development: true, AttestedAt: 1}
	if got := attestationClaim(callerAttestation(ios)); !reflect.DeepEqual(got, ios) {
		t.Errorf("round trip %+v, want %+v", got, ios)
	}
	if callerAttestation(nil) != nil || attestationClaim(nil) != nil {
		t.Error("no attestation became one")
	}
}

// The verdict is the gateway's judgement under the current policy, in every
// mode: monitor answers what enforcing would decide.
func TestAttestationJudge_judgesUnderTheCurrentPolicyInEveryMode(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	android := func(mode auth.AttestationMode, digests ...string) auth.AttestationPolicy {
		return auth.AttestationPolicy{Mode: mode, ReattestInterval: auth.DefaultReattestInterval,
			Android: auth.AndroidAttestation{Package: "chat.anchat", SigningCertDigests: digests}}
	}
	policies := policyMap{
		"off":     {Mode: auth.AttestationOff, ReattestInterval: auth.DefaultReattestInterval},
		"monitor": android(auth.AttestationMonitor, judgeDigest),
		"enforce": android(auth.AttestationEnforce, judgeDigest),
		"rotated": android(auth.AttestationMonitor, strings.Repeat("ab", 32)),
	}
	judge := AttestationJudge{Policies: policies, Now: func() time.Time { return now }}
	att := &serverless.CallerAttestation{Platform: "android", AppID: "chat.anchat", AppVersion: "412",
		SigningCertDigests: []string{judgeDigest}, AttestedAt: now.Add(-time.Hour).Unix()}
	stale := *att
	stale.AttestedAt = now.Add(-auth.DefaultReattestInterval - time.Minute).Unix()

	cases := []struct {
		name, namespace string
		att             *serverless.CallerAttestation
		verdict, reason string
	}{
		{"off admits nobody as attested", "off", att, serverless.VerdictNotConfigured, ""},
		{"monitor admits a genuine build", "monitor", att, serverless.VerdictAdmitted, ""},
		{"enforce admits a genuine build", "enforce", att, serverless.VerdictAdmitted, ""},
		{"monitor judges a caller who never attested", "monitor", nil, serverless.VerdictAttestationRequired, ""},
		{"monitor judges an attestation past the interval", "monitor", &stale, serverless.VerdictAttestationRequired, ""},
		{"a signing certificate removed from the policy", "rotated", att, serverless.VerdictAttestationInvalid, "signing_cert"},
	}
	for _, c := range cases {
		got, err := judge.AttestationVerdict(c.namespace, c.att)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.Verdict != c.verdict || got.Reason != c.reason || got.Mode != string(policies[c.namespace].Mode) {
			t.Errorf("%s: %+v, want %s/%q", c.name, got, c.verdict, c.reason)
		}
		if (got.Verdict == serverless.VerdictAdmitted) != (got.Message == "") {
			t.Errorf("%s: %s with message %q", c.name, got.Verdict, got.Message)
		}
	}
	if _, err := judge.AttestationVerdict("unreadable", att); !errors.Is(err, auth.ErrAttestationPoliciesUnavailable) {
		t.Errorf("an unreadable policy answered %v", err)
	}
}
