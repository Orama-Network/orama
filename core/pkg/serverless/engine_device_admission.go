package serverless

import (
	"context"

	"github.com/tetratelabs/wazero/api"
	"go.uber.org/zap"
)

// The device-admission host calls (feat-973). hostfunctions/device_admission.go
// says what each one does; these move bytes across the guest boundary.

// hGetCallerDeviceState returns "pending", "active", or "" when the caller's
// session is bound to no device.
func (e *Engine) hGetCallerDeviceState(ctx context.Context, mod api.Module) uint64 {
	state := e.hostServices.GetCallerDeviceState(ctx)
	return e.executor.WriteToGuest(ctx, mod, []byte(state))
}

// hApproveDevice makes a pending device of a subject active. Returns 1 on
// success, 0 on failure, whose reason the gateway log records.
func (e *Engine) hApproveDevice(ctx context.Context, mod api.Module, subPtr, subLen, devPtr, devLen uint32) uint32 {
	return e.admitDevice(ctx, mod, "approve_device", e.hostServices.ApproveDevice, subPtr, subLen, devPtr, devLen)
}

// hDenyDevice revokes a pending device of a subject. Returns 1 on success, 0
// on failure, whose reason the gateway log records.
func (e *Engine) hDenyDevice(ctx context.Context, mod api.Module, subPtr, subLen, devPtr, devLen uint32) uint32 {
	return e.admitDevice(ctx, mod, "deny_device", e.hostServices.DenyDevice, subPtr, subLen, devPtr, devLen)
}

// hDenyDeviceWithEvidence revokes a pending device of a subject and keeps the
// function's evidence with its tombstone. Returns 1 on success, 0 on failure,
// whose reason the gateway log records.
func (e *Engine) hDenyDeviceWithEvidence(ctx context.Context, mod api.Module,
	subPtr, subLen, devPtr, devLen, evPtr, evLen uint32) uint32 {
	evidence, ok := e.executor.ReadFromGuest(mod, evPtr, evLen)
	if !ok {
		return 0
	}
	return e.admitDevice(ctx, mod, "deny_device_with_evidence",
		func(ctx context.Context, subject, device string) error {
			return e.hostServices.DenyDeviceWithEvidence(ctx, subject, device, string(evidence))
		}, subPtr, subLen, devPtr, devLen)
}

func (e *Engine) admitDevice(ctx context.Context, mod api.Module, name string, decide func(context.Context, string, string) error,
	subPtr, subLen, devPtr, devLen uint32) uint32 {
	subject, ok1 := e.executor.ReadFromGuest(mod, subPtr, subLen)
	device, ok2 := e.executor.ReadFromGuest(mod, devPtr, devLen)
	if !ok1 || !ok2 {
		return 0
	}
	if err := decide(ctx, string(subject), string(device)); err != nil {
		HostLogger(ctx, e.logger).Warn("host function "+name+" refused", zap.Error(err))
		return 0
	}
	return 1
}
