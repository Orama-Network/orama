package wssession

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
)

// revokedJTIs denies the tokens it names, and every token bound to a device
// it names as "device:<id>".
type revokedJTIs map[string]bool

func (d revokedJTIs) Revoked(c *auth.JWTClaims) (bool, error) {
	return d[c.Jti] || d.DeviceRevoked(c), nil
}
func (d revokedJTIs) RefreshRevocations(context.Context) {}
func (d revokedJTIs) RevocationsUsable() bool            { return true }
func (d revokedJTIs) DeviceRevoked(c *auth.JWTClaims) bool {
	return c.Did != "" && d["device:"+c.Did]
}

// RevokedKey denies the keys it names as "key:<id>".
func (d revokedJTIs) RevokedKey(id int64) (bool, error) { return d[fmt.Sprintf("key:%d", id)], nil }

// closeLog records what a socket was closed with.
type closeLog struct {
	mu    sync.Mutex
	codes []int
}

func (l *closeLog) closer() Closer {
	return func(code int, _ string) {
		l.mu.Lock()
		l.codes = append(l.codes, code)
		l.mu.Unlock()
	}
}

func (l *closeLog) got() []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]int(nil), l.codes...)
}

var now = time.Unix(1_800_000_000, 0)

func token(jti string, exp time.Time) *auth.JWTClaims {
	return &auth.JWTClaims{Sub: "0xwallet", Jti: jti, Iat: now.Add(-time.Minute).Unix(), Exp: exp.Unix()}
}

func TestRegister_noClaimsIsNotRegistered(t *testing.T) {
	r := NewRegistry(nil)
	var log closeLog
	s := r.Register(nil, log.closer())
	if s != nil || r.Len() != 0 {
		t.Fatal("a socket with no token was registered; there is nothing to hold it to")
	}
	// Every method is safe on the nil a keyless socket gets.
	if s.Expired(now) {
		t.Error("a keyless socket reported expired")
	}
	if !errors.Is(s.Refresh(token("x", now)), ErrNotRefreshable) {
		t.Error("a keyless socket accepted a token; that would hand it an identity")
	}
	s.Unregister()
}

func TestSweep_closesASocketWhoseTokenExpired(t *testing.T) {
	r := NewRegistry(nil)
	var expiredLog, graceLog, liveLog closeLog
	r.Register(token("a", now.Add(-ExpiryGrace-time.Second)), expiredLog.closer())
	r.Register(token("b", now.Add(-ExpiryGrace+time.Second)), graceLog.closer())
	r.Register(token("c", now.Add(time.Minute)), liveLog.closer())

	if n := r.Sweep(now, revokedJTIs{}); n != 1 {
		t.Errorf("closed %d sockets, want 1", n)
	}
	if got := expiredLog.got(); len(got) != 1 || got[0] != CloseExpired {
		t.Errorf("expired socket closed with %v, want [%d]", got, CloseExpired)
	}
	if len(graceLog.got()) != 0 {
		t.Error("a socket inside the grace window was closed; the client had no time to refresh")
	}
	if len(liveLog.got()) != 0 {
		t.Error("a socket with a live token was closed")
	}
}

// The bug: nothing closed an open socket when its session was revoked.
func TestSweep_closesASocketWhoseTokenWasRevoked(t *testing.T) {
	r := NewRegistry(nil)
	var revoked, other closeLog
	r.Register(token("gone", now.Add(time.Minute)), revoked.closer())
	r.Register(token("kept", now.Add(time.Minute)), other.closer())

	r.Sweep(now, revokedJTIs{"gone": true})

	if got := revoked.got(); len(got) != 1 || got[0] != CloseRevoked {
		t.Errorf("revoked socket closed with %v, want [%d]", got, CloseRevoked)
	}
	if len(other.got()) != 0 {
		t.Error("a socket whose token was not revoked was closed")
	}
}

