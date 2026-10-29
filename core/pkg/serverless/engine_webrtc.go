package serverless

import (
	"context"
	"time"

	"github.com/tetratelabs/wazero/api"
	"go.uber.org/zap"
)

// The WebRTC host calls (bugboard #726). hostfunctions/webrtc.go says what each
// one does; these move bytes across the guest boundary.

// maxAdmissionTTLSeconds bounds a guest's ttl before it becomes a Duration, so
// no value it passes can overflow one. The controller enforces the real bound.
const maxAdmissionTTLSeconds = int64(365 * 24 * 60 * 60)

// hWebRTCAdmit admits a user to a room. Returns the packed ptr<<32|len of the
// JSON {"room","user_id","device_id","expires_at"}, or 0 on failure, whose
// reason the gateway log records.
func (e *Engine) hWebRTCAdmit(ctx context.Context, mod api.Module,
	roomPtr, roomLen, userPtr, userLen, devPtr, devLen uint32, ttlSeconds int64) uint64 {
	return e.webrtcAdmit(ctx, mod, "webrtc_admit", e.hostServices.WebRTCAdmit, roomPtr, roomLen, userPtr, userLen, devPtr, devLen, ttlSeconds)
}

// hWebRTCAdmitWithMutes is hWebRTCAdmit stating the user's audio (muted != 0)
// and video (videoMuted != 0) mutes, recorded with the admission. Returns the
// packed ptr<<32|len of the JSON {"room","user_id","device_id","expires_at",
// "muted","video_muted"}, or 0 on failure, whose reason the gateway log
// records.
func (e *Engine) hWebRTCAdmitWithMutes(ctx context.Context, mod api.Module,
	roomPtr, roomLen, userPtr, userLen, devPtr, devLen uint32, ttlSeconds int64, muted, videoMuted uint32) uint64 {
	mutes := WebRTCMutes{Muted: muted != 0, VideoMuted: videoMuted != 0}
	admit := func(ctx context.Context, room, user, device string, ttl time.Duration) (string, error) {
		return e.hostServices.WebRTCAdmitWithMutes(ctx, room, user, device, ttl, mutes)
	}
	return e.webrtcAdmit(ctx, mod, "webrtc_admit_with_mutes", admit, roomPtr, roomLen, userPtr, userLen, devPtr, devLen, ttlSeconds)
}

func (e *Engine) webrtcAdmit(ctx context.Context, mod api.Module, name string,
	admit func(context.Context, string, string, string, time.Duration) (string, error),
	roomPtr, roomLen, userPtr, userLen, devPtr, devLen uint32, ttlSeconds int64) uint64 {
	room, ok1 := e.executor.ReadFromGuest(mod, roomPtr, roomLen)
	user, ok2 := e.executor.ReadFromGuest(mod, userPtr, userLen)
	device, ok3 := e.executor.ReadFromGuest(mod, devPtr, devLen)
	if !ok1 || !ok2 || !ok3 {
		return 0
	}
	if ttlSeconds <= 0 || ttlSeconds > maxAdmissionTTLSeconds {
		HostLogger(ctx, e.logger).Warn("host function "+name+" refused: ttl out of range", zap.Int64("ttl_seconds", ttlSeconds))
		return 0
	}
	admitted, err := admit(ctx, string(room), string(user), string(device), time.Duration(ttlSeconds)*time.Second)
	if err != nil {
		HostLogger(ctx, e.logger).Warn("host function "+name+" refused", zap.Error(err))
		return 0
	}
	return e.executor.WriteToGuest(ctx, mod, []byte(admitted))
}

// hWebRTCKick revokes a user's admissions to a room and closes their
// connection. Returns 1 on success, 0 on failure.
func (e *Engine) hWebRTCKick(ctx context.Context, mod api.Module, roomPtr, roomLen, userPtr, userLen uint32) uint32 {
	room, ok1 := e.executor.ReadFromGuest(mod, roomPtr, roomLen)
	user, ok2 := e.executor.ReadFromGuest(mod, userPtr, userLen)
	if !ok1 || !ok2 {
		return 0
	}
	if err := e.hostServices.WebRTCKick(ctx, string(room), string(user)); err != nil {
		HostLogger(ctx, e.logger).Warn("host function webrtc_kick failed", zap.Error(err))
		return 0
	}
	return 1
}

