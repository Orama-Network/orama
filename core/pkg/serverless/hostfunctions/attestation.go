package hostfunctions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Orama-Network/orama/core/pkg/serverless"
	"go.uber.org/zap"
)

// A capability (feat-264) and a storage fetch capability (bugboard #266) carry
// no credential when they are used: whoever holds one opens what it names. In
// a namespace that enforces device attestation (feat-941) that would be a way
// round it, so the gate is at the mint: only an invocation whose caller is an
// attested device mints one. A caller the attestation gate exempts (an
// operator, a workload, a control-plane key) and a gateway-started invocation
// (cron, a trigger) carry no attestation, and mint none there.

// AttestationEnforcement says whether a namespace enforces device attestation.
// *auth.Service is the one the gateway uses.
type AttestationEnforcement interface {
	AttestationEnforced(namespace string) (bool, error)
}

// AttestationJudge judges a caller's attestation against its namespace's
// policy as the gateway holds it now (feat-965). The gateway's is
// serverlesshandlers.AttestationJudge.
type AttestationJudge interface {
	AttestationVerdict(namespace string, att *serverless.CallerAttestation) (serverless.AttestationVerdict, error)
}

var errMintNeedsAttestation = errors.New("this namespace admits only its attested app: a capability is minted only for an attested caller")

// verdictUnavailableMessage is what a function is told when the namespace's
// policy cannot be read.
const verdictUnavailableMessage = "this gateway cannot read the namespace's attestation policy right now; retry shortly"

// SetAttestationEnforcement wires what tells whether a namespace enforces
// attestation. Called once at gateway start, before any function runs.
func (h *HostFunctions) SetAttestationEnforcement(a AttestationEnforcement) {
	h.attestation = a
}

// SetAttestationJudge wires what answers get_caller_attestation_verdict.
// Called once at gateway start, before any function runs.
func (h *HostFunctions) SetAttestationJudge(j AttestationJudge) {
	h.attestationJudge = j
}

// requireAttestedMint refuses a mint in a namespace that enforces attestation
// for an invocation whose caller is not an attested device.
func (h *HostFunctions) requireAttestedMint(ctx context.Context) error {
	cur := h.currentInvocationContext(ctx)
	if h.attestation == nil || cur == nil || cur.CallerAttestation != nil {
		return nil
	}
	enforced, err := h.attestation.AttestationEnforced(cur.Namespace)
	if err != nil {
		return fmt.Errorf("cannot tell whether this namespace enforces attestation: %w", err)
	}
	if enforced {
		return errMintNeedsAttestation
	}
	return nil
}

// GetCallerAttestationVerdict returns, as JSON, the gateway's verdict on the
// caller's attestation under the namespace's policy as it is at this call, in
// every mode (serverless.AttestationVerdict). It is judged each time it is
// asked, so a long-running invocation or a socket's later message sees a
// policy changed since it began. Empty outside a gateway.
func (h *HostFunctions) GetCallerAttestationVerdict(ctx context.Context) string {
	cur := h.currentInvocationContext(ctx)
	if cur == nil || h.attestationJudge == nil {
		return ""
	}
	verdict, err := h.attestationJudge.AttestationVerdict(cur.Namespace, cur.CallerAttestation)
	if err != nil {
		if h.logger != nil {
			serverless.HostLogger(ctx, h.logger).Warn("get_caller_attestation_verdict: the namespace's attestation policy cannot be read",
				zap.String("namespace", cur.Namespace), zap.Error(err))
		}
		verdict = serverless.AttestationVerdict{Verdict: serverless.VerdictUnavailable, Message: verdictUnavailableMessage}
	}
	buf, err := json.Marshal(verdict)
	if err != nil {
		return ""
	}
	return string(buf)
}
