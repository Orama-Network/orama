package hostfunctions

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Orama-Network/orama/core/pkg/serverless"
)

// judgeFunc is an AttestationJudge from a function.
type judgeFunc func(namespace string, att *serverless.CallerAttestation) (serverless.AttestationVerdict, error)

func (f judgeFunc) AttestationVerdict(namespace string, att *serverless.CallerAttestation) (serverless.AttestationVerdict, error) {
	return f(namespace, att)
}

// get_caller_attestation_verdict asks the judge, at the call, about this
// invocation's namespace and caller, and hands back its verdict as JSON.
func TestGetCallerAttestationVerdict_asksTheJudgeAtTheCall(t *testing.T) {
	h := &HostFunctions{}
	att := &serverless.CallerAttestation{Platform: "android", AppID: "chat.anchat", SigningCertDigests: []string{"ab"}}
	ctx := invocationCtx(&serverless.InvocationContext{Namespace: "anchat", CallerDeviceID: "d", CallerAttestation: att})
	if got := h.GetCallerAttestationVerdict(ctx); got != "" {
		t.Errorf("with no judge wired the verdict is %q, want empty", got)
	}

	verdict := serverless.AttestationVerdict{Mode: "monitor", Verdict: serverless.VerdictAttestationInvalid,
		Reason: "signing_cert", Message: "no longer admitted"}
	var gotNamespace string
	var gotAtt *serverless.CallerAttestation
	h.SetAttestationJudge(judgeFunc(func(namespace string, a *serverless.CallerAttestation) (serverless.AttestationVerdict, error) {
		gotNamespace, gotAtt = namespace, a
		return verdict, nil
	}))
	var got serverless.AttestationVerdict
	if err := json.Unmarshal([]byte(h.GetCallerAttestationVerdict(ctx)), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if got != verdict || gotNamespace != "anchat" || gotAtt != att {
		t.Errorf("verdict %+v for %q / %+v", got, gotNamespace, gotAtt)
	}
	if got := h.GetCallerAttestationVerdict(context.Background()); got != "" {
		t.Errorf("no invocation reported %q", got)
	}
}

// A policy that cannot be read is "unavailable", never a verdict on the
// caller.
func TestGetCallerAttestationVerdict_anUnreadablePolicyIsUnavailable(t *testing.T) {
	h := &HostFunctions{}
	h.SetAttestationJudge(judgeFunc(func(string, *serverless.CallerAttestation) (serverless.AttestationVerdict, error) {
		return serverless.AttestationVerdict{}, errors.New("stale copy")
	}))
	var got serverless.AttestationVerdict
	ctx := invocationCtx(&serverless.InvocationContext{Namespace: "anchat"})
	if err := json.Unmarshal([]byte(h.GetCallerAttestationVerdict(ctx)), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if got.Verdict != serverless.VerdictUnavailable || got.Mode != "" || got.Message == "" {
		t.Errorf("an unreadable policy answered %+v", got)
	}
}
