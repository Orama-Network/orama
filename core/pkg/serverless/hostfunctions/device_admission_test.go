package hostfunctions

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Orama-Network/orama/core/pkg/serverless"
)

type recordingAdmitter struct {
	calls []string
	err   error
}

func (r *recordingAdmitter) ApprovePendingDevice(_ context.Context, ns, subject, device, approver string) error {
	r.calls = append(r.calls, strings.Join([]string{"approve", ns, subject, device, approver}, "|"))
	return r.err
}

func (r *recordingAdmitter) DenyPendingDevice(_ context.Context, ns, subject, device, approver, evidence string) error {
	r.calls = append(r.calls, strings.Join([]string{"deny", ns, subject, device, approver, evidence}, "|"))
	return r.err
}

func admitterHost(a DeviceAdmitter) (*HostFunctions, context.Context) {
	h := &HostFunctions{}
	h.SetDeviceAdmitter(a)
	return h, invocationCtx(&serverless.InvocationContext{Namespace: "anchat", FunctionName: "device-join"})
}

// The decision acts in the calling function's own namespace and names the
// function as who decided; the namespace is never the caller's to pick.
func TestDeviceAdmission_actsInTheCallingFunctionsNamespace(t *testing.T) {
	a := &recordingAdmitter{}
	h, ctx := admitterHost(a)

	if err := h.ApproveDevice(ctx, " 0xwallet ", "device-2"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := h.DenyDevice(ctx, "0xwallet", "device-3"); err != nil {
		t.Fatalf("deny: %v", err)
	}
	if err := h.DenyDeviceWithEvidence(ctx, "0xwallet", "device-4", "roster-link"); err != nil {
		t.Fatalf("deny with evidence: %v", err)
	}
	want := []string{
		"approve|anchat|0xwallet|device-2|function:device-join",
		"deny|anchat|0xwallet|device-3|function:device-join|",
		"deny|anchat|0xwallet|device-4|function:device-join|roster-link",
	}
	if strings.Join(a.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", a.calls, want)
	}
}

func TestDeviceAdmission_wrapsTheRegistrysRefusal(t *testing.T) {
	boom := errors.New("not pending")
	h, ctx := admitterHost(&recordingAdmitter{err: boom})
	for name, err := range map[string]error{
		"approve_device":            h.ApproveDevice(ctx, "0xwallet", "d"),
		"deny_device":               h.DenyDevice(ctx, "0xwallet", "d"),
		"deny_device_with_evidence": h.DenyDeviceWithEvidence(ctx, "0xwallet", "d", "e"),
	} {
		var hfe *serverless.HostFunctionError
		if !errors.As(err, &hfe) || !errors.Is(err, boom) || hfe.Function != name {
			t.Errorf("%s: err = %v, want a %s host function error wrapping the cause", name, err, name)
		}
	}
}

func TestDeviceAdmission_refusesWithoutAnInvocationAnAdmitterOrATarget(t *testing.T) {
	if err := (&HostFunctions{}).ApproveDevice(invocationCtx(&serverless.InvocationContext{Namespace: "anchat"}), "0xw", "d"); err == nil ||
		!strings.Contains(err.Error(), "no device registry") {
		t.Errorf("approve without an admitter: %v", err)
	}

	a := &recordingAdmitter{}
	h, ctx := admitterHost(a)
	if err := h.ApproveDevice(context.Background(), "0xw", "d"); err == nil {
		t.Error("approve outside an invocation succeeded")
	}
	if err := h.DenyDevice(invocationCtx(&serverless.InvocationContext{}), "0xw", "d"); err == nil {
		t.Error("deny with no namespace succeeded")
	}
	if err := h.ApproveDevice(ctx, " ", "d"); err == nil {
		t.Error("approve with no subject succeeded")
	}
	if err := h.DenyDevice(ctx, "0xw", ""); err == nil {
		t.Error("deny with no device succeeded")
	}
	if err := h.DenyDeviceWithEvidence(ctx, "", "d", "e"); err == nil {
		t.Error("deny with evidence and no subject succeeded")
	}
	if len(a.calls) != 0 {
		t.Errorf("a refused call reached the registry: %v", a.calls)
	}
}

func TestDeviceAdmission_budgetIsPerInvocation(t *testing.T) {
	a := &recordingAdmitter{}
	h, base := admitterHost(a)
	ctx := serverless.WithDeviceAdmissionCounter(base)

	for i := 0; i < maxDeviceAdmissionsPerInvocation; i++ {
		if err := h.ApproveDevice(ctx, "0xw", "d"); err != nil {
			t.Fatalf("call %d within the budget failed: %v", i+1, err)
		}
	}
	if err := h.DenyDeviceWithEvidence(ctx, "0xw", "d", "e"); !errors.Is(err, ErrDeviceAdmissionBudgetExceeded) {
		t.Fatalf("call past the budget: err = %v, want ErrDeviceAdmissionBudgetExceeded", err)
	}
	if len(a.calls) != maxDeviceAdmissionsPerInvocation {
		t.Errorf("registry saw %d calls, want %d", len(a.calls), maxDeviceAdmissionsPerInvocation)
	}
	if err := h.ApproveDevice(serverless.WithDeviceAdmissionCounter(base), "0xw", "d"); err != nil {
		t.Errorf("a fresh invocation was refused: %v", err)
	}
}

func TestGetCallerDeviceState_isTheInvocationsOwn(t *testing.T) {
	h := &HostFunctions{}
	if got := h.GetCallerDeviceState(context.Background()); got != "" {
		t.Errorf("outside an invocation = %q, want empty", got)
	}
	ctx := invocationCtx(&serverless.InvocationContext{Namespace: "anchat", CallerDeviceState: serverless.CallerDevicePending})
	if got := h.GetCallerDeviceState(ctx); got != serverless.CallerDevicePending {
		t.Errorf("pending caller = %q, want pending", got)
	}
}
