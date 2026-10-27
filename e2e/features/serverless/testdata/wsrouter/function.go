// Command function is the serverless feature's persistent-socket router
// (ws_persistent: true; TinyGo builds it reactor-mode). Each frame
// {"fn","mode","key","count"} is one op: the svcfn function named fn runs its
// "reads" op (count sequential reads). With mode "sync" the router runs it with
// function_invoke and answers with its output; otherwise it hands it to
// function_invoke_async and the op answers the socket itself.
package main

import (
	"encoding/json"
	"unsafe"
)

//go:wasmimport env function_invoke
func functionInvoke(np, nn, pp, pn uint32) uint64

//go:wasmimport env function_invoke_async
func functionInvokeAsync(np, nn, pp, pn uint32) uint32

//go:wasmimport env ws_send
func wsSend(cp, cn, dp, dn uint32) uint32

// clientID is this socket's, from ws_open: the instance's memory is the
// connection's state.
var clientID string

// held keeps what the host wrote into guest memory alive until the callback
// that received it returns.
var held [][]byte

func main() {}

//go:wasmexport orama_alloc
func oramaAlloc(n uint32) uint32 {
	if n == 0 {
		return 0
	}
	b := make([]byte, n)
	held = append(held, b)
	return uint32(uintptr(unsafe.Pointer(&b[0])))
}

//go:wasmexport ws_open
func wsOpen(p, n uint32) uint32 {
	defer release()
	var in struct {
		ClientID string `json:"client_id"`
	}
	if json.Unmarshal(at(p, n), &in) != nil || in.ClientID == "" {
		return 1
	}
	clientID = in.ClientID
	return 0
}

type frame struct {
	Fn    string `json:"fn"`
	Mode  string `json:"mode"`
	Key   string `json:"key"`
	Count int    `json:"count"`
}

//go:wasmexport ws_frame
func wsFrame(p, n uint32) uint32 {
	defer release()
	var f frame
	if err := json.Unmarshal(at(p, n), &f); err != nil {
		send(map[string]any{"error": "frame is not JSON: " + err.Error()})
		return 0
	}
	op := map[string]any{"op": "reads", "key": f.Key, "count": f.Count}
	if f.Mode == "sync" {
		body := encode(op)
		out := unpack(functionInvoke(ptr(f.Fn), size(f.Fn), ptr(body), size(body)))
		if out == "" {
			send(map[string]any{"id": f.Key, "error": "function_invoke failed"})
			return 0
		}
		wsSend(ptr(clientID), size(clientID), ptr(out), size(out))
		return 0
	}
	op["value"] = "send"
	body := encode(op)
	if functionInvokeAsync(ptr(f.Fn), size(f.Fn), ptr(body), size(body)) == 0 {
		send(map[string]any{"id": f.Key, "error": "function_invoke_async refused"})
	}
	return 0
}

//go:wasmexport ws_close
func wsClose(p, n uint32) { release() }

func release() { held = nil }

func send(v map[string]any) {
	msg := encode(v)
	wsSend(ptr(clientID), size(clientID), ptr(msg), size(msg))
}

func encode(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func at(p, n uint32) []byte {
	if n == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(p))), n)
}

func ptr(s string) uint32 {
	if s == "" {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(unsafe.StringData(s))))
}

func size(s string) uint32 { return uint32(len(s)) }

func unpack(v uint64) string {
	if v == 0 {
		return ""
	}
	p, n := uint32(v>>32), uint32(v)
	return string(unsafe.Slice((*byte)(unsafe.Pointer(uintptr(p))), n))
}
