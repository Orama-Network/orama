package webrtc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/logging"
	"github.com/Orama-Network/orama/core/pkg/sfu/ctrlauth"
)

// controlSFU is an SFU that answers health and the control routes, checking
// the MAC the way the real one does and recording what it was asked.
type controlSFU struct {
	node        SFUNode
	srv         *httptest.Server
	mu          sync.Mutex
	kicks       []ctrlauth.KickRequest
	deviceKicks []ctrlauth.KickRequest
	mutes       []ctrlauth.MuteRequest
	videoMutes  []ctrlauth.MuteRequest
	status      int
}

func newControlSFU(t *testing.T, id string) *controlSFU {
	t.Helper()
	key, err := ctrlauth.Key(testTURNSecret)
	if err != nil {
		t.Fatal(err)
	}
	f := &controlSFU{status: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"status":"ok","hasRoom":false}`) })
	control := func(path string, record func([]byte)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if err := ctrlauth.Verify(key, f.node.Addr(), r.Header.Get(ctrlauth.MACHeader), r.Method, path, body, time.Now()); err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.status != http.StatusOK {
				http.Error(w, "boom", f.status)
				return
			}
			record(body)
			_ = json.NewEncoder(w).Encode(ctrlauth.ControlResult{Affected: 1})
		})
	}
	control(ctrlauth.KickPath, func(b []byte) {
		var r ctrlauth.KickRequest
		_ = json.Unmarshal(b, &r)
		f.kicks = append(f.kicks, r)
	})
	control(ctrlauth.MutePath, func(b []byte) {
		var r ctrlauth.MuteRequest
		_ = json.Unmarshal(b, &r)
		f.mutes = append(f.mutes, r)
	})
	control(ctrlauth.KickDevicePath, func(b []byte) {
		var r ctrlauth.KickRequest
		_ = json.Unmarshal(b, &r)
		f.deviceKicks = append(f.deviceKicks, r)
	})
	control(ctrlauth.VideoMutePath, func(b []byte) {
		var r ctrlauth.MuteRequest
		_ = json.Unmarshal(b, &r)
		f.videoMutes = append(f.videoMutes, r)
	})
	srv := httptest.NewServer(mux)
	f.srv = srv
	t.Cleanup(srv.Close)
	host, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	f.node = SFUNode{NodeID: id, Host: host, Port: p}
	return f
}

// srvClose stops the SFU, so a call to it fails to connect.
func (f *controlSFU) srvClose() { f.srv.Close() }

func controllerFor(t *testing.T, sfu *controlSFU) *WebRTCHandlers {
	t.Helper()
	logger, _ := logging.NewColoredLogger(logging.ComponentGeneral, false)
	h := NewWebRTCHandlers(logger, "10.0.0.9", 30000, "", testTURNSecret, nil)
	h.SetSFUDirectory(staticDirectory{nodes: []SFUNode{sfu.node}})
	h.SetNamespace("ns")
	withAdmissions(t, h)
	return h
}

var bg = context.Background()

func TestAdmit_recordsAnAdmissionThatTheJoinPathHonours(t *testing.T) {
	sfu := newControlSFU(t, "a")
	h := controllerFor(t, sfu)

	expires, err := h.Admit(bg, "ns", "r1", "alice", "", 10*time.Minute)

	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(expires); d < 9*time.Minute || d > 10*time.Minute {
		t.Fatalf("expires in %s, want about 10 minutes", d)
	}
	if a, _ := h.admissions.Lookup(bg, "ns", "r1", "alice", ""); !a.Valid {
		t.Fatal("the admission is not on record")
	}
}

func TestAdmit_refusesWhatCannotBeAnAdmission(t *testing.T) {
	h := controllerFor(t, newControlSFU(t, "a"))
	long := strings.Repeat("x", maxIdentityLen+1)
	cases := []struct {
		name               string
		room, user, device string
		ttl                time.Duration
	}{
		{"bad room", "has a space", "alice", "", time.Minute},
		{"empty room", "", "alice", "", time.Minute},
		{"no user", "r1", "", "", time.Minute},
		{"user too long", "r1", long, "", time.Minute},
		{"device too long", "r1", "alice", long, time.Minute},
		{"zero ttl", "r1", "alice", "", 0},
		{"negative ttl", "r1", "alice", "", -time.Second},
		{"ttl beyond the cap", "r1", "alice", "", MaxAdmissionTTL + time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := h.Admit(bg, "ns", c.room, c.user, c.device, c.ttl); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if _, err := h.Admit(bg, "ns", "r1", "alice", "", MaxAdmissionTTL); err != nil {
		t.Errorf("a ttl exactly at the cap was refused: %v", err)
	}
}

func TestKick_revokesTheAdmissionAndClosesThePeerOnTheOwnerSFU(t *testing.T) {
	sfu := newControlSFU(t, "a")
	h := controllerFor(t, sfu)
	h.Admit(bg, "ns", "r1", "alice", "", time.Hour)
	before := time.Now().UnixMilli()

	if err := h.Kick(bg, "ns", "r1", "alice"); err != nil {
		t.Fatal(err)
	}

	if a, _ := h.admissions.Lookup(bg, "ns", "r1", "alice", ""); a.Valid || !a.Revoked {
		t.Fatalf("admission after kick = %+v, want revoked", a)
	}
	if len(sfu.kicks) != 1 || sfu.kicks[0].Room != "r1" || sfu.kicks[0].UserID != "alice" || sfu.kicks[0].AtMs < before {
		t.Fatalf("SFU kicks = %+v", sfu.kicks)
	}
}

func TestKick_carriesTheRevokedGenerationAndAReadmissionOutranksIt(t *testing.T) {
	sfu := newControlSFU(t, "a")
	h := controllerFor(t, sfu)
	h.Admit(bg, "ns", "r1", "alice", "", time.Hour)
	h.Admit(bg, "ns", "r1", "alice", "", time.Hour) // generation 2

	if err := h.Kick(bg, "ns", "r1", "alice"); err != nil {
		t.Fatal(err)
	}
	if len(sfu.kicks) != 1 || sfu.kicks[0].AdmitGen != 2 {
		t.Fatalf("SFU kicks = %+v, want one carrying admit_gen 2", sfu.kicks)
	}
	if err := h.Kick(bg, "ns", "r1", "nobody"); err != nil {
		t.Fatal(err)
	}
	if sfu.kicks[1].AdmitGen != 0 {
		t.Errorf("a kick of a user with no admission carried generation %d, want 0", sfu.kicks[1].AdmitGen)
	}
}

func TestKick_aRevokedUserIsRefusedAtTheNextJoin(t *testing.T) {
	sfu := newControlSFU(t, "a")
	h := controllerFor(t, sfu)
	h.admissions.SetRequireAdmission(bg, "ns", true)
	h.admissions.Admit(bg, "ns", "r1", testUser, "", time.Hour)
	h.proxyWebSocket = func(http.ResponseWriter, *http.Request, string) bool { return true }
	if _, w := signalAs(h, "r1", testUser, ""); w.Code != http.StatusOK {
		t.Fatalf("before the kick: status %d %s", w.Code, w.Body)
	}

	if err := h.Kick(bg, "ns", "r1", testUser); err != nil {
		t.Fatal(err)
	}

	_, w := signalAs(h, "r1", testUser, "")
	if w.Code != http.StatusForbidden || rpcCode(t, w) != codeAdmissionRevoked {
		t.Fatalf("rejoin after the kick: status %d %s", w.Code, w.Body)
	}
}

func TestKick_sfuFailureIsReportedButTheRevocationStands(t *testing.T) {
	sfu := newControlSFU(t, "a")
	sfu.status = http.StatusInternalServerError
	h := controllerFor(t, sfu)
	h.Admit(bg, "ns", "r1", "alice", "", time.Hour)

	err := h.Kick(bg, "ns", "r1", "alice")

	if err == nil || !strings.Contains(err.Error(), "revoked, but their connection could not be closed") {
		t.Fatalf("err = %v, want it to say the revocation stands and the close failed", err)
	}
	if a, _ := h.admissions.Lookup(bg, "ns", "r1", "alice", ""); !a.Revoked {
		t.Fatal("a failed SFU call undid the revocation")
	}
}

func TestKick_noSFUAndBadInputAreErrors(t *testing.T) {
	h := controllerFor(t, newControlSFU(t, "a"))
	h.SetSFUDirectory(staticDirectory{})
	if err := h.Kick(bg, "ns", "r1", "alice"); err == nil {
		t.Error("a namespace with no SFU reported a kick as done")
	}
	if err := h.Kick(bg, "ns", "bad room", "alice"); err == nil {
		t.Error("an invalid room was accepted")
	}
	h.controlKey = nil
	if err := h.Kick(bg, "ns", "r1", "alice"); err == nil || !strings.Contains(err.Error(), "TURN secret") {
		t.Errorf("err = %v, want it to name the missing TURN secret", err)
	}
}

func TestMute_isRecordedAndSentToTheSFU(t *testing.T) {
	sfu := newControlSFU(t, "a")
	h := controllerFor(t, sfu)
	h.Admit(bg, "ns", "r1", "alice", "", time.Hour)

	if err := h.Mute(bg, "ns", "r1", "alice", true); err != nil {
		t.Fatal(err)
	}
	if a, _ := h.admissions.Lookup(bg, "ns", "r1", "alice", ""); !a.Muted {
		t.Fatal("the mute is not on record, so a rejoin would be unmuted")
	}
	if len(sfu.mutes) != 1 || !sfu.mutes[0].Muted || sfu.mutes[0].UserID != "alice" || sfu.mutes[0].Room != "r1" {
		t.Fatalf("SFU mutes = %+v", sfu.mutes)
	}

	if err := h.Mute(bg, "ns", "r1", "alice", false); err != nil {
		t.Fatal(err)
	}
	if a, _ := h.admissions.Lookup(bg, "ns", "r1", "alice", ""); a.Muted || len(sfu.mutes) != 2 || sfu.mutes[1].Muted {
		t.Fatalf("unmute: record %+v, SFU %+v", a, sfu.mutes)
	}
}

func TestMute_sfuRefusalIsAnError(t *testing.T) {
	sfu := newControlSFU(t, "a")
	h := controllerFor(t, sfu)
	h.controlKey, _ = ctrlauth.Key("another-namespace's-secret") // the SFU will refuse the MAC

	if err := h.Mute(bg, "ns", "r1", "alice", true); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want the SFU's 401 surfaced", err)
	}
	if len(sfu.mutes) != 0 {
		t.Error("the SFU acted on an unauthenticated mute")
	}
}
