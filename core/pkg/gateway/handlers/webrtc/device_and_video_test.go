package webrtc

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// feat-964: a function admits, kicks and reads one device of a user, named by
// the device id of its session; and mutes a user's video apart from their audio.

func TestJudge_aNewerRevocationOutranksAnOlderAdmissionAndANewerAdmissionOutranksIt(t *testing.T) {
	now := int64(1000)
	kicked := judge([]admissionRow{
		{DeviceID: "", ExpiresAt: 2000, Gen: 1},
		{DeviceID: "phone", ExpiresAt: 2000, RevokedAt: 900, Gen: 2},
	}, now)
	if kicked.Valid || !kicked.Revoked {
		t.Fatalf("a device kicked after the user was admitted from every device: %+v, want refused as revoked", kicked)
	}
	readmitted := judge([]admissionRow{
		{DeviceID: "", ExpiresAt: 2000, Gen: 3},
		{DeviceID: "phone", ExpiresAt: 2000, RevokedAt: 900, Gen: 2},
	}, now)
	if !readmitted.Valid || readmitted.Generation != 3 {
		t.Fatalf("the user admitted from every device after the kick: %+v, want valid at generation 3", readmitted)
	}
	legacy := judge([]admissionRow{
		{DeviceID: "", ExpiresAt: 2000},
		{DeviceID: "phone", ExpiresAt: 2000, RevokedAt: 900},
	}, now)
	if !legacy.Valid {
		t.Fatalf("rows that predate generations: %+v, want any valid one to admit, as before", legacy)
	}
}

func TestAdmissionStore_aDeviceAdmissionAdmitsThatDeviceAndNoOtherOfTheAccount(t *testing.T) {
	s, _, _ := newSQLiteStore(t)
	ctx := context.Background()
	s.Admit(ctx, "ns", "r1", "alice", "did-phone", time.Hour)

	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", "did-phone"); !a.Valid {
		t.Fatalf("the admitted device: %+v", a)
	}
	for _, device := range []string{"did-tablet", ""} {
		if a, _ := s.Lookup(ctx, "ns", "r1", "alice", device); a.Valid {
			t.Errorf("device %q of the same account was admitted by the phone's admission", device)
		}
	}
}

func TestAdmissionStore_revokeDeviceRefusesThatDeviceDespiteAnAdmissionForEveryDevice(t *testing.T) {
	s, _, _ := newSQLiteStore(t)
	ctx := context.Background()
	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour) // generation 1

	gen, err := s.RevokeDevice(ctx, "ns", "r1", "alice", "did-phone")
	if err != nil || gen != 2 {
		t.Fatalf("RevokeDevice = %d, %v, want generation 2", gen, err)
	}
	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", "did-phone"); a.Valid || !a.Revoked {
		t.Fatalf("the kicked device: %+v, want refused as revoked", a)
	}
	for _, device := range []string{"did-tablet", ""} {
		if a, _ := s.Lookup(ctx, "ns", "r1", "alice", device); !a.Valid {
			t.Errorf("device %q lost its admission to a kick of the phone: %+v", device, a)
		}
	}

	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour)
	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", "did-phone"); !a.Valid || a.Generation <= gen {
		t.Fatalf("after admitting every device again: %+v, want valid above generation %d", a, gen)
	}
	gen, _ = s.RevokeDevice(ctx, "ns", "r1", "alice", "did-phone")
	s.Admit(ctx, "ns", "r1", "alice", "did-phone", time.Hour)
	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", "did-phone"); !a.Valid || a.Generation <= gen {
		t.Fatalf("after admitting the device again: %+v, want valid above generation %d", a, gen)
	}
}

// The purge keeps a device's revocation as long as the admission for every
// device it outranks: removed sooner, the device would be let back in.
func TestAdmissionStore_aDeviceRevocationOutlivesThePurgeWhileTheUsersAdmissionLasts(t *testing.T) {
	s, _, now := newSQLiteStore(t)
	ctx := context.Background()
	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour)
	s.RevokeDevice(ctx, "ns", "r1", "alice", "did-phone")

	*now = now.Add(30 * time.Minute) // far beyond generationRetention
	s.Admit(ctx, "ns", "other", "bob", "", time.Hour)
	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", "did-phone"); a.Valid {
		t.Fatal("the purge removed the device's revocation while the user's admission still lets every device in")
	}
}

