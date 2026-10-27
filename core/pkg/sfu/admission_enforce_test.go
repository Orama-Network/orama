package sfu

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/sfu/ctrlauth"
	"github.com/gorilla/websocket"
)

// waitCount polls the room's size: a peer is removed on another goroutine.
func waitCount(t *testing.T, s *Server, room string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if r := s.roomManager.GetRoom(room); (r == nil && want == 0) || (r != nil && r.GetParticipantCount() == want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := -1
	if r := s.roomManager.GetRoom(room); r != nil {
		got = r.GetParticipantCount()
	}
	t.Fatalf("room %s has %d peers, want %d", room, got, want)
}

// A kick that lands after the upgrade and before the client's join frame finds
// no peer to remove. The peer must not then be let in.
func TestKick_betweenUpgradeAndJoinFrameKeepsThePeerOut(t *testing.T) {
	s := newAuthServer(t)
	tk := testTicket(t, s, "r1", "alice")
	conn := dialSignalAs(t, s, "room=r1", tk) // upgraded, no join frame yet
	time.Sleep(5 * time.Millisecond)

	if n := affected(t, control(t, s, ctrlauth.KickPath, ctrlauth.KickRequest{Room: "r1", UserID: "alice", AtMs: time.Now().UnixMilli()})); n != 0 {
		t.Fatalf("affected = %d, want 0: the peer has not joined yet", n)
	}
	sendJoin(t, conn, "r1")

	for {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var m rawFrame
		if err := conn.ReadJSON(&m); err != nil {
			break // closed: the peer was refused
		}
		if m.Type == MessageTypeWelcome {
			t.Fatal("a kicked user was welcomed into the room")
		}
	}
	waitCount(t, s, "r1", 0)
}

func TestKick_afterTheJoinFrameStillRemovesThePeer(t *testing.T) {
	s := newAuthServer(t)
	alice, _ := joinAs(t, s, testTicket(t, s, "r1", "alice"), "")
	if n := affected(t, control(t, s, ctrlauth.KickPath, ctrlauth.KickRequest{Room: "r1", UserID: "alice", AtMs: time.Now().UnixMilli()})); n != 1 {
		t.Fatalf("affected = %d, want 1", n)
	}
	readType(t, alice, MessageTypeKicked)
}

func TestKickLog_usesTheSFUsClockAndTheGatewaySkewMargin(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	k := newKickLog()
	k.now = func() time.Time { return now }
	at := now.UnixMilli() // the kicking gateway's clock
	k.record("r", "alice", "", at, 0)

	cases := []struct {
		name     string
		room     string
		user     string
		issuedMs int64
		later    time.Duration // how long after the kick the SFU receives the ticket
		want     bool
	}{
		{"issued before the kick", "r", "alice", at - 1000, 0, true},
		{"issued by a gateway whose clock runs ahead of the kicker's", "r", "alice", at + kickSkewMargin.Milliseconds() - 1, 0, true},
		{"issued after the kick, beyond the skew margin", "r", "alice", at + kickSkewMargin.Milliseconds() + 1, 0, false},
		{"received after the window the kick can matter in", "r", "alice", at - 1000, kickWindow + time.Second, false},
		{"another user", "r", "bob", at - 1000, 0, false},
		{"another room", "r2", "alice", at - 1000, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			now = time.Unix(1_800_000_000, 0).Add(c.later)
			if got := k.refuses(c.room, c.user, "", c.issuedMs, 0); got != c.want {
				t.Fatalf("refuses = %v, want %v", got, c.want)
			}
		})
	}
}

func TestKickLog_dropsKicksOlderThanTheWindowAndKeepsTheLatest(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	k := newKickLog()
	k.now = func() time.Time { return now }
	k.record("r", "old", "", now.UnixMilli(), 0)
	now = now.Add(kickWindow + time.Second)
	k.record("r", "alice", "", 2000, 0)
	k.record("r", "alice", "", 1000, 0) // an older kick does not move the time back
	if _, ok := k.kicks[kickKey("r", "old")]; ok {
		t.Error("a kick past the window was kept")
	}
	if got := k.kicks[kickKey("r", "alice")].atMs; got != 2000 {
		t.Errorf("kick time = %d, want the latest, 2000", got)
	}
}

