package hostfunctions

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Orama-Network/orama/core/pkg/serverless"
)

// get_caller_attestation hands the function the gateway's verdict on the
// caller's device: what the token's signed `att` claim says, nothing else.
func TestGetCallerAttestation_returnsTheVerdictAsJSON(t *testing.T) {
	h := &HostFunctions{}
	ctx := invocationCtx(&serverless.InvocationContext{
		CallerDeviceID: "device-1",
		CallerAttestation: &serverless.CallerAttestation{
			Platform: "android", AppID: "chat.anchat", AppVersion: "412", AttestedAt: 1_790_000_000,
		},
	})
	var got serverless.CallerAttestation
	if err := json.Unmarshal([]byte(h.GetCallerAttestation(ctx)), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if got.Platform != "android" || got.AppID != "chat.anchat" || got.AppVersion != "412" || got.AttestedAt != 1_790_000_000 {
		t.Errorf("GetCallerAttestation = %+v", got)
	}
}

func TestGetCallerAttestation_emptyWithoutOne(t *testing.T) {
	h := &HostFunctions{}
	if got := h.GetCallerAttestation(invocationCtx(&serverless.InvocationContext{CallerDeviceID: "device-1"})); got != "" {
		t.Errorf("an unattested caller reported %q", got)
	}
	if got := h.GetCallerAttestation(context.Background()); got != "" {
		t.Errorf("no invocation reported %q", got)
	}
}

// A nested call is the same caller: the callee is told the same verdict.
func TestFunctionInvoke_carriesTheCallersAttestation(t *testing.T) {
	inv := &nestedRecordingInvoker{}
	h := hostWithInvoker(inv)
	att := &serverless.CallerAttestation{Platform: "android", AppID: "chat.anchat", AttestedAt: 1,
		SigningCertDigests: []string{"0123"}, OSPatchLevel: 202609}
	ctx := parentCtx(&serverless.InvocationContext{
		Namespace: "anchat-test", TriggerType: serverless.TriggerTypeHTTP,
		CallerJWTSubject: "0xwallet", CallerDeviceID: "device-1", CallerAttestation: att,
	})
	if _, err := h.FunctionInvoke(ctx, "callee", []byte(`{}`)); err != nil {
		t.Fatalf("FunctionInvoke: %v", err)
	}
	if got := inv.last().CallerAttestation; got == nil || !reflect.DeepEqual(got, att) {
		t.Errorf("nested call attestation = %+v", got)
	}
}

type enforcing map[string]bool

func (e enforcing) AttestationEnforced(namespace string) (bool, error) { return e[namespace], nil }

// In a namespace that enforces attestation only an attested caller mints a
// capability; anywhere else, and for an attested caller, the gate is open.
func TestRequireAttestedMint(t *testing.T) {
	h := &HostFunctions{}
	h.SetAttestationEnforcement(enforcing{"anchat": true})
	unattested := invocationCtx(&serverless.InvocationContext{Namespace: "anchat", CallerDeviceID: "d"})
	if err := h.requireAttestedMint(unattested); err == nil {
		t.Error("an unattested caller minted in an enforcing namespace")
	}
	attested := invocationCtx(&serverless.InvocationContext{Namespace: "anchat", CallerDeviceID: "d",
		CallerAttestation: &serverless.CallerAttestation{Platform: "ios"}})
	if err := h.requireAttestedMint(attested); err != nil {
		t.Errorf("an attested caller was refused: %v", err)
	}
	other := invocationCtx(&serverless.InvocationContext{Namespace: "other", CallerDeviceID: "d"})
	if err := h.requireAttestedMint(other); err != nil {
		t.Errorf("a namespace that does not enforce refused a mint: %v", err)
	}
}
