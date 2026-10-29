package serverless

import (
	"errors"
	"fmt"
)

// A pending device's restricted session (the function device policy,
// feat-973) runs only the functions the namespace deployed with
// `open_to_pending_devices: true`: the ones that carry out its own admission
// of a new device. The gateway refuses such a session on every other route;
// this is the invoker's half, which covers every way a function is reached —
// HTTP, a socket frame, a persistent socket and a nested function_invoke.

// The states of a caller's device, as InvokeRequest.CallerDeviceState and
// get_caller_device_state carry them. They are the gateway's own
// (auth.CallerDevicePending, auth.CallerDeviceActive).
const (
	CallerDevicePending = "pending"
	CallerDeviceActive  = "active"
)

// ErrDevicePendingApproval is a pending device's session asking for a
// function that is not open to pending devices.
var ErrDevicePendingApproval = errors.New("the caller's device is pending approval")

// PendingDeviceMayInvoke refuses a pending device's session a function that is
// not open to pending devices. Any other caller may, as far as this goes.
func PendingDeviceMayInvoke(fn *Function, callerDeviceState string) error {
	if callerDeviceState != CallerDevicePending || fn.OpenToPendingDevices {
		return nil
	}
	return fmt.Errorf("%w, and function %q is not open to pending devices (function.yaml open_to_pending_devices)",
		ErrDevicePendingApproval, fn.Name)
}

// IsDevicePendingApproval reports whether err is that refusal.
func IsDevicePendingApproval(err error) bool {
	return errors.Is(err, ErrDevicePendingApproval)
}
