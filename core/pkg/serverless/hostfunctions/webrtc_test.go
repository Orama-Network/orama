package hostfunctions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/serverless"
)

type recordingWebRTC struct {
	calls     []string
	expires   time.Time
	admission serverless.WebRTCAdmission
	err       error
}

func (r *recordingWebRTC) Admit(_ context.Context, ns, room, user, device string, ttl time.Duration) (time.Time, error) {
	r.calls = append(r.calls, strings.Join([]string{"admit", ns, room, user, device, ttl.String()}, "|"))
	return r.expires, r.err
}

func (r *recordingWebRTC) Kick(_ context.Context, ns, room, user string) error {
	r.calls = append(r.calls, strings.Join([]string{"kick", ns, room, user}, "|"))
	return r.err
}

func (r *recordingWebRTC) KickDevice(_ context.Context, ns, room, user, device string) error {
	r.calls = append(r.calls, strings.Join([]string{"kick-device", ns, room, user, device}, "|"))
	return r.err
}

func (r *recordingWebRTC) Mute(_ context.Context, ns, room, user string, muted bool) error {
	r.calls = append(r.calls, strings.Join([]string{muteVerb("", muted), ns, room, user}, "|"))
	return r.err
}

func (r *recordingWebRTC) MuteVideo(_ context.Context, ns, room, user string, muted bool) error {
	r.calls = append(r.calls, strings.Join([]string{muteVerb("video-", muted), ns, room, user}, "|"))
	return r.err
}

func (r *recordingWebRTC) AdmissionOf(_ context.Context, ns, room, user, device string) (serverless.WebRTCAdmission, error) {
	r.calls = append(r.calls, strings.Join([]string{"admission", ns, room, user, device}, "|"))
	return r.admission, r.err
}

func muteVerb(prefix string, muted bool) string {
	if muted {
		return prefix + "mute"
	}
	return prefix + "unmute"
}

func webrtcHost(c serverless.WebRTCController) (*HostFunctions, context.Context) {
	h := &HostFunctions{}
	h.SetWebRTCController(c)
	return h, invocationCtx(&serverless.InvocationContext{Namespace: "anchat", FunctionName: "call-start"})
}

func TestWebRTCAdmit_actsOnTheCallersOwnNamespace(t *testing.T) {
	c := &recordingWebRTC{expires: time.Unix(1_800_003_600, 0)}
	h, ctx := webrtcHost(c)

	out, err := h.WebRTCAdmit(ctx, "room-1", "0xalice", "phone", time.Hour)

	if err != nil {
		t.Fatal(err)
	}
	if len(c.calls) != 1 || c.calls[0] != "admit|anchat|room-1|0xalice|phone|1h0m0s" {
		t.Fatalf("calls = %v, want the caller's namespace and nothing a function chose", c.calls)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got["room"] != "room-1" || got["user_id"] != "0xalice" || got["device_id"] != "phone" || got["expires_at"] != float64(1_800_003_600) {
		t.Fatalf("admission = %v", got)
	}
}

func TestWebRTCKickAndMute_passThroughToTheController(t *testing.T) {
	c := &recordingWebRTC{}
	h, ctx := webrtcHost(c)

	if err := h.WebRTCKick(ctx, "r", "u"); err != nil {
		t.Fatal(err)
	}
	if err := h.WebRTCMute(ctx, "r", "u", true); err != nil {
		t.Fatal(err)
	}
	if err := h.WebRTCMute(ctx, "r", "u", false); err != nil {
		t.Fatal(err)
	}
	want := []string{"kick|anchat|r|u", "mute|anchat|r|u", "unmute|anchat|r|u"}
	if strings.Join(c.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", c.calls, want)
	}
}

// feat-964: a device kick names the device, a video mute is not an audio mute.
func TestWebRTCKickDeviceAndMuteVideo_passThroughToTheController(t *testing.T) {
	c := &recordingWebRTC{}
	h, ctx := webrtcHost(c)

	if err := h.WebRTCKickDevice(ctx, "r", "u", "did-phone"); err != nil {
		t.Fatal(err)
	}
	if err := h.WebRTCMuteVideo(ctx, "r", "u", true); err != nil {
		t.Fatal(err)
	}
	if err := h.WebRTCMuteVideo(ctx, "r", "u", false); err != nil {
		t.Fatal(err)
	}
	want := []string{"kick-device|anchat|r|u|did-phone", "video-mute|anchat|r|u", "video-unmute|anchat|r|u"}
	if strings.Join(c.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", c.calls, want)
	}
}

func TestWebRTCAdmission_reportsTheControllersVerdictForTheNamedDevice(t *testing.T) {
	c := &recordingWebRTC{admission: serverless.WebRTCAdmission{Admitted: true, VideoMuted: true, ExpiresAt: 1_800_003_600}}
	h, ctx := webrtcHost(c)

	out, err := h.WebRTCAdmission(ctx, "r", "u", "did-phone")

	if err != nil {
		t.Fatal(err)
	}
	if len(c.calls) != 1 || c.calls[0] != "admission|anchat|r|u|did-phone" {
		t.Fatalf("calls = %v", c.calls)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"room": "r", "user_id": "u", "device_id": "did-phone", "admitted": true, "revoked": false,
		"expired": false, "muted": false, "video_muted": true, "expires_at": float64(1_800_003_600)}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v (all: %v)", k, got[k], v, got)
		}
	}
}