// hWebRTCKickDevice revokes the admission of one device of a user to a room
// and closes that device's connection. Returns 1 on success, 0 on failure.
func (e *Engine) hWebRTCKickDevice(ctx context.Context, mod api.Module, roomPtr, roomLen, userPtr, userLen, devPtr, devLen uint32) uint32 {
	room, ok1 := e.executor.ReadFromGuest(mod, roomPtr, roomLen)
	user, ok2 := e.executor.ReadFromGuest(mod, userPtr, userLen)
	device, ok3 := e.executor.ReadFromGuest(mod, devPtr, devLen)
	if !ok1 || !ok2 || !ok3 {
		return 0
	}
	if err := e.hostServices.WebRTCKickDevice(ctx, string(room), string(user), string(device)); err != nil {
		HostLogger(ctx, e.logger).Warn("host function webrtc_kick_device failed", zap.Error(err))
		return 0
	}
	return 1
}

// hWebRTCMute stops (muted != 0) or resumes a user's audio in a room. Returns 1
// on success, 0 on failure.
func (e *Engine) hWebRTCMute(ctx context.Context, mod api.Module, roomPtr, roomLen, userPtr, userLen, muted uint32) uint32 {
	return e.webrtcMute(ctx, mod, "webrtc_mute", e.hostServices.WebRTCMute, roomPtr, roomLen, userPtr, userLen, muted)
}

// hWebRTCMuteVideo stops (muted != 0) or resumes a user's video, camera and
// screen, in a room. Returns 1 on success, 0 on failure.
func (e *Engine) hWebRTCMuteVideo(ctx context.Context, mod api.Module, roomPtr, roomLen, userPtr, userLen, muted uint32) uint32 {
	return e.webrtcMute(ctx, mod, "webrtc_mute_video", e.hostServices.WebRTCMuteVideo, roomPtr, roomLen, userPtr, userLen, muted)
}

func (e *Engine) webrtcMute(ctx context.Context, mod api.Module, name string, mute func(context.Context, string, string, bool) error,
	roomPtr, roomLen, userPtr, userLen, muted uint32) uint32 {
	room, ok1 := e.executor.ReadFromGuest(mod, roomPtr, roomLen)
	user, ok2 := e.executor.ReadFromGuest(mod, userPtr, userLen)
	if !ok1 || !ok2 {
		return 0
	}
	if err := mute(ctx, string(room), string(user), muted != 0); err != nil {
		HostLogger(ctx, e.logger).Warn("host function "+name+" failed", zap.Error(err))
		return 0
	}
	return 1
}

// hWebRTCAdmission reads the admission one device of a user holds in a room
// ("" names a session bound to no device). Returns the packed ptr<<32|len of
// the JSON {"room","user_id","device_id","admitted","revoked","expired",
// "muted","video_muted","expires_at"}, or 0 on failure, whose reason the
// gateway log records.
func (e *Engine) hWebRTCAdmission(ctx context.Context, mod api.Module, roomPtr, roomLen, userPtr, userLen, devPtr, devLen uint32) uint64 {
	room, ok1 := e.executor.ReadFromGuest(mod, roomPtr, roomLen)
	user, ok2 := e.executor.ReadFromGuest(mod, userPtr, userLen)
	device, ok3 := e.executor.ReadFromGuest(mod, devPtr, devLen)
	if !ok1 || !ok2 || !ok3 {
		return 0
	}
	adm, err := e.hostServices.WebRTCAdmission(ctx, string(room), string(user), string(device))
	if err != nil {
		HostLogger(ctx, e.logger).Warn("host function webrtc_admission failed", zap.Error(err))
		return 0
	}
	return e.executor.WriteToGuest(ctx, mod, []byte(adm))
}
