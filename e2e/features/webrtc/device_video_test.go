//go:build e2e_fleet

package webrtc

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/e2e/features/internal/services"
	"github.com/Orama-Network/orama/e2e/harness"
	"github.com/Orama-Network/orama/e2e/harness/eventually"
	"github.com/Orama-Network/orama/e2e/harness/ns"
	"github.com/Orama-Network/orama/e2e/harness/wallet"
)

// feat-964: a forced mute of a user's video, apart from their audio
// (website/src/docs/developer/webrtc.mdx#kick-and-mute), and admission, kick and
// check of one device of a user, named by the device id of its session
// (website/src/docs/developer/webrtc.mdx#admitting-one-device). Both run inside
// TestAdmission_lifecycle, on its namespace, which requires admission.

// rtcDevice is one device-bound session of an account.
type rtcDevice struct {
	rtcUser
	device string // the device id the gateway put in the session's did
}

// newRTCDevices signs one runtime member of the namespace in from n devices,
// each with a key of its own, so each session is bound to its own device.
func newRTCDevices(t *testing.T, n *ns.Namespace, count int) []rtcDevice {
	t.Helper()
	w, err := wallet.NewEVM()
	if err != nil {
		t.Fatal(err)
	}
	n.CLI.MustOK(t, "members", "add", w.Address(), "--role", "runtime")
	out := make([]rtcDevice, 0, count)
	for i := 0; i < count; i++ {
		dev, err := wallet.NewEd25519Device()
		if err != nil {
			t.Fatal(err)
		}
		s, err := harness.GW(t).For(t).SignIn(t.Context(), w, n.Name, dev)
		if err != nil {
			t.Fatal(err)
		}
		if s.DeviceID != dev.ID() {
			t.Fatalf("sign-in from device %s returned a session bound to %q", dev.ID(), s.DeviceID)
		}
		out = append(out, rtcDevice{rtcUser: rtcUser{subject: s.Subject, token: s.AccessToken}, device: dev.ID()})
	}
	return out
}

// admitDevice admits device of user to room through the fixture.
func admitDevice(t *testing.T, fx *fixture, by, room, user, device string) {
	t.Helper()
	out := callRTC(t, fx, by, map[string]any{"op": "admit", "room": room, "user": user, "device": device, "ttl": admitTTLSeconds})
	if out["device_id"] != device {
		t.Fatalf("admit answered %v, want device %q", out, device)
	}
}

// admissionOf reads what the namespace has on record for device of user in room.
func admissionOf(t *testing.T, fx *fixture, by, room, user, device string) map[string]any {
	t.Helper()
	return callRTC(t, fx, by, map[string]any{"op": "admission", "room": room, "user": user, "device": device})
}

// expectVideoSilence publishes audio and video from pub for silenceWindow and
// fails if sub receives video, or stops receiving audio, once the packets in
// flight when the mute landed have arrived.
func expectVideoSilence(t *testing.T, pub, sub *services.RTCPeer) {
	t.Helper()
	last := int64(-1)
	eventually.Require(t, settleEvery, mediaBudget, "the video packets in flight to arrive", func() (bool, error) {
		got := sub.ReceivedVideo()
		settled := got == last
		last = got
		return settled, nil
	})
	video, audio := sub.ReceivedVideo(), sub.ReceivedAudio()
	leaked := false
	err := eventually.Poll(t.Context(), publishEvery, silenceWindow, "a video-muted publisher to show nothing", func() (bool, error) {
		if err := pub.Publish(); err != nil {
			return false, eventually.Stop(err)
		}
		if err := pub.PublishVideo(); err != nil {
			return false, eventually.Stop(err)
		}
		leaked = sub.ReceivedVideo() > video
		return leaked, nil
	})
	var timeout *eventually.TimeoutError
	if err != nil && !errors.As(err, &timeout) {
		t.Fatal(err)
	}
	if leaked {
		t.Fatalf("a video-muted publisher's video still reached the room: %d packets", sub.ReceivedVideo()-video)
	}
	if sub.ReceivedAudio() <= audio {
		t.Fatal("the video mute also stopped the publisher's audio")
	}
}

// joinAudioVideo joins room as u, publishing a microphone and a camera.
func joinAudioVideo(t *testing.T, fx *fixture, u rtcUser, room string) *services.RTCPeer {
	t.Helper()
	p, err := services.JoinRoom(t.Context(), fx.c, u.token, room, u.subject)
	if err != nil {
		t.Fatalf("%s joining %s: %v", u.subject, room, err)
	}
	t.Cleanup(p.Close)
	if err := p.StartAudioVideo(); err != nil {
		t.Fatal(err)
	}
	return p
}