func TestAdmissionStore_revokeDeviceCarriesTheUsersMutes(t *testing.T) {
	s, db, _ := newSQLiteStore(t)
	ctx := context.Background()
	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour)
	s.SetMuted(ctx, "ns", "r1", "alice", MediaVideo, true)
	s.RevokeDevice(ctx, "ns", "r1", "alice", "did-phone")

	var audio, video int
	if err := db.QueryRow(`SELECT muted, video_muted FROM webrtc_admissions WHERE device_id = 'did-phone'`).Scan(&audio, &video); err != nil {
		t.Fatal(err)
	}
	if audio != 0 || video != 1 {
		t.Fatalf("the device's row carries muted=%d video_muted=%d, want the user's 0 and 1", audio, video)
	}
}

func TestAdmissionStore_videoMuteIsApartFromTheAudioAndSurvivesReadmission(t *testing.T) {
	s, _, _ := newSQLiteStore(t)
	ctx := context.Background()
	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour)
	if err := s.SetMuted(ctx, "ns", "r1", "alice", MediaVideo, true); err != nil {
		t.Fatal(err)
	}
	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour)
	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", ""); !a.VideoMuted || a.Muted {
		t.Fatalf("after a video mute and a readmission: %+v, want the video muted and the audio not", a)
	}
	s.SetMuted(ctx, "ns", "r1", "alice", MediaAudio, true)
	s.SetMuted(ctx, "ns", "r1", "alice", MediaVideo, false)
	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", ""); a.VideoMuted || !a.Muted {
		t.Fatalf("after muting the audio and unmuting the video: %+v", a)
	}
}

func TestAdmissionStore_aVideoMutedRowIsKeptForTheMuteRetention(t *testing.T) {
	s, _, now := newSQLiteStore(t)
	ctx := context.Background()
	s.Admit(ctx, "ns", "r1", "alice", "", time.Second)
	s.SetMuted(ctx, "ns", "r1", "alice", MediaVideo, true)

	*now = now.Add(mutedRetention / 2)
	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour) // purges what it may, then readmits
	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", ""); !a.VideoMuted {
		t.Fatal("readmitting a video-muted user inside the retention unmuted their video")
	}
}

func TestKickDevice_revokesTheDeviceAndClosesItsConnectionOnTheSFUs(t *testing.T) {
	sfu := newControlSFU(t, "a")
	h := controllerFor(t, sfu)
	h.Admit(bg, "ns", "r1", "alice", "did-phone", time.Hour)
	h.Admit(bg, "ns", "r1", "alice", "did-tablet", time.Hour) // generation 2

	if err := h.KickDevice(bg, "ns", "r1", "alice", "did-phone"); err != nil {
		t.Fatal(err)
	}
	if a, _ := h.admissions.Lookup(bg, "ns", "r1", "alice", "did-phone"); a.Valid {
		t.Fatal("the kicked device is still admitted")
	}
	if a, _ := h.admissions.Lookup(bg, "ns", "r1", "alice", "did-tablet"); !a.Valid {
		t.Fatal("the user's other device lost its admission")
	}
	if len(sfu.kicks) != 0 || len(sfu.deviceKicks) != 1 {
		t.Fatalf("SFU user kicks %+v device kicks %+v, want one device kick", sfu.kicks, sfu.deviceKicks)
	}
	if k := sfu.deviceKicks[0]; k.Room != "r1" || k.UserID != "alice" || k.DeviceID != "did-phone" || k.AdmitGen != 3 || k.AtMs == 0 {
		t.Fatalf("device kick = %+v, want the phone, generation 3", k)
	}
}

func TestKickDevice_refusesNoDevice(t *testing.T) {
	sfu := newControlSFU(t, "a")
	h := controllerFor(t, sfu)
	for _, device := range []string{"", strings.Repeat("d", maxIdentityLen+1)} {
		if err := h.KickDevice(bg, "ns", "r1", "alice", device); err == nil {
			t.Errorf("a device kick naming %d bytes was accepted", len(device))
		}
	}
	if len(sfu.deviceKicks) != 0 {
		t.Fatal("a refused device kick reached the SFU")
	}
}