// feat-976: a token-opened socket refused because its device was revoked is
// closed with CloseDeviceRevoked, which tells the client where to read the
// revocation; a capability socket the same device issued is closed with
// CloseRevoked, since its holder is not that device.
func TestSweep_aRevokedDeviceClosesItsSocketsSayingSo(t *testing.T) {
	r := NewRegistry(nil)
	var device, capability, session closeLog
	var reason string
	onDevice := token("d", now.Add(time.Minute))
	onDevice.Did = "dev-1"
	r.Register(onDevice, func(code int, why string) { reason = why; device.closer()(code, why) })
	issued := token("cap", now.Add(time.Minute))
	issued.Did = "dev-1"
	r.RegisterCapability(issued, capability.closer())
	r.Register(token("gone", now.Add(time.Minute)), session.closer())

	if n := r.Sweep(now, revokedJTIs{"device:dev-1": true, "gone": true}); n != 3 {
		t.Fatalf("closed %d sockets, want 3", n)
	}
	if got := device.got(); len(got) != 1 || got[0] != CloseDeviceRevoked || reason != ReasonDeviceRevoked {
		t.Errorf("the revoked device's socket closed with %v %q, want %d %q", got, reason, CloseDeviceRevoked, ReasonDeviceRevoked)
	}
	if got := capability.got(); len(got) != 1 || got[0] != CloseRevoked {
		t.Errorf("the device's capability socket closed with %v, want [%d]: its holder is not the device", got, CloseRevoked)
	}
	if got := session.got(); len(got) != 1 || got[0] != CloseRevoked {
		t.Errorf("a revoked session's socket closed with %v, want [%d]", got, CloseRevoked)
	}
	// RFC 6455 §5.5: a control frame's payload is at most 125 bytes, two of
	// them the code.
	if len(ReasonDeviceRevoked) > 123 {
		t.Errorf("ReasonDeviceRevoked is %d bytes; a close frame holds 123", len(ReasonDeviceRevoked))
	}
}

func TestSweep_closesASocketOnce(t *testing.T) {
	r := NewRegistry(nil)
	var log closeLog
	r.Register(token("gone", now.Add(time.Minute)), log.closer())

	r.Sweep(now, revokedJTIs{"gone": true})
	r.Sweep(now.Add(SweepInterval), revokedJTIs{"gone": true})

	if got := log.got(); len(got) != 1 {
		t.Errorf("closed %d times, want once: the handler may not have unregistered it yet", len(got))
	}
}

func TestUnregister_stopsTheSweepFromReachingIt(t *testing.T) {
	r := NewRegistry(nil)
	var log closeLog
	s := r.Register(token("gone", now.Add(time.Minute)), log.closer())
	s.Unregister()

	r.Sweep(now, revokedJTIs{"gone": true})
	if len(log.got()) != 0 || r.Len() != 0 {
		t.Error("a socket that had gone was still swept")
	}
}

func TestRefresh_holdsTheSocketToTheNewToken(t *testing.T) {
	r := NewRegistry(nil)
	var log closeLog
	s := r.Register(token("old", now.Add(-ExpiryGrace-time.Minute)), log.closer())
	if !s.Expired(now) {
		t.Fatal("precondition: the old token is past its grace")
	}

	if err := s.Refresh(token("new", now.Add(15*time.Minute))); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if s.Expired(now) {
		t.Error("a refreshed socket still reported the old token's expiry")
	}
	// Revoking the token it was opened with no longer reaches it; revoking
	// the one it holds now does.
	r.Sweep(now, revokedJTIs{"old": true})
	if len(log.got()) != 0 {
		t.Error("revoking the replaced token closed the socket")
	}
	r.Sweep(now, revokedJTIs{"new": true})
	if got := log.got(); len(got) != 1 || got[0] != CloseRevoked {
		t.Errorf("revoking the current token closed with %v, want [%d]", got, CloseRevoked)
	}
}

// The bug: auth.refresh on an open socket could switch it to another subject.
// A socket opened on a capability is held to no account, and a refresh cannot
// give it one: the capability exists so that no account is named.
func TestRefresh_aSocketWithNoAccountTakesNoToken(t *testing.T) {
	r := NewRegistry(nil)
	capability := &auth.JWTClaims{Jti: "cap:anchat:1", Did: "device-1", Exp: now.Add(time.Hour).Unix()}
	s := r.Register(capability, (&closeLog{}).closer())

	if err := s.CheckRefresh(token("b", now.Add(time.Hour))); !errors.Is(err, ErrNotRefreshable) {
		t.Fatalf("a capability socket took a token: %v", err)
	}
	if got := s.Claims(); got.Sub != "" || got.Jti != "cap:anchat:1" {
		t.Errorf("the refused refresh changed the socket to %+v", got)
	}
}

