package sfu

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/sfu/ctrlauth"
)

// control posts a signed control request straight to the SFU's handler.
func control(t *testing.T, s *Server, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	return controlRaw(s, path, raw, ctrlauth.Sign(s.controlKey, s.config.ListenAddr, http.MethodPost, path, raw, time.Now()))
}

func controlRaw(s *Server, path string, raw []byte, mac string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set(ctrlauth.MACHeader, mac)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	return w
}

func affected(t *testing.T, w *httptest.ResponseRecorder) int {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	var r ctrlauth.ControlResult
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r.Affected
}

func TestKick_closesThePeerAndTellsIt(t *testing.T) {
	s := newAuthServer(t)
	alice, _ := joinAs(t, s, testTicket(t, s, "r1", "alice"), "")
	bob, _ := joinAs(t, s, testTicket(t, s, "r1", "bob"), "")

	w := control(t, s, ctrlauth.KickPath, ctrlauth.KickRequest{Room: "r1", UserID: "alice", AtMs: time.Now().UnixMilli()})

	if n := affected(t, w); n != 1 {
		t.Fatalf("affected = %d, want 1", n)
	}
	readType(t, alice, MessageTypeKicked)
	if _, _, err := alice.ReadMessage(); err == nil {
		t.Fatal("the kicked peer's socket is still open")
	}
	left := readType(t, bob, MessageTypeParticipantLeft)
	if len(left.Data) == 0 {
		t.Fatal("bob was not told alice left")
	}
	if s.roomManager.GetRoom("r1").GetParticipantCount() != 1 {
		t.Error("the kicked peer is still in the room")
	}
}

func TestKick_removesEveryDeviceOfTheUser(t *testing.T) {
	s := newAuthServer(t)
	phone := testTicket(t, s, "r1", "alice")
	phone.DeviceID = "phone"
	laptop := testTicket(t, s, "r1", "alice")
	laptop.DeviceID = "laptop"
	joinAs(t, s, phone, "")
	joinAs(t, s, laptop, "")

	w := control(t, s, ctrlauth.KickPath, ctrlauth.KickRequest{Room: "r1", UserID: "alice", AtMs: time.Now().UnixMilli()})

	if n := affected(t, w); n != 2 {
		t.Fatalf("affected = %d, want both devices", n)
	}
}

func TestKick_aJoinTicketedBeforeTheKickIsRefusedAndOneAfterIsNot(t *testing.T) {
	s := newAuthServer(t)
	inFlight := testTicket(t, s, "r1", "alice") // the admission was valid when this was issued
	time.Sleep(5 * time.Millisecond)
	kickAt := time.Now().UnixMilli()
	time.Sleep(5 * time.Millisecond)

	// The room is not even hosted here yet: the kick must still be remembered.
	if n := affected(t, control(t, s, ctrlauth.KickPath, ctrlauth.KickRequest{Room: "r1", UserID: "alice", AtMs: kickAt})); n != 0 {
		t.Fatalf("affected = %d, want 0 for a room not hosted here", n)
	}

	_, resp, err := tryDial(t, s, "room=r1", http.Header{ctrlauth.TicketHeader: {sealTicket(t, s, inFlight)}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a ticket issued before the kick was let in: err=%v resp=%v", err, resp)
	}

	after := testTicket(t, s, "r1", "alice")
	after.IssuedAtMs = kickAt + kickSkewMargin.Milliseconds() + 10
	joinAs(t, s, after, "")
	// Another user, and another room, are not affected by alice's kick.
	joinAs(t, s, testTicket(t, s, "r1", "bob"), "")
	joinAs(t, s, testTicket(t, s, "r2", "alice"), "")
}

func TestControl_refusesWhatItCannotAuthenticate(t *testing.T) {
	s := newAuthServer(t)
	body, _ := json.Marshal(ctrlauth.KickRequest{Room: "r1", UserID: "alice", AtMs: 1})
	otherKey, _ := ctrlauth.Key("other-namespace")

	cases := []struct {
		name string
		mac  string
	}{
		{"no MAC", ""},
		{"another namespace's key", ctrlauth.Sign(otherKey, s.config.ListenAddr, http.MethodPost, ctrlauth.KickPath, body, time.Now())},
		{"a stamp for another path", ctrlauth.Sign(s.controlKey, s.config.ListenAddr, http.MethodPost, ctrlauth.MutePath, body, time.Now())},
		{"a stale stamp", ctrlauth.Sign(s.controlKey, s.config.ListenAddr, http.MethodPost, ctrlauth.KickPath, body, time.Now().Add(-time.Hour))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if w := controlRaw(s, ctrlauth.KickPath, body, c.mac); w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
		})
	}
	// None of them recorded a kick.
	if s.kicks.refuses("r1", "alice", "", 0, 0) {
		t.Error("an unauthenticated request left a kick on record")
	}
}