func TestMuteVideo_isRecordedAndSentOnTheVideoRoute(t *testing.T) {
	sfu := newControlSFU(t, "a")
	h := controllerFor(t, sfu)
	h.Admit(bg, "ns", "r1", "alice", "", time.Hour)

	if err := h.MuteVideo(bg, "ns", "r1", "alice", true); err != nil {
		t.Fatal(err)
	}
	if a, _ := h.admissions.Lookup(bg, "ns", "r1", "alice", ""); !a.VideoMuted || a.Muted {
		t.Fatalf("record = %+v, want the video muted and the audio not", a)
	}
	if len(sfu.mutes) != 0 || len(sfu.videoMutes) != 1 || !sfu.videoMutes[0].Muted || sfu.videoMutes[0].UserID != "alice" {
		t.Fatalf("SFU audio mutes %+v video mutes %+v, want one video mute", sfu.mutes, sfu.videoMutes)
	}
}

func TestAdmissionOf_isTheVerdictAJoinFromThatDeviceGets(t *testing.T) {
	sfu := newControlSFU(t, "a")
	h := controllerFor(t, sfu)
	h.Admit(bg, "ns", "r1", "alice", "did-phone", time.Hour)
	h.MuteVideo(bg, "ns", "r1", "alice", true)

	phone, err := h.AdmissionOf(bg, "ns", "r1", "alice", "did-phone")
	if err != nil || !phone.Admitted || phone.Revoked || phone.Expired || !phone.VideoMuted || phone.ExpiresAt == 0 {
		t.Fatalf("the admitted phone: %+v, %v", phone, err)
	}
	if tablet, _ := h.AdmissionOf(bg, "ns", "r1", "alice", "did-tablet"); tablet.Admitted || tablet.Revoked || tablet.Expired {
		t.Fatalf("a device never admitted: %+v, want not admitted with no reason", tablet)
	}
	h.KickDevice(bg, "ns", "r1", "alice", "did-phone")
	if phone, _ = h.AdmissionOf(bg, "ns", "r1", "alice", "did-phone"); phone.Admitted || !phone.Revoked || phone.ExpiresAt != 0 {
		t.Fatalf("the kicked phone: %+v, want revoked", phone)
	}
	if _, err := h.AdmissionOf(bg, "other", "r1", "alice", ""); err == nil {
		t.Fatal("a function read another namespace's admission")
	}
}

func TestSignalHandler_ticketCarriesTheVideoMute(t *testing.T) {
	_, dir := threeSFUs(t)
	var target string
	h := gatewayFor(t, dir, &target)
	ctx := httptest.NewRequest("GET", "/", nil).Context()
	h.admissions.Admit(ctx, "ns", "r1", "0xanyone", "", time.Hour)
	h.admissions.SetMuted(ctx, "ns", "r1", "0xanyone", MediaVideo, true)

	req, w := signalAs(h, "r1", "0xanyone", "")
	if tk := openTicketOf(t, req); w.Code != 200 || !tk.VideoMuted || tk.Muted {
		t.Fatalf("status %d ticket %+v, want one that says the video is muted and the audio is not", w.Code, tk)
	}
}

// The join path refuses a device the namespace kicked, and admits the user's
// other device, on the same admission for every device.
func TestSignalHandler_aKickedDeviceIsRefusedAndAnotherDeviceOfTheAccountIsNot(t *testing.T) {
	_, dir := threeSFUs(t)
	var target string
	h := gatewayFor(t, dir, &target)
	ctx := httptest.NewRequest("GET", "/", nil).Context()
	h.admissions.SetRequireAdmission(ctx, "ns", true)
	h.admissions.Admit(ctx, "ns", "r1", testUser, "", time.Hour)
	h.admissions.RevokeDevice(ctx, "ns", "r1", testUser, "did-phone")

	if _, w := signalAs(h, "r1", testUser, "did-phone"); w.Code != 403 || !strings.Contains(w.Body.String(), string(codeAdmissionRevoked)) {
		t.Fatalf("the kicked device: status %d %s, want 403 %s", w.Code, w.Body, codeAdmissionRevoked)
	}
	if _, w := signalAs(h, "r1", testUser, "did-tablet"); w.Code != 200 {
		t.Fatalf("another device of the account: status %d %s, want admitted", w.Code, w.Body)
	}
}