func TestRefresh_refusesAnotherSubject(t *testing.T) {
	r := NewRegistry(nil)
	s := r.Register(token("a", now.Add(time.Minute)), (&closeLog{}).closer())

	other := token("b", now.Add(time.Hour))
	other.Sub = "0xsomeone-else"
	if err := s.Refresh(other); !errors.Is(err, ErrSubjectChanged) {
		t.Fatalf("refresh to another subject returned %v, want ErrSubjectChanged", err)
	}
	if got := s.Claims(); got.Sub != "0xwallet" || got.Jti != "a" {
		t.Errorf("a refused refresh changed the socket to %+v", got)
	}
	if !errors.Is(s.Refresh(nil), ErrNoClaims) {
		t.Error("a refresh with no claims was accepted")
	}
}

func TestExpired_boundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		exp  int64
		want bool
	}{
		{"no exp has nothing to enforce", 0, false},
		{"negative exp has nothing to enforce", -5, false},
		{"well before exp", now.Add(10 * time.Minute).Unix(), false},
		{"past exp, inside grace", now.Add(-30 * time.Second).Unix(), false},
		{"exactly at exp+grace", now.Add(-ExpiryGrace).Unix(), false},
		{"past exp+grace", now.Add(-ExpiryGrace - time.Second).Unix(), true},
		{"long expired", now.Add(-24 * time.Hour).Unix(), true},
	} {
		if got := expired(tc.exp, now); got != tc.want {
			t.Errorf("%s: expired(%d) = %v, want %v", tc.name, tc.exp, got, tc.want)
		}
	}
}

// countingDenier records the order a sweep asks in.
type countingDenier struct {
	mu        sync.Mutex
	refreshes int
	revoked   map[string]bool
	askedAt   []int // refresh count at each Revoked call
}

func (d *countingDenier) RefreshRevocations(context.Context) {
	d.mu.Lock()
	d.refreshes++
	d.mu.Unlock()
}

func (d *countingDenier) RevocationsUsable() bool { return true }

func (d *countingDenier) RevokedKey(int64) (bool, error) { return false, nil }

func (d *countingDenier) DeviceRevoked(*auth.JWTClaims) bool { return false }

func (d *countingDenier) Revoked(c *auth.JWTClaims) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.askedAt = append(d.askedAt, d.refreshes)
	return d.revoked[c.Jti], nil
}

func TestRun_reloadsTheListThenSweepsUntilCancelled(t *testing.T) {
	r := NewRegistry(nil)
	closed := make(chan int, 1)
	r.Register(token("gone", time.Now().Add(time.Hour)), func(code int, _ string) { closed <- code })
	d := &countingDenier{revoked: map[string]bool{"gone": true}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx, d, time.Millisecond)
		close(done)
	}()

	select {
	case code := <-closed:
		if code != CloseRevoked {
			t.Errorf("closed with %d, want %d", code, CloseRevoked)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sweeper never closed a revoked socket")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweeper did not stop when its context was cancelled")
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.askedAt) == 0 || d.askedAt[0] == 0 {
		t.Error("a sweep asked about revocations before reloading the list; " +
			"it would miss a revocation recorded on another gateway for up to another interval")
	}
}