func TestControl_malformedRequestsAreBadRequests(t *testing.T) {
	s := newAuthServer(t)
	for path, body := range map[string]string{
		ctrlauth.KickPath: `{"room":"r1"}`,
		ctrlauth.MutePath: `not json`,
	} {
		raw := []byte(body)
		mac := ctrlauth.Sign(s.controlKey, s.config.ListenAddr, http.MethodPost, path, raw, time.Now())
		if w := controlRaw(s, path, raw, mac); w.Code != http.StatusBadRequest {
			t.Errorf("%s %q: status = %d, want 400", path, body, w.Code)
		}
	}
	// A mute without the gateway's clock cannot be ordered against a ticket.
	noClock := []byte(`{"room":"r1","user_id":"alice","muted":true}`)
	mac := ctrlauth.Sign(s.controlKey, s.config.ListenAddr, http.MethodPost, ctrlauth.MutePath, noClock, time.Now())
	if w := controlRaw(s, ctrlauth.MutePath, noClock, mac); w.Code != http.StatusBadRequest {
		t.Errorf("mute without at_ms: status = %d, want 400", w.Code)
	}
	req := httptest.NewRequest(http.MethodGet, ctrlauth.KickPath, nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", w.Code)
	}
}

func TestMute_marksThePeerAndTellsTheRoom(t *testing.T) {
	s := newAuthServer(t)
	joinAs(t, s, testTicket(t, s, "r1", "alice"), "")
	bob, _ := joinAs(t, s, testTicket(t, s, "r1", "bob"), "")

	w := control(t, s, ctrlauth.MutePath, ctrlauth.MuteRequest{Room: "r1", UserID: "alice", Muted: true, AtMs: nowMs()})

	if n := affected(t, w); n != 1 {
		t.Fatalf("affected = %d, want 1", n)
	}
	var alicePeer *Peer
	for _, p := range s.roomManager.GetRoom("r1").snapshotPeers("") {
		if p.UserID == "alice" {
			alicePeer = p
		}
	}
	if alicePeer == nil || !alicePeer.muted.Load() {
		t.Fatal("alice's peer is not muted")
	}
	m := readType(t, bob, MessageTypeParticipantState)
	var st ParticipantStateData
	_ = json.Unmarshal(m.Data, &st)
	if st.UserID != "alice" || st.Kind != "audio" || st.Enabled || !st.Forced {
		t.Fatalf("state = %+v, want alice's audio disabled, forced", st)
	}

	control(t, s, ctrlauth.MutePath, ctrlauth.MuteRequest{Room: "r1", UserID: "alice", Muted: false, AtMs: nowMs() + 1000})
	if alicePeer.muted.Load() {
		t.Error("unmuting did not clear the mute")
	}
}

func TestMute_unknownRoomOrUserAffectsNobody(t *testing.T) {
	s := newAuthServer(t)
	joinAs(t, s, testTicket(t, s, "r1", "alice"), "")
	for _, req := range []ctrlauth.MuteRequest{
		{Room: "nowhere", UserID: "alice", Muted: true, AtMs: nowMs()},
		{Room: "r1", UserID: "nobody", Muted: true, AtMs: nowMs()},
	} {
		if n := affected(t, control(t, s, ctrlauth.MutePath, req)); n != 0 {
			t.Errorf("%+v affected %d peers", req, n)
		}
	}
}