// videoMuteHolds: webrtc_mute_video stops the user's video on the SFU, leaves
// their audio flowing, is announced as a forced participant-state of kind
// video, holds through a rejoin, and is lifted by the unmute.
func videoMuteHolds(t *testing.T, fx *fixture, caller string, alice, bob rtcUser) {
	room := roomPrefix + "vmute-" + fx.n.Name
	admit(t, fx, caller, room, alice.subject, admitTTLSeconds)
	admit(t, fx, caller, room, bob.subject, admitTTLSeconds)
	pub, sub := joinAudioVideo(t, fx, alice, room), rtcJoin(t, fx, bob, room, false)
	eventually.Require(t, time.Second, mediaBudget, "alice's audio and video to reach bob", func() (bool, error) {
		if err := pub.Publish(); err != nil {
			return false, err
		}
		if err := pub.PublishVideo(); err != nil {
			return false, err
		}
		return sub.ReceivedAudio() > 0 && sub.ReceivedVideo() > 0, nil
	})

	callRTC(t, fx, caller, map[string]any{"op": "mute_video", "room": room, "user": alice.subject, "muted": true})
	waitFrame(t, sub, "alice's video forced off", func(m services.SignalMsg) bool {
		return forcedState(m, alice.subject, "video", false)
	})
	expectVideoSilence(t, pub, sub)

	pub.Close()
	pub = joinAudioVideo(t, fx, alice, room) // the mute holds against a client that starts over
	expectVideoSilence(t, pub, sub)

	callRTC(t, fx, caller, map[string]any{"op": "mute_video", "room": room, "user": alice.subject, "muted": false})
	before := sub.ReceivedVideo()
	eventually.Require(t, time.Second, mediaBudget, "video to flow again after the unmute", func() (bool, error) {
		if err := pub.PublishVideo(); err != nil {
			return false, err
		}
		return sub.ReceivedVideo() > before, nil
	})
}

// forcedState reports whether m is a forced participant-state of user's kind
// media set to enabled.
func forcedState(m services.SignalMsg, user, kind string, enabled bool) bool {
	var s struct {
		UserID  string `json:"userId"`
		Kind    string `json:"kind"`
		Enabled bool   `json:"enabled"`
		Forced  bool   `json:"forced"`
	}
	return m.Type == services.MsgParticipantState && json.Unmarshal(m.Data, &s) == nil &&
		strings.EqualFold(s.UserID, user) && s.Kind == kind && s.Enabled == enabled && s.Forced
}

// deviceAdmission: an application that records the device id of its device's
// session (get_caller_device_id) when the device registers admits, checks and
// kicks that device alone; the account's other device is refused until it is
// admitted, and stays when the first is kicked.
func deviceAdmission(t *testing.T, fx *fixture, caller, admin string) {
	devs := newRTCDevices(t, fx.n, 2)
	phone, tablet := devs[0], devs[1]
	user := phone.subject
	room := roomPrefix + "device-" + fx.n.Name
	watch := watchMembership(t, fx, admin, room)

	// The application learns its device's id from the device's own session.
	if got := callRTC(t, fx, phone.token, map[string]any{"op": "whoami"})["device_id"]; got != phone.device {
		t.Fatalf("get_caller_device_id = %v, want the phone's session device %s", got, phone.device)
	}

	admitDevice(t, fx, caller, room, user, phone.device)
	p := rtcJoin(t, fx, phone.rtcUser, room, false)
	if ev := watch("webrtc.join", user); ev.DeviceID != phone.device {
		t.Fatalf("join event = %+v, want the phone", ev)
	}
	expectRefused(t, fx, tablet.token, room, "WEBRTC_ADMISSION_REQUIRED")
	if a := admissionOf(t, fx, caller, room, user, phone.device); a["admitted"] != true {
		t.Fatalf("the phone's admission = %v, want admitted", a)
	}
	if a := admissionOf(t, fx, caller, room, user, tablet.device); a["admitted"] != false || a["revoked"] != false {
		t.Fatalf("the tablet's admission = %v, want not admitted and not revoked", a)
	}

	admitDevice(t, fx, caller, room, user, tablet.device)
	tp := rtcJoin(t, fx, tablet.rtcUser, room, false)
	watch("webrtc.join", user)

	callRTC(t, fx, caller, map[string]any{"op": "kick_device", "room": room, "user": user, "device": phone.device})
	waitFrame(t, p, "the device kick to close the phone's socket", func(m services.SignalMsg) bool { return m.Type == services.MsgKicked })
	if ev := watch("webrtc.leave", user); ev.Reason != "kicked" || ev.DeviceID != phone.device {
		t.Fatalf("leave event = %+v, want the phone, kicked", ev)
	}
	waitFrame(t, tp, "the tablet, still in the room, to see the phone leave", func(m services.SignalMsg) bool {
		return m.Type == services.MsgParticipantLeft && strings.Contains(string(m.Data), p.PeerID)
	})
	expectRefused(t, fx, phone.token, room, "WEBRTC_ADMISSION_REVOKED")
	if a := admissionOf(t, fx, caller, room, user, phone.device); a["admitted"] != false || a["revoked"] != true {
		t.Fatalf("the kicked phone's admission = %v, want revoked", a)
	}
	if a := admissionOf(t, fx, caller, room, user, tablet.device); a["admitted"] != true {
		t.Fatalf("the tablet's admission after the phone's kick = %v, want admitted", a)
	}

	// A device kick outranks an older admission of every device of the user,
	// and a newer one lets the device back in.
	tp.Close()
	watch("webrtc.leave", user)
	admit(t, fx, caller, room, user, admitTTLSeconds)
	callRTC(t, fx, caller, map[string]any{"op": "kick_device", "room": room, "user": user, "device": tablet.device})
	expectRefused(t, fx, tablet.token, room, "WEBRTC_ADMISSION_REVOKED")
	rtcJoin(t, fx, phone.rtcUser, room, false).Close()
}
