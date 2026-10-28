// Command function is the device-admission fixture: TinyGo, WASI, stateless.
// It reports who called and the state of the caller's device
// (get_caller_jwt_subject, get_caller_device_id, get_caller_device_state),
// and approves or denies a pending device (approve_device, deny_device) — the
// namespace's own admission decision, which an application takes after its
// own check (a code shown on both devices, a recovery question).
package main

import (
	"encoding/json"
	"io"
	"os"
	"unsafe"
)

//go:wasmimport env get_caller_jwt_subject
func getCallerJWTSubject() uint64

//go:wasmimport env get_caller_device_id
func getCallerDeviceID() uint64

//go:wasmimport env get_caller_device_state
func getCallerDeviceState() uint64

//go:wasmimport env approve_device
func approveDevice(subPtr, subLen, devPtr, devLen uint32) uint32

//go:wasmimport env deny_device
func denyDevice(subPtr, subLen, devPtr, devLen uint32) uint32

type request struct {
	Op      string `json:"op"`
	Subject string `json:"subject"`
	Device  string `json:"device"`
}

func main() {
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		reply(map[string]any{"error": "read input: " + err.Error()})
		return
	}
	var req request
	if len(in) > 0 {
		if err := json.Unmarshal(in, &req); err != nil {
			reply(map[string]any{"error": "input is not JSON: " + err.Error()})
			return
		}
	}
	switch req.Op {
	case "approve":
		reply(map[string]any{"done": approveDevice(ptr(req.Subject), uint32(len(req.Subject)), ptr(req.Device), uint32(len(req.Device))) == 1})
	case "deny":
		reply(map[string]any{"done": denyDevice(ptr(req.Subject), uint32(len(req.Subject)), ptr(req.Device), uint32(len(req.Device))) == 1})
	default:
		reply(map[string]any{
			"subject":      unpack(getCallerJWTSubject()),
			"device_id":    unpack(getCallerDeviceID()),
			"device_state": unpack(getCallerDeviceState()),
		})
	}
}

func reply(v map[string]any) {
	out, err := json.Marshal(v)
	if err != nil {
		out = []byte(`{"error":"encode reply"}`)
	}
	os.Stdout.Write(out)
}

// ptr is the guest address of s's bytes; 0 for an empty string.
func ptr(s string) uint32 {
	if s == "" {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(unsafe.StringData(s))))
}

// unpack reads the host's packed ptr<<32|len result; 0 is "nothing".
func unpack(v uint64) string {
	if v == 0 {
		return ""
	}
	p, n := uint32(v>>32), uint32(v)
	return string(unsafe.Slice((*byte)(unsafe.Pointer(uintptr(p))), n))
}
