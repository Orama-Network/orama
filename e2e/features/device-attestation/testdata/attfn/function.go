// Command function is the device-attestation fixture: TinyGo, WASI,
// stateless. It answers with what the gateway says about the caller's device
// attestation (get_caller_attestation) and its verdict on it under the
// namespace's policy (get_caller_attestation_verdict), so a test sees both
// that the request reached the function and what the function was told.
package main

import (
	"encoding/json"
	"os"
	"unsafe"
)

//go:wasmimport env get_caller_attestation
func getCallerAttestation() uint64

//go:wasmimport env get_caller_attestation_verdict
func getCallerAttestationVerdict() uint64

func main() {
	out, err := json.Marshal(map[string]string{
		"attestation": unpack(getCallerAttestation()),
		"verdict":     unpack(getCallerAttestationVerdict()),
	})
	if err != nil {
		out = []byte(`{"error":"encode reply"}`)
	}
	os.Stdout.Write(out)
}

// unpack reads the host's packed ptr<<32|len result; 0 is "nothing".
func unpack(v uint64) string {
	if v == 0 {
		return ""
	}
	p, n := uint32(v>>32), uint32(v)
	return string(unsafe.Slice((*byte)(unsafe.Pointer(uintptr(p))), n))
}
