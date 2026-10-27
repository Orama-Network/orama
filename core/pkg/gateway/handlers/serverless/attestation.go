package serverless

import (
	"time"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/serverless"
)

// A function reads a caller's device attestation two ways (feat-941,
// feat-965): what the token's `att` claim proved (get_caller_attestation),
// and the gateway's verdict on it under the namespace's policy as it is when
// the function asks (get_caller_attestation_verdict). The claim crosses into
// the function runtime as a serverless.CallerAttestation, which carries
// everything the policy judges, so the verdict is the gateway's own
// auth.JudgeAttestation applied to it; the two conversions below are each
// other's inverse.

// callerAttestation is a token's attestation as a function reads it.
func callerAttestation(att *auth.AttestationClaim) *serverless.CallerAttestation {
	if att == nil {
		return nil
	}
	return &serverless.CallerAttestation{
		Platform:           att.Platform,
		AppID:              att.AppID,
		AppVersion:         att.AppVersion,
		AttestedAt:         att.AttestedAt,
		Development:        att.Development,
		GrapheneOS:         att.GrapheneOS,
		SigningCertDigests: att.SigningCertDigests,
		OSPatchLevel:       att.OSPatchLevel,
	}
}

// attestationClaim is a function caller's attestation as the policy judges it.
func attestationClaim(att *serverless.CallerAttestation) *auth.AttestationClaim {
	if att == nil {
		return nil
	}
	return &auth.AttestationClaim{
		Platform:           att.Platform,
		AppID:              att.AppID,
		AppVersion:         att.AppVersion,
		AttestedAt:         att.AttestedAt,
		Development:        att.Development,
		GrapheneOS:         att.GrapheneOS,
		SigningCertDigests: att.SigningCertDigests,
		OSPatchLevel:       att.OSPatchLevel,
	}
}

// attestationVerdicts names each refusal as a function reads it.
var attestationVerdicts = map[auth.AttestationRefusal]string{
	auth.AttestationAdmitted:       serverless.VerdictAdmitted,
	auth.AttestationRequired:       serverless.VerdictAttestationRequired,
	auth.AttestationUpdateRequired: serverless.VerdictAppUpdateRequired,
	auth.AttestationInvalid:        serverless.VerdictAttestationInvalid,
}

// AttestationPolicySource is the gateway's copy of every namespace's
// attestation policy; *auth.Service is the one the gateway uses.
type AttestationPolicySource interface {
	AttestationPolicyFor(namespace string) (auth.AttestationPolicy, error)
}

// AttestationJudge answers get_caller_attestation_verdict with the policy the
// gateway judges requests by: its in-memory copy, a map lookup, so a function
// may ask on every call.
type AttestationJudge struct {
	Policies AttestationPolicySource
	// Now is the clock the re-attestation interval and patch window are
	// measured on.
	Now func() time.Time
}

// NewAttestationJudge returns the judge for the gateway's policies.
func NewAttestationJudge(policies AttestationPolicySource) AttestationJudge {
	return AttestationJudge{Policies: policies, Now: time.Now}
}

// AttestationVerdict judges att, nil for a caller who carries none, against
// namespace's policy in whatever mode it is: monitor is judged as enforce
// would judge it. A namespace whose mode is off has no app to admit a caller
// as, and answers VerdictNotConfigured. An error is a policy that cannot be
// read now, never a verdict on the caller.
func (j AttestationJudge) AttestationVerdict(namespace string, att *serverless.CallerAttestation) (serverless.AttestationVerdict, error) {
	policy, err := j.Policies.AttestationPolicyFor(namespace)
	if err != nil {
		return serverless.AttestationVerdict{}, err
	}
	mode := string(policy.Mode)
	if policy.Mode == auth.AttestationOff {
		return serverless.AttestationVerdict{Mode: mode, Verdict: serverless.VerdictNotConfigured,
			Message: "this namespace has no attestation policy: no caller is admitted as its attested app"}, nil
	}
	judgement := auth.JudgeAttestation(policy, attestationClaim(att), j.Now())
	return serverless.AttestationVerdict{
		Mode:    mode,
		Verdict: attestationVerdicts[judgement.Refusal],
		Reason:  string(judgement.Reason),
		Message: judgement.Message,
	}, nil
}
