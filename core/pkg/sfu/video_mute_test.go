package sfu

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/sfu/ctrlauth"
	"github.com/gorilla/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

// feat-964: a namespace stops a user's video (camera and screen) apart from
// their audio, forced, held across a rejoin, and announced as kind "video".

func muteVideo(t *testing.T, s *Server, room, user string, muted bool, atMs int64) int {
	t.Helper()
	return affected(t, control(t, s, ctrlauth.VideoMutePath, ctrlauth.MuteRequest{Room: room, UserID: user, Muted: muted, AtMs: atMs}))
}

// readForcedState reads up to the next participant-state and decodes it.
func readForcedState(t *testing.T, conn *websocket.Conn) ParticipantStateData {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	m := readType(t, conn, MessageTypeParticipantState)
	var st ParticipantStateData
	if err := json.Unmarshal(m.Data, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestMediaOf_audioAndVideoHaveTheirOwnFlag(t *testing.T) {
	for kind, want := range map[webrtc.RTPCodecType]media{webrtc.RTPCodecTypeAudio: mediaAudio, webrtc.RTPCodecTypeVideo: mediaVideo} {
		if got, ok := mediaOf(kind); !ok || got != want || got.String() != kind.String() {
			t.Errorf("mediaOf(%s) = %v %v, want %v named %q", kind, got, ok, want, kind.String())
		}
	}
	if _, ok := mediaOf(webrtc.RTPCodecType(0)); ok {
		t.Error("an unknown kind was given a mute")
	}
	p := NewPeer("u", nil, nil, testLogger())
	p.mutedFlag(mediaVideo).Store(true)
	if p.muted.Load() || !p.videoMuted.Load() {
		t.Fatal("muting video touched the audio flag, or missed the video one")
	}
}

func TestVideoMute_marksOnlyTheVideoAndTellsTheRoomAsVideo(t *testing.T) {
	s := newAuthServer(t)
	joinAs(t, s, testTicket(t, s, "r1", "alice"), "")
	bob, _ := joinAs(t, s, testTicket(t, s, "r1", "bob"), "")

	if n := muteVideo(t, s, "r1", "alice", true, nowMs()); n != 1 {
		t.Fatalf("affected = %d, want 1", n)
	}
	p := peerOf(s, "r1", "alice")
	if !p.videoMuted.Load() || p.muted.Load() {
		t.Fatalf("after a video mute: video muted %v, audio muted %v; want only the video", p.videoMuted.Load(), p.muted.Load())
	}
	if st := readForcedState(t, bob); st.UserID != "alice" || st.Kind != "video" || st.Enabled || !st.Forced {
		t.Fatalf("state = %+v, want alice's video disabled, forced", st)
	}

	muteVideo(t, s, "r1", "alice", false, nowMs()+1000)
	if p.videoMuted.Load() {
		t.Fatal("unmuting the video did not clear it")
	}
	if st := readForcedState(t, bob); st.Kind != "video" || !st.Enabled || !st.Forced {
		t.Fatalf("state = %+v, want alice's video enabled, forced", st)
	}
}

func TestVideoMute_andAudioMuteAreIndependent(t *testing.T) {
	s := newAuthServer(t)
	joinAs(t, s, testTicket(t, s, "r1", "alice"), "")
	at := nowMs()

	muteUser(t, s, "r1", "alice", true, at)
	muteVideo(t, s, "r1", "alice", false, at+1) // a video unmute, newer, must not lift the audio mute
	p := peerOf(s, "r1", "alice")
	if !p.muted.Load() || p.videoMuted.Load() {
		t.Fatalf("audio muted %v video muted %v, want audio only", p.muted.Load(), p.videoMuted.Load())
	}
}

func TestVideoMute_aTicketCarriesItFromTheStart(t *testing.T) {
	s := newAuthServer(t)
	tk := testTicket(t, s, "r1", "alice")
	tk.VideoMuted = true
	joinAs(t, s, tk, "")
	if p := peerOf(s, "r1", "alice"); !p.videoMuted.Load() || p.muted.Load() {
		t.Fatal("a ticket's video mute was not applied at the join, or muted the audio")
	}
}

// A ticket minted before the video mute is a stale snapshot; the log corrects it.
func TestVideoMute_rejoinWithAPreMuteTicketStaysVideoMuted(t *testing.T) {
	s := newAuthServer(t)
	stale := testTicket(t, s, "r1", "alice")
	stale.IssuedAtMs = nowMs() - 1000

	muteVideo(t, s, "r1", "alice", true, nowMs()) // nobody of alice's is in the room
	conn, _ := joinAs(t, s, stale, "")
	if st := readForcedState(t, conn); st.Kind != "video" || st.Enabled || !st.Forced {
		t.Fatalf("correction = %+v, want the video forced off", st)
	}
	if p := peerOf(s, "r1", "alice"); !p.videoMuted.Load() || p.muted.Load() {
		t.Fatal("a user video-muted after its ticket was issued rejoined with video, or lost its audio")
	}
}

// Video resumed mid-stream is undecodable until a keyframe: the unmute asks the
// publisher for one, and the mute does not.
func TestVideoMute_unmuteAsksThePublisherForAKeyframe(t *testing.T) {
	room, got, mu := roomWithPublisher(t, newKeyframeLimiter(time.Millisecond))
	pub := room.snapshotPeers("")[0]

	room.setPeerMuted(pub, mediaVideo, true)
	mu.Lock()
	sentOnMute := len(*got)
	mu.Unlock()
	room.setPeerMuted(pub, mediaVideo, false)

	mu.Lock()
	defer mu.Unlock()
	if sentOnMute != 0 {
		t.Errorf("the mute sent %d RTCP packets, want none", sentOnMute)
	}
	if len(*got) != 1 {
		t.Fatalf("the unmute sent %d RTCP packets, want one PLI", len(*got))
	}
	if pli, ok := (*got)[0].(*rtcp.PictureLossIndication); !ok || pli.MediaSSRC != 4242 {
		t.Fatalf("publisher got %#v, want a PLI for its video SSRC", (*got)[0])
	}
}
