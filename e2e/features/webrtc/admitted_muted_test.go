//go:build e2e_fleet

package webrtc

import (
	"errors"
	"testing"
	"time"

	"github.com/Orama-Network/orama/e2e/harness/eventually"
)

// feat-972: an admission that states the user's mutes
// (website/src/docs/developer/webrtc.mdx#admission, webrtc_admit_with_mutes).
// Runs inside TestAdmission_lifecycle, on its namespace, which requires
// admission.

// admittedMuted: a device admitted with both mutes, which joins and publishes
// audio and video at once, has nothing forwarded from its first packet with no
// mute call after the admission; webrtc_admission reports the mutes; the
// mutes are the user's, lifted by webrtc_mute and webrtc_mute_video.
func admittedMuted(t *testing.T, fx *fixture, caller string, bob rtcUser) {
	phone := newRTCDevices(t, fx.n, 1)[0]
	room := roomPrefix + "admitmuted-" + fx.n.Name
	admit(t, fx, caller, room, bob.subject, admitTTLSeconds)
	sub := rtcJoin(t, fx, bob, room, false)

	out := callRTC(t, fx, caller, map[string]any{"op": "admit_with_mutes", "room": room, "user": phone.subject,
		"device": phone.device, "ttl": admitTTLSeconds, "muted": true, "video_muted": true})
	if out["device_id"] != phone.device || out["muted"] != true || out["video_muted"] != true {
		t.Fatalf("webrtc_admit_with_mutes answered %v, want the phone admitted with both mutes", out)
	}
	if a := admissionOf(t, fx, caller, room, phone.subject, phone.device); a["admitted"] != true || a["muted"] != true || a["video_muted"] != true {
		t.Fatalf("webrtc_admission = %v, want admitted with both mutes", a)
	}

	pub := joinAudioVideo(t, fx, phone.rtcUser, room)
	publish := func() error {
		if err := pub.Publish(); err != nil {
			return err
		}
		return pub.PublishVideo()
	}
	// Publishing from the join on, so the first packets that can reach the SFU
	// are sent; the silence is watched from when the path is up.
	eventually.Require(t, publishEvery, mediaBudget, "the muted phone's connection to come up", func() (bool, error) {
		return pub.Connected(), publish()
	})
	err := eventually.Poll(t.Context(), publishEvery, silenceWindow, "a device admitted muted to show nothing", func() (bool, error) {
		if err := publish(); err != nil {
			return false, eventually.Stop(err)
		}
		return sub.Received() > 0, nil
	})
	var timeout *eventually.TimeoutError
	if err != nil && !errors.As(err, &timeout) {
		t.Fatal(err)
	}
	if got := sub.Received(); got != 0 {
		t.Fatalf("a device admitted muted had %d packets forwarded (%d video), want none from its first", got, sub.ReceivedVideo())
	}

	callRTC(t, fx, caller, map[string]any{"op": "mute", "room": room, "user": phone.subject, "muted": false})
	callRTC(t, fx, caller, map[string]any{"op": "mute_video", "room": room, "user": phone.subject, "muted": false})
	eventually.Require(t, time.Second, mediaBudget, "audio and video to flow once unmuted", func() (bool, error) {
		if err := publish(); err != nil {
			return false, err
		}
		return sub.ReceivedAudio() > 0 && sub.ReceivedVideo() > 0, nil
	})
}
