// Command function is the WebRTC fixture the webrtc feature deploys with
// `orama function deploy`: TinyGo, WASI, stateless. It admits a user (or one
// device of theirs) to a room, kicks a user or one device, mutes a user's audio
// or video, reads a device's admission through the webrtc_* host calls, and
// reports the caller's device id as an application records it when its device
// registers.
package main

import (
	"encoding/json"
	"io"
	"os"
	"unsafe"
)

//go:wasmimport env webrtc_admit
func webrtcAdmit(roomPtr, roomLen, userPtr, userLen, devPtr, devLen uint32, ttlSeconds int64) uint64

//go:wasmimport env webrtc_kick
func webrtcKick(roomPtr, roomLen, userPtr, userLen uint32) uint32

//go:wasmimport env webrtc_kick_device
func webrtcKickDevice(roomPtr, roomLen, userPtr, userLen, devPtr, devLen uint32) uint32

//go:wasmimport env webrtc_mute
func webrtcMute(roomPtr, roomLen, userPtr, userLen, muted uint32) uint32

//go:wasmimport env webrtc_mute_video
func webrtcMuteVideo(roomPtr, roomLen, userPtr, userLen, muted uint32) uint32

//go:wasmimport env webrtc_admission
func webrtcAdmission(roomPtr, roomLen, userPtr, userLen, devPtr, devLen uint32) uint64

//go:wasmimport env get_caller_device_id
func getCallerDeviceID() uint64

type request struct {
	Op     string `json:"op"`
	Room   string `json:"room"`
	User   string `json:"user"`
	Device string `json:"device"`
	TTL    int64  `json:"ttl"`
	Muted  bool   `json:"muted"`
}

func main() {
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		reply(map[string]any{"error": "read input: " + err.Error()})
		return
	}
	var req request
	if err := json.Unmarshal(in, &req); err != nil {
		reply(map[string]any{"error": "input is not JSON: " + err.Error()})
		return
	}
	room, user, device := req.Room, req.User, req.Device
	switch req.Op {
	case "admit":
		admitted := unpack(webrtcAdmit(ptr(room), uint32(len(room)), ptr(user), uint32(len(user)), ptr(device), uint32(len(device)), req.TTL))
		if admitted == "" {
			reply(map[string]any{"error": "admit refused"})
			return
		}
		os.Stdout.Write([]byte(admitted))
	case "admission":
		admission := unpack(webrtcAdmission(ptr(room), uint32(len(room)), ptr(user), uint32(len(user)), ptr(device), uint32(len(device))))
		if admission == "" {
			reply(map[string]any{"error": "admission read refused"})
			return
		}
		os.Stdout.Write([]byte(admission))
	case "whoami":
		reply(map[string]any{"device_id": unpack(getCallerDeviceID())})
	case "kick":
		reply(map[string]any{"done": webrtcKick(ptr(room), uint32(len(room)), ptr(user), uint32(len(user))) == 1})
	case "kick_device":
		reply(map[string]any{"done": webrtcKickDevice(ptr(room), uint32(len(room)), ptr(user), uint32(len(user)), ptr(device), uint32(len(device))) == 1})
	case "mute":
		reply(map[string]any{"done": webrtcMute(ptr(room), uint32(len(room)), ptr(user), uint32(len(user)), flag(req.Muted)) == 1})
	case "mute_video":
		reply(map[string]any{"done": webrtcMuteVideo(ptr(room), uint32(len(room)), ptr(user), uint32(len(user)), flag(req.Muted)) == 1})
	default:
		reply(map[string]any{"error": "unknown op " + req.Op})
	}
}

func flag(b bool) uint32 {
	if b {
		return 1
	}
	return 0
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