func TestAdmissionExpiry_endsALiveSession(t *testing.T) {
	s := newAuthServer(t)
	gw := newSink(t, s.controlKey, http.StatusOK)
	s.roomManager.reporter.observe(gw.URL)
	fire := make(chan time.Time)
	waits := make(chan time.Duration, 1)
	s.expireAfter = func(d time.Duration) <-chan time.Time { waits <- d; return fire }

	tk := testTicket(t, s, "r1", "alice")
	tk.AdmitExp = time.Now().Add(90 * time.Second).Unix()
	alice, _ := joinAs(t, s, tk, "")
	joinAs(t, s, testTicket(t, s, "r1", "bob"), "") // no admission bound: unaffected

	if d := <-waits; d < 80*time.Second || d > 90*time.Second {
		t.Fatalf("the session was bound for %s, want what is left of the admission", d)
	}
	fire <- time.Now()

	var m rawFrame
	for m.Type != MessageTypeKicked {
		m = readFrame(t, alice)
	}
	if !strings.Contains(string(m.Data), KickedCodeExpired) {
		t.Fatalf("kicked frame = %s, want code %s", m.Data, KickedCodeExpired)
	}
	waitCount(t, s, "r1", 1)
	var leave ctrlauth.MembershipEvent
	for _, e := range waitEvents(t, gw, 3) {
		if e.Type == ctrlauth.EventLeave {
			leave = e
		}
	}
	if leave.UserID != "alice" || leave.Reason != ctrlauth.ExpiredReason {
		t.Fatalf("leave event = %+v, want alice with reason %q", leave, ctrlauth.ExpiredReason)
	}
}

func TestAdmissionExpiry_aPeerThatLeftFirstIsNotTouched(t *testing.T) {
	s := newAuthServer(t)
	fire := make(chan time.Time)
	waits := make(chan struct{}, 1)
	s.expireAfter = func(time.Duration) <-chan time.Time { waits <- struct{}{}; return fire }
	tk := testTicket(t, s, "r1", "alice")
	tk.AdmitExp = time.Now().Add(time.Minute).Unix()
	alice, _ := joinAs(t, s, tk, "")
	<-waits

	_ = alice.WriteJSON(ClientMessage{Type: MessageTypeLeave})
	waitCount(t, s, "r1", 0)
	select {
	case fire <- time.Now():
		t.Fatal("the expiry waiter is still running for a peer that left")
	case <-time.After(200 * time.Millisecond):
	}
}

// A control request is served once: the same MAC again is a replay.
func TestControl_aReplayedRequestIsRefused(t *testing.T) {
	s := newAuthServer(t)
	body := []byte(`{"room":"r1","user_id":"alice","at_ms":1}`)
	mac := ctrlauth.Sign(s.controlKey, s.config.ListenAddr, http.MethodPost, ctrlauth.KickPath, body, time.Now())

	if w := controlRaw(s, ctrlauth.KickPath, body, mac); w.Code != http.StatusOK {
		t.Fatalf("first: status %d %s", w.Code, w.Body)
	}
	if w := controlRaw(s, ctrlauth.KickPath, body, mac); w.Code != http.StatusUnauthorized {
		t.Fatalf("replay: status %d, want 401", w.Code)
	}
	fresh := ctrlauth.Sign(s.controlKey, s.config.ListenAddr, http.MethodPost, ctrlauth.KickPath, body, time.Now())
	if w := controlRaw(s, ctrlauth.KickPath, body, fresh); w.Code != http.StatusOK {
		t.Fatalf("an honest repeat of the same request was refused: status %d", w.Code)
	}
}

// A forged request must not be able to fill the replay memory.
func TestControl_unauthenticRequestsAreNotRemembered(t *testing.T) {
	s := newAuthServer(t)
	body := []byte(`{"room":"r1","user_id":"alice","at_ms":1}`)
	otherKey, _ := ctrlauth.Key("another-namespace")
	mac := ctrlauth.Sign(otherKey, s.config.ListenAddr, http.MethodPost, ctrlauth.KickPath, body, time.Now())
	controlRaw(s, ctrlauth.KickPath, body, mac)
	if err := s.replays.Use(mac, time.Now()); err != nil {
		t.Fatalf("a refused stamp was remembered: %v", err)
	}
}

// The listener's timeouts bound idle clients of the control routes, and must
// not end a signalling socket: net/http clears the deadlines when the upgrade
// hijacks the connection.
func TestHTTPServer_timeoutsAreSetAndDoNotKillAnUpgradedSocket(t *testing.T) {
	s := newAuthServer(t)
	if s.httpServer.ReadTimeout != httpReadTimeout || s.httpServer.IdleTimeout != httpIdleTimeout || s.httpServer.ReadHeaderTimeout != httpReadHeaderTimeout {
		t.Fatalf("timeouts = read %s idle %s header %s", s.httpServer.ReadTimeout, s.httpServer.IdleTimeout, s.httpServer.ReadHeaderTimeout)
	}
	s.httpServer.ReadTimeout = 200 * time.Millisecond // short, to see it hold
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = s.httpServer.Serve(ln) }()
	t.Cleanup(func() { _ = s.httpServer.Close(); wg.Wait() })

	tk := testTicket(t, s, "r1", "alice")
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/ws/signal?room=r1", http.Header{ctrlauth.TicketHeader: {sealTicket(t, s, tk)}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	time.Sleep(700 * time.Millisecond) // well past ReadTimeout
	sendJoin(t, conn, "r1")
	if m := readFrame(t, conn); m.Type != MessageTypeWelcome {
		t.Fatalf("first frame = %s, want welcome: the upgraded socket outlived the read timeout", m.Type)
	}
}

