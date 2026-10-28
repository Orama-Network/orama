package hostfunctions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Orama-Network/orama/core/pkg/serverless"
)

// Device admission by the namespace (feat-973). Under the function device
// policy a device beyond an account's first signs in to a restricted session
// and waits; the namespace's own functions decide. get_caller_device_state
// tells a function whether its caller is such a device, and approve_device and
// deny_device carry out the decision, in the calling function's namespace
// only, through the gateway's device registry — never through the caller's
// token, so what a pending caller can make of them is exactly what the
// function's code lets it. deny_device_with_evidence denies as deny_device
// does and attaches the application's evidence (an account-signed statement,
// for example) to the device's tombstone, which the device is shown in its
// DEVICE_REVOKED refusals (feat-976).

// DeviceAdmitter approves and denies pending devices of a namespace. The
// gateway's is its *auth.Service.
type DeviceAdmitter interface {
	ApprovePendingDevice(ctx context.Context, namespace, subject, device, approver string) error
	DenyPendingDevice(ctx context.Context, namespace, subject, device, approver, evidence string) error
}

// maxDeviceAdmissionsPerInvocation caps how many approve_device, deny_device
// and deny_device_with_evidence calls one invocation (or persistent socket frame) may make. A
// real admission decides on one device; each call writes the registry, and
// the WASM runtime has no fuel metering, so a runaway loop would otherwise
// hammer it.
const maxDeviceAdmissionsPerInvocation = 16

var (
	errNoDeviceAdmitter = errors.New("this gateway cannot admit devices: it has no device registry")
	errAdmissionNeeds   = errors.New("name the account (its subject, as get_caller_jwt_subject returns it) and the device (as get_caller_device_id returns it)")

	// ErrDeviceAdmissionBudgetExceeded is the cause of an approve_device or
	// deny_device refused because its invocation already made
	// maxDeviceAdmissionsPerInvocation of them.
	ErrDeviceAdmissionBudgetExceeded = fmt.Errorf("device admission budget exceeded (max %d per invocation)", maxDeviceAdmissionsPerInvocation)
)

// SetDeviceAdmitter wires what approve_device and deny_device act through.
// Called once at gateway start, before any function runs.
func (h *HostFunctions) SetDeviceAdmitter(a DeviceAdmitter) {
	h.deviceAdmitter = a
}

// GetCallerDeviceState returns the state of the device the caller's session is
// bound to, as the gateway minted its token: "pending" for a device the
// namespace has yet to approve, "active", or "" when the session is bound to
// no device (or there is no caller).
func (h *HostFunctions) GetCallerDeviceState(ctx context.Context) string {
	cur := h.currentInvocationContext(ctx)
	if cur == nil {
		return ""
	}
	return cur.CallerDeviceState
}

// ApproveDevice makes subject's pending device active in the calling
// function's namespace. Its next refresh is a full session.
func (h *HostFunctions) ApproveDevice(ctx context.Context, subject, device string) error {
	ns, approver, a, err := h.deviceAdmission(ctx, "approve_device", subject, device)
	if err != nil {
		return err
	}
	if err := a.ApprovePendingDevice(ctx, ns, strings.TrimSpace(subject), strings.TrimSpace(device), approver); err != nil {
		return &serverless.HostFunctionError{Function: "approve_device", Cause: err}
	}
	return nil
}

// DenyDevice revokes subject's pending device in the calling function's
// namespace, which ends its restricted session.
func (h *HostFunctions) DenyDevice(ctx context.Context, subject, device string) error {
	return h.denyDevice(ctx, "deny_device", subject, device, "")
}

// DenyDeviceWithEvidence denies as DenyDevice does and keeps evidence, at most
// 16 KiB of UTF-8, with the device's tombstone, verbatim.
func (h *HostFunctions) DenyDeviceWithEvidence(ctx context.Context, subject, device, evidence string) error {
	return h.denyDevice(ctx, "deny_device_with_evidence", subject, device, evidence)
}

func (h *HostFunctions) denyDevice(ctx context.Context, fn, subject, device, evidence string) error {
	ns, approver, a, err := h.deviceAdmission(ctx, fn, subject, device)
	if err != nil {
		return err
	}
	if err := a.DenyPendingDevice(ctx, ns, strings.TrimSpace(subject), strings.TrimSpace(device), approver, evidence); err != nil {
		return &serverless.HostFunctionError{Function: fn, Cause: err}
	}
	return nil
}

// deviceAdmission is the calling function's namespace, who decides (for the
// audit record), and what to decide through.
func (h *HostFunctions) deviceAdmission(ctx context.Context, fn, subject, device string) (string, string, DeviceAdmitter, error) {
	cur := h.currentInvocationContext(ctx)
	if cur == nil || cur.Namespace == "" {
		return "", "", nil, &serverless.HostFunctionError{Function: fn, Cause: errNoInvocation}
	}
	if h.deviceAdmitter == nil {
		return "", "", nil, &serverless.HostFunctionError{Function: fn, Cause: errNoDeviceAdmitter}
	}
	if strings.TrimSpace(subject) == "" || strings.TrimSpace(device) == "" {
		return "", "", nil, &serverless.HostFunctionError{Function: fn, Cause: errAdmissionNeeds}
	}
	if n := serverless.AddDeviceAdmissionCount(ctx); n > maxDeviceAdmissionsPerInvocation {
		return "", "", nil, &serverless.HostFunctionError{Function: fn, Cause: ErrDeviceAdmissionBudgetExceeded}
	}
	return cur.Namespace, "function:" + cur.FunctionName, h.deviceAdmitter, nil
}