// Every gateway runs a sweeper, most of them idle. Reloading the list for no
// socket would be a registry query per gateway per interval for nothing.
func TestRun_anIdleGatewayDoesNotReloadTheList(t *testing.T) {
	r := NewRegistry(nil)
	d := &countingDenier{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r.Run(ctx, d, time.Millisecond)

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.refreshes != 0 {
		t.Errorf("an empty registry reloaded the revocation list %d times", d.refreshes)
	}
}

// A client that stops reading makes its close take up to CloseFrameTimeout.
// That must not hold up closing everybody else's revoked sockets.
func TestSweep_aStalledCloseDoesNotHoldUpTheOthers(t *testing.T) {
	r := NewRegistry(nil)
	release := make(chan struct{})
	stalled := make(chan struct{})
	r.Register(token("stuck", now.Add(time.Minute)), func(int, string) {
		close(stalled)
		<-release
	})
	closedOther := make(chan struct{})
	r.Register(token("other", now.Add(time.Minute)), func(int, string) { close(closedOther) })

	swept := make(chan int, 1)
	go func() { swept <- r.Sweep(now, revokedJTIs{"stuck": true, "other": true}) }()

	select {
	case <-closedOther:
	case <-time.After(5 * time.Second):
		t.Fatal("one stalled close held up closing another revoked socket")
	}
	<-stalled
	close(release)
	if n := <-swept; n != 2 {
		t.Errorf("the sweep reported %d closed, want 2", n)
	}
}

// A socket and a new request must learn of a revocation within the same bound:
// the list's staleness, which is what the docs promise for both.
func TestSweepInterval_isTheRevocationListsStaleness(t *testing.T) {
	if SweepInterval != auth.RevocationRefreshInterval {
		t.Errorf("SweepInterval = %s, want the revocation list's %s", SweepInterval, auth.RevocationRefreshInterval)
	}
}

// feat-422: a refresh keeps the device too. The function was told which
// device is calling, and revoking that device must reach this socket.
func TestRefresh_refusesAnotherDevice(t *testing.T) {
	r := NewRegistry(nil)
	opened := token("a", now.Add(time.Minute))
	opened.Did = "device-1"
	s := r.Register(opened, (&closeLog{}).closer())

	for name, did := range map[string]string{"another device": "device-2", "no device": ""} {
		next := token("b", now.Add(time.Hour))
		next.Did = did
		if err := s.Refresh(next); !errors.Is(err, ErrDeviceChanged) {
			t.Errorf("%s: refresh returned %v, want ErrDeviceChanged", name, err)
		}
	}
	same := token("c", now.Add(time.Hour))
	same.Did = "device-1"
	if err := s.Refresh(same); err != nil {
		t.Errorf("a refresh from the same device was refused: %v", err)
	}
}

// unavailableDenier cannot answer: the list is past its bound and the registry
// does not respond. Revoked hangs, as a wait on a hung registry would, so a
// sweep that called it would never return.
type unavailableDenier struct {
	probes int32
	asked  int32
	hang   chan struct{}
}

func newUnavailableDenier() *unavailableDenier { return &unavailableDenier{hang: make(chan struct{})} }

func (d *unavailableDenier) RefreshRevocations(context.Context) {}
func (d *unavailableDenier) RevocationsUsable() bool {
	atomic.AddInt32(&d.probes, 1)
	return false
}
func (d *unavailableDenier) RevokedKey(int64) (bool, error) {
	return false, auth.ErrRevocationsUnavailable
}

func (d *unavailableDenier) DeviceRevoked(*auth.JWTClaims) bool { return false }

func (d *unavailableDenier) Revoked(*auth.JWTClaims) (bool, error) {
	atomic.AddInt32(&d.asked, 1)
	<-d.hang
	return false, auth.ErrRevocationsUnavailable
}

// registryAt is a registry whose clock is the fixed `now`.
func registryAt(at time.Time) *Registry {
	r := NewRegistry(nil)
	r.now = func() time.Time { return at }
	return r
}

// A list too old to answer must not close every socket on the gateway at once,
// nor leave them open for good: the socket stays inside the cap, and an expired
// one is still closed, since that needs no list.
func TestSweep_leavesASocketOpenWhenRevocationCannotBeCheckedWithinTheCap(t *testing.T) {
	r := registryAt(now)
	var live, expired closeLog
	r.Register(token("live", now.Add(time.Hour)), live.closer())
	r.Register(token("expired", now.Add(-time.Hour)), expired.closer())

	r.Sweep(now.Add(maxUncheckedSocketAge-time.Second), newUnavailableDenier())

	if len(live.got()) != 0 {
		t.Error("a socket was closed on an unknown revocation state inside the cap")
	}
	if got := expired.got(); len(got) != 1 || got[0] != CloseExpired {
		t.Errorf("expired socket closed with %v, want [%d]", got, CloseExpired)
	}
}

// The exposure of an open socket is bounded: a session revoked while the
// registry is unreachable must not stay open for as long as the outage lasts.
func TestSweep_closesASocketUncheckedPastTheCap(t *testing.T) {
	r := registryAt(now)
	var live closeLog
	r.Register(token("live", now.Add(time.Hour)), live.closer())

	r.Sweep(now.Add(maxUncheckedSocketAge+time.Second), newUnavailableDenier())

	if got := live.got(); len(got) != 1 || got[0] != CloseUnavailable {
		t.Errorf("socket closed with %v, want [%d]", got, CloseUnavailable)
	}
}

// A successful check restarts the clock: the cap counts from the last time the
// list vouched for the socket, not from when it opened.
func TestSweep_aSuccessfulCheckRestartsTheUncheckedClock(t *testing.T) {
	r := registryAt(now)
	var live closeLog
	r.Register(token("live", now.Add(time.Hour)), live.closer())

	checkedAt := now.Add(time.Minute)
	if r.Sweep(checkedAt, revokedJTIs{}) != 0 {
		t.Fatal("a socket nobody revoked was closed")
	}
	r.Sweep(checkedAt.Add(maxUncheckedSocketAge-time.Second), newUnavailableDenier())
	if len(live.got()) != 0 {
		t.Error("the cap was counted from the open, not from the last successful check")
	}
	r.Sweep(checkedAt.Add(maxUncheckedSocketAge+time.Second), newUnavailableDenier())
	if got := live.got(); len(got) != 1 || got[0] != CloseUnavailable {
		t.Errorf("socket closed with %v, want [%d]", got, CloseUnavailable)
	}
}

// Under a hung registry the sweep asks one availability question and never
// waits on the list: before, every socket waited for a flight of its own, so
// closing the expired ones took ten seconds per socket.
func TestSweep_underAHungRegistryClosesExpiredSocketsPromptlyWithOneProbe(t *testing.T) {
	r := registryAt(now)
	var expired closeLog
	for i := 0; i < 20; i++ {
		r.Register(token("live", now.Add(time.Hour)), func(int, string) {})
	}
	r.Register(token("expired", now.Add(-time.Hour)), expired.closer())
	d := newUnavailableDenier()
	defer close(d.hang)

	done := make(chan struct{})
	go func() { r.Sweep(now, d); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the sweep waited on a hung registry")
	}
	if got := expired.got(); len(got) != 1 || got[0] != CloseExpired {
		t.Errorf("expired socket closed with %v, want [%d]", got, CloseExpired)
	}
	if probes := atomic.LoadInt32(&d.probes); probes != 1 {
		t.Errorf("%d availability probes in one sweep of 21 sockets, want 1", probes)
	}
	if asked := atomic.LoadInt32(&d.asked); asked != 0 {
		t.Errorf("%d per-socket list calls while the list was unavailable, want 0", asked)
	}
}

// A refreshed token was checked against the list when it was verified.
func TestSocketRefresh_countsAsACheck(t *testing.T) {
	var at atomic.Value
	at.Store(now)
	r := NewRegistry(nil)
	r.now = func() time.Time { return at.Load().(time.Time) }
	first := token("a", now.Add(time.Hour))
	first.Sub = "0xwallet"
	var live closeLog
	s := r.Register(first, live.closer())

	at.Store(now.Add(time.Minute))
	next := token("b", now.Add(2*time.Hour))
	next.Sub = "0xwallet"
	if err := s.Refresh(next); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	r.Sweep(now.Add(time.Minute+maxUncheckedSocketAge-time.Second), newUnavailableDenier())
	if len(live.got()) != 0 {
		t.Error("a socket whose token was just refreshed was closed as unchecked")
	}
}

// bug-702: a socket opened with a bare API key is held to the key's
// revocation, closed with its own reason, and stays open while the key is not
// revoked.
func TestRegisterKey_aRevokedKeyClosesItsSockets(t *testing.T) {
	r := NewRegistry(nil)
	var gone, kept closeLog
	var reason string
	revoked := r.RegisterKey(7, func(code int, why string) { reason = why; gone.closer()(code, why) })
	r.RegisterKey(8, kept.closer())

	if n := r.Sweep(time.Now(), revokedJTIs{"key:7": true}); n != 1 {
		t.Fatalf("closed %d sockets, want the revoked key's one", n)
	}
	if got := gone.got(); len(got) != 1 || got[0] != CloseRevoked || reason != ReasonKeyRevoked {
		t.Errorf("the revoked key's socket closed with %v %q, want %d %q", got, reason, CloseRevoked, ReasonKeyRevoked)
	}
	if got := kept.got(); len(got) != 0 {
		t.Errorf("another key's socket was closed: %v", got)
	}
	if err := revoked.CheckRefresh(token("j", time.Now().Add(time.Hour))); !errors.Is(err, ErrNotRefreshable) {
		t.Errorf("a key-opened socket took on a token: %v", err)
	}
}

// A socket with no key id names nothing to revoke and is not held.
func TestRegisterKey_noKeyRegistersNothing(t *testing.T) {
	r := NewRegistry(nil)
	if s := r.RegisterKey(0, func(int, string) {}); s != nil || r.Len() != 0 {
		t.Errorf("a socket with no key was registered: %v, %d", s, r.Len())
	}
}