func TestKickLog_generationsDecideWhereBothExistAndNoClockIsConsulted(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	k := newKickLog()
	k.now = func() time.Time { return now }
	at := now.UnixMilli()
	k.record("r", "alice", "", at, 4)

	cases := []struct {
		name     string
		issuedMs int64
		gen      int64
		want     bool
	}{
		{"a re-admission's ticket, issued a second after the kick", at + 1000, 5, false},
		{"a re-admission's ticket, even if its gateway's clock is behind the kicker's", at - 5000, 5, false},
		{"the revoked admission's ticket, issued by a gateway whose clock runs far ahead", at + 60_000, 4, true},
		{"an older admission's ticket", at + 60_000, 2, true},
		{"a ticket with no generation keeps the clock rule: issued before", at - 1000, 0, true},
		{"a ticket with no generation keeps the clock rule: issued after the margin", at + kickSkewMargin.Milliseconds() + 1, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := k.refuses("r", "alice", "", c.issuedMs, c.gen); got != c.want {
				t.Fatalf("refuses = %v, want %v", got, c.want)
			}
		})
	}
}

func TestKickLog_aKickWithoutAGenerationKeepsTheClockRuleForTicketsWithOne(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	k := newKickLog()
	k.now = func() time.Time { return now }
	at := now.UnixMilli()
	k.record("r", "alice", "", at, 0) // a gateway that predates generations, or a namespace with none
	if !k.refuses("r", "alice", "", at-1000, 7) {
		t.Error("a ticket issued before the kick was let in")
	}
	if k.refuses("r", "alice", "", at+kickSkewMargin.Milliseconds()+1, 7) {
		t.Error("a ticket issued after the kick was refused")
	}
}

func TestKickLog_keepsTheNewestGenerationAndExpiresWithTheWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	k := newKickLog()
	k.now = func() time.Time { return now }
	k.record("r", "alice", "", 2000, 6)
	k.record("r", "alice", "", 1000, 3) // a late, older kick does not lower it
	if got := k.kicks[kickKey("r", "alice")].gen; got != 6 {
		t.Fatalf("generation = %d, want 6", got)
	}
	now = now.Add(kickWindow + time.Second)
	if k.refuses("r", "alice", "", 1, 5) {
		t.Error("a kick past the window still refuses")
	}
}

// A user admitted again right after a kick holds a ticket issued within the
// clock margin of the kick. The generation, not the margin, lets it in.
func TestKick_aReadmittedUserJoinsAtOnceAndTheKickedGenerationStaysOut(t *testing.T) {
	s := newAuthServer(t)
	kick := ctrlauth.KickRequest{Room: "r1", UserID: "alice", AtMs: time.Now().UnixMilli(), AdmitGen: 3}
	if n := affected(t, control(t, s, ctrlauth.KickPath, kick)); n != 0 {
		t.Fatalf("affected = %d, want 0", n)
	}

	stale := testTicket(t, s, "r1", "alice")
	stale.AdmitGen = 3
	_, resp, err := tryDial(t, s, "room=r1", http.Header{ctrlauth.TicketHeader: {sealTicket(t, s, stale)}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a ticket of the kicked generation: err=%v resp=%v, want 403", err, resp)
	}

	fresh := testTicket(t, s, "r1", "alice")
	fresh.AdmitGen = 4
	joinAs(t, s, fresh, "")
}

// A kick that carries no generation (an old gateway during a rolling upgrade)
// puts the entry back on the clock rule. It used to inherit the previous
// kick's generation, so a ticket of an admission made between the two kicks
// passed the second one: kick gen 5, re-admit gen 6, kick without a
// generation, join with the gen-6 ticket issued before it — let in.
func TestKickLog_aKickWithoutAGenerationAfterOneWithStillRefusesTheTicketsBeforeIt(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	k := newKickLog()
	k.now = func() time.Time { return now }
	first := now.UnixMilli()
	k.record("r", "alice", "", first, 5)
	readmittedTicket := first + 2000 // issued on the gen-6 re-admission
	second := first + 5000
	k.record("r", "alice", "", second, 0)

	if !k.refuses("r", "alice", "", readmittedTicket, 6) {
		t.Error("a ticket issued before the second kick passed it")
	}
	if k.refuses("r", "alice", "", second+kickSkewMargin.Milliseconds()+1, 7) {
		t.Error("a ticket issued well after the second kick was refused")
	}
}
