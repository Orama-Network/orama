package webrtc

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/serverless"
)

// feat-972: an admission states the user's mutes, recorded in the same
// transaction as the admission, so a device admitted muted is muted from its
// first packet with no mute call after the admission.

var bothMuted = serverless.WebRTCMutes{Muted: true, VideoMuted: true}

func TestAdmissionStore_admitWithMutesRecordsThemWithTheAdmission(t *testing.T) {
	s, _, _ := newSQLiteStore(t)
	ctx := context.Background()

	if _, err := s.AdmitWithMutes(ctx, "ns", "r1", "alice", "did-phone", time.Hour, bothMuted); err != nil {
		t.Fatal(err)
	}
	if a, err := s.Lookup(ctx, "ns", "r1", "alice", "did-phone"); err != nil || !a.Valid || !a.Muted || !a.VideoMuted {
		t.Fatalf("the device's first admission: %+v, %v, want admitted with both media muted", a, err)
	}
	if a, _ := s.Lookup(ctx, "ns", "r1", "bob", ""); a.Muted || a.VideoMuted {
		t.Fatalf("another user took the mutes: %+v", a)
	}
}

func TestAdmissionStore_aPlainReadmissionKeepsTheStatedMutes(t *testing.T) {
	s, _, _ := newSQLiteStore(t)
	ctx := context.Background()
	s.AdmitWithMutes(ctx, "ns", "r1", "alice", "did-phone", time.Hour, serverless.WebRTCMutes{Muted: true})

	if _, err := s.Admit(ctx, "ns", "r1", "alice", "did-phone", 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", "did-phone"); !a.Valid || !a.Muted || a.VideoMuted {
		t.Fatalf("after webrtc_admit again: %+v, want the audio still muted and the video not", a)
	}
}

// The mutes are the user's, as webrtc_mute's are: stating them on one device's
// admission sets them on every admission of the user in the room, so the
// admission for every device cannot keep a device muted the call unmuted.
func TestAdmissionStore_statedMutesApplyToEveryAdmissionOfTheUser(t *testing.T) {
	s, _, _ := newSQLiteStore(t)
	ctx := context.Background()
	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour)
	s.SetMuted(ctx, "ns", "r1", "alice", MediaAudio, true)
	s.SetMuted(ctx, "ns", "r1", "alice", MediaVideo, true)

	if _, err := s.AdmitWithMutes(ctx, "ns", "r1", "alice", "did-phone", time.Hour, serverless.WebRTCMutes{}); err != nil {
		t.Fatal(err)
	}
	for _, device := range []string{"did-phone", "did-laptop"} {
		if a, _ := s.Lookup(ctx, "ns", "r1", "alice", device); !a.Valid || a.Muted || a.VideoMuted {
			t.Errorf("%s after an unmuted admission: %+v, want admitted and unmuted", device, a)
		}
	}
	if _, err := s.AdmitWithMutes(ctx, "ns", "r1", "alice", "did-phone", time.Hour, serverless.WebRTCMutes{VideoMuted: true}); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", "did-laptop"); a.Muted || !a.VideoMuted {
		t.Errorf("the user's other device: %+v, want the video muted and the audio not", a)
	}
}

// On a real rqlited: the upsert with the mutes and the update of the user's
// other admissions commit as one batch.
func TestAdmissionStore_admitWithMutesOnRqlite(t *testing.T) {
	s := newRqliteStore(t)
	ctx := context.Background()
	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour)

	if _, err := s.AdmitWithMutes(ctx, "ns", "r1", "alice", "did-phone", time.Hour, bothMuted); err != nil {
		t.Fatal(err)
	}
	for _, device := range []string{"did-phone", "did-laptop"} {
		if a, err := s.Lookup(ctx, "ns", "r1", "alice", device); err != nil || !a.Valid || !a.Muted || !a.VideoMuted {
			t.Errorf("%s: %+v, %v, want admitted with both media muted", device, a, err)
		}
	}
}

func TestAdmissionStore_staleMutesAreTheMediaAnSFUMayHoldOtherwise(t *testing.T) {
	s, _, _ := newSQLiteStore(t)
	ctx := context.Background()
	stale := func(user string, mutes serverless.WebRTCMutes) []Media {
		t.Helper()
		m, err := s.StaleMutes(ctx, "ns", "r1", user, mutes)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}

	s.SetRequireAdmission(ctx, "ns", true)
	if got := stale("alice", bothMuted); len(got) != 0 {
		t.Errorf("a user never admitted where admission is required has no ticket or connection; stale = %v, want none", got)
	}

	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour)
	if got := stale("alice", serverless.WebRTCMutes{}); len(got) != 0 {
		t.Errorf("unmuted on record and stated unmuted: stale = %v, want none", got)
	}
	if got := stale("alice", serverless.WebRTCMutes{VideoMuted: true}); !slices.Equal(got, []Media{MediaVideo}) {
		t.Errorf("stated a video mute over an unmuted admission: stale = %v, want the video", got)
	}
	s.AdmitWithMutes(ctx, "ns", "r1", "alice", "did-phone", time.Hour, bothMuted)
	if got := stale("alice", bothMuted); len(got) != 0 {
		t.Errorf("stated what is on record: stale = %v, want none", got)
	}
	s.Admit(ctx, "ns", "r1", "alice", "did-tablet", time.Hour) // a plain admission of a new device starts unmuted
	if got := stale("alice", bothMuted); !slices.Equal(got, []Media{MediaAudio, MediaVideo}) {
		t.Errorf("one admission of the user holds another state: stale = %v, want both", got)
	}
}