func TestWebRTCHostCalls_controllerErrorsAreHostFunctionErrors(t *testing.T) {
	boom := errors.New("sfu unreachable")
	c := &recordingWebRTC{err: boom}
	h, ctx := webrtcHost(c)

	_, errAdmit := h.WebRTCAdmit(ctx, "r", "u", "", time.Minute)
	_, errAdmission := h.WebRTCAdmission(ctx, "r", "u", "")
	for name, err := range map[string]error{
		"admit":       errAdmit,
		"kick":        h.WebRTCKick(ctx, "r", "u"),
		"kick_device": h.WebRTCKickDevice(ctx, "r", "u", "d"),
		"mute":        h.WebRTCMute(ctx, "r", "u", true),
		"mute_video":  h.WebRTCMuteVideo(ctx, "r", "u", true),
		"admission":   errAdmission,
	} {
		var hfe *serverless.HostFunctionError
		if !errors.As(err, &hfe) || !errors.Is(err, boom) || hfe.Function != "webrtc_"+name {
			t.Errorf("%s: err = %v, want a webrtc_%s host function error wrapping the cause", name, err, name)
		}
	}
}

func TestWebRTCHostCalls_refuseWithoutAnInvocationOrAController(t *testing.T) {
	// No controller: WebRTC is not set up on this gateway.
	h := &HostFunctions{}
	ctx := invocationCtx(&serverless.InvocationContext{Namespace: "anchat"})
	if _, err := h.WebRTCAdmit(ctx, "r", "u", "", time.Minute); err == nil || !strings.Contains(err.Error(), "not set up") {
		t.Errorf("admit without a controller: %v", err)
	}
	if err := h.WebRTCKick(ctx, "r", "u"); err == nil {
		t.Error("kick without a controller succeeded")
	}
	if err := h.WebRTCMute(ctx, "r", "u", true); err == nil {
		t.Error("mute without a controller succeeded")
	}

	// A call outside any invocation says so rather than acting for nobody.
	c := &recordingWebRTC{}
	h, _ = webrtcHost(c)
	if err := h.WebRTCKick(context.Background(), "r", "u"); err == nil || len(c.calls) != 0 {
		t.Errorf("kick outside an invocation: err=%v calls=%v", err, c.calls)
	}
	empty := invocationCtx(&serverless.InvocationContext{})
	if _, err := h.WebRTCAdmit(empty, "r", "u", "", time.Minute); err == nil || len(c.calls) != 0 {
		t.Errorf("admit with no namespace: err=%v calls=%v", err, c.calls)
	}
}

// LOW-D: a runaway function cannot hammer the database and the SFUs.
func TestWebRTCHostCalls_budgetIsPerInvocation(t *testing.T) {
	c := &recordingWebRTC{}
	h, base := webrtcHost(c)
	ctx := serverless.WithWebRTCCounter(base)

	for i := 0; i < maxWebRTCCallsPerInvocation; i++ {
		if err := h.WebRTCKick(ctx, "r", "u"); err != nil {
			t.Fatalf("call %d within the budget failed: %v", i+1, err)
		}
	}
	_, errAdmit := h.WebRTCAdmit(ctx, "r", "u", "", time.Minute)
	_, errAdmission := h.WebRTCAdmission(ctx, "r", "u", "")
	for name, err := range map[string]error{
		"admit":       errAdmit,
		"kick":        h.WebRTCKick(ctx, "r", "u"),
		"kick_device": h.WebRTCKickDevice(ctx, "r", "u", "d"),
		"mute":        h.WebRTCMute(ctx, "r", "u", true),
		"mute_video":  h.WebRTCMuteVideo(ctx, "r", "u", true),
		"admission":   errAdmission,
	} {
		var hfe *serverless.HostFunctionError
		if !errors.As(err, &hfe) || !errors.Is(err, ErrWebRTCBudgetExceeded) || hfe.Function != "webrtc_"+name {
			t.Errorf("%s over the budget: err = %v, want a webrtc_%s host function error wrapping ErrWebRTCBudgetExceeded", name, err, name)
		}
	}
	if len(c.calls) != maxWebRTCCallsPerInvocation {
		t.Fatalf("the controller was called %d times, want exactly the budget, %d", len(c.calls), maxWebRTCCallsPerInvocation)
	}

	// A new invocation has a new budget; a path with no counter is not limited.
	if err := h.WebRTCKick(serverless.WithWebRTCCounter(base), "r", "u"); err != nil {
		t.Errorf("a fresh invocation was refused: %v", err)
	}
	if err := h.WebRTCKick(base, "r", "u"); err != nil {
		t.Errorf("a context with no counter was refused: %v", err)
	}
}