func TestAdmissionStore_staleMutesWithoutRequiredAdmissionAreBothMedia(t *testing.T) {
	s, _, _ := newSQLiteStore(t)
	got, err := s.StaleMutes(context.Background(), "ns", "r1", "alice", serverless.WebRTCMutes{})
	if err != nil || !slices.Equal(got, []Media{MediaAudio, MediaVideo}) {
		t.Fatalf("stale = %v, %v: a user with no admission may be connected in any state where admission is not required", got, err)
	}
}

// The acceptance: admitted muted, a device that connects at once joins on a
// ticket that mutes both media, with no mute call and no call to an SFU.
func TestAdmitWithMutes_theFirstJoinsTicketCarriesTheMutes(t *testing.T) {
	_, dir := threeSFUs(t) // they answer no control route: a control call would fail the admission
	var target string
	h := gatewayFor(t, dir, &target)
	h.SetNamespace("ns")
	ctx := httptest.NewRequest("GET", "/", nil).Context()
	h.admissions.SetRequireAdmission(ctx, "ns", true)

	if _, err := h.AdmitWithMutes(ctx, "ns", "r1", testUser, "did-phone", time.Hour, bothMuted); err != nil {
		t.Fatalf("the first admission called an SFU, or failed: %v", err)
	}
	req, w := signalAs(h, "r1", testUser, "did-phone")
	if tk := openTicketOf(t, req); w.Code != 200 || !tk.Muted || !tk.VideoMuted {
		t.Fatalf("status %d ticket %+v, want a ticket that mutes the audio and the video", w.Code, tk)
	}
	adm, err := h.AdmissionOf(ctx, "ns", "r1", testUser, "did-phone")
	if err != nil || !adm.Admitted || !adm.Muted || !adm.VideoMuted {
		t.Fatalf("webrtc_admission: %+v, %v, want admitted with the stated mutes", adm, err)
	}
}

func TestAdmitWithMutes_noMutesBehavesAsAdmit(t *testing.T) {
	sfu := newControlSFU(t, "a")
	h := controllerFor(t, sfu)
	h.admissions.SetRequireAdmission(bg, "ns", true)

	if _, err := h.AdmitWithMutes(bg, "ns", "r1", "alice", "", time.Hour, serverless.WebRTCMutes{}); err != nil {
		t.Fatal(err)
	}
	if a, _ := h.admissions.Lookup(bg, "ns", "r1", "alice", ""); !a.Valid || a.Muted || a.VideoMuted {
		t.Fatalf("record = %+v, want admitted and unmuted", a)
	}
	if len(sfu.mutes)+len(sfu.videoMutes) != 0 {
		t.Fatalf("SFU told %+v %+v, want nothing", sfu.mutes, sfu.videoMutes)
	}
}

// A user admitted before, unmuted, may hold a ticket or a connection: the SFU
// is told the stated mutes before they are recorded, and only the media whose
// state changes.
func TestAdmitWithMutes_tellsTheSFUOfAChangeBeforeRecordingIt(t *testing.T) {
	sfu := newControlSFU(t, "a")
	h := controllerFor(t, sfu)
	h.admissions.SetRequireAdmission(bg, "ns", true)
	h.Admit(bg, "ns", "r1", "alice", "", time.Hour)

	if _, err := h.AdmitWithMutes(bg, "ns", "r1", "alice", "", time.Hour, serverless.WebRTCMutes{Muted: true}); err != nil {
		t.Fatal(err)
	}
	if len(sfu.mutes) != 1 || !sfu.mutes[0].Muted || sfu.mutes[0].UserID != "alice" || sfu.mutes[0].Room != "r1" || len(sfu.videoMutes) != 0 {
		t.Fatalf("SFU audio %+v video %+v, want one audio mute", sfu.mutes, sfu.videoMutes)
	}

	if _, err := h.AdmitWithMutes(bg, "ns", "r1", "alice", "", time.Hour, serverless.WebRTCMutes{Muted: true}); err != nil {
		t.Fatal(err)
	}
	if len(sfu.mutes) != 1 {
		t.Fatalf("restating the recorded mutes called the SFU again: %+v", sfu.mutes)
	}
}

func TestAdmitWithMutes_anSFUThatCannotBeToldLeavesNothingRecorded(t *testing.T) {
	sfu := newControlSFU(t, "a")
	h := controllerFor(t, sfu)
	h.Admit(bg, "ns", "r1", "alice", "", time.Minute)
	before, _ := h.admissions.Lookup(bg, "ns", "r1", "alice", "")
	sfu.mu.Lock()
	sfu.status = 500
	sfu.mu.Unlock()

	_, err := h.AdmitWithMutes(bg, "ns", "r1", "alice", "", time.Hour, bothMuted)

	if err == nil || !strings.Contains(err.Error(), "not recorded") {
		t.Fatalf("err = %v, want the admission refused as not recorded", err)
	}
	if a, _ := h.admissions.Lookup(bg, "ns", "r1", "alice", ""); a.Muted || a.VideoMuted || a.ValidUntil != before.ValidUntil {
		t.Fatalf("record = %+v, want it as it was (%+v)", a, before)
	}
}

func TestAdmitWithMutes_refusesWhatAdmitRefuses(t *testing.T) {
	h := controllerFor(t, newControlSFU(t, "a"))
	for name, ttl := range map[string]time.Duration{"zero ttl": 0, "beyond the cap": MaxAdmissionTTL + time.Second} {
		if _, err := h.AdmitWithMutes(bg, "ns", "r1", "alice", "", ttl, bothMuted); err == nil {
			t.Errorf("%s: admitted", name)
		}
	}
	if _, err := h.AdmitWithMutes(bg, "other", "r1", "alice", "", time.Hour, bothMuted); err == nil {
		t.Error("admitted in another namespace")
	}
}
