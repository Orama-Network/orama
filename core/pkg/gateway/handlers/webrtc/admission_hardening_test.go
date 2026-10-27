package webrtc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/rqlite"
	"github.com/Orama-Network/orama/core/pkg/rqlite/rqlitetest"
	"github.com/Orama-Network/orama/core/pkg/sfu/ctrlauth"
)

// clockedDB lets a test move the gateway's clock while the admission is being
// read, which is the window a concurrent revoke commits in.
type clockedDB struct {
	rqlite.Client
	onQuery func()
}

func (c clockedDB) Query(ctx context.Context, dest any, query string, args ...any) error {
	err := c.Client.Query(ctx, dest, query, args...)
	if c.onQuery != nil {
		c.onQuery()
	}
	return err
}

// F2: the ticket is stamped from before the admission was read, so a revoke that
// commits during the read (and stamps its own, later, time) is never older than
// a ticket that did not see it.
func TestAuthorizeJoin_ticketIsStampedBeforeTheAdmissionIsRead(t *testing.T) {
	_, dir := threeSFUs(t)
	var target string
	h := gatewayFor(t, dir, &target)
	clock := time.Now()
	h.now = func() time.Time { return clock }
	start := clock
	h.admissions.db = clockedDB{Client: h.admissions.db, onQuery: func() { clock = clock.Add(time.Second) }}

	req, w := signalAs(h, "r1", testUser, "")

	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if got := openTicketOf(t, req).IssuedAtMs; got != start.UnixMilli() {
		t.Fatalf("ticket issued at %d, want %d (the time before the admission was read)", got, start.UnixMilli())
	}
}

// MEDIUM-2a: the ticket carries the admission's end when admission is required.
func TestAuthorizeJoin_ticketCarriesTheAdmissionEnd(t *testing.T) {
	_, dir := threeSFUs(t)
	var target string
	h := gatewayFor(t, dir, &target)
	ctx := context.Background()
	h.admissions.SetRequireAdmission(ctx, "ns", true)
	short, _ := h.admissions.Admit(ctx, "ns", "r1", testUser, "", time.Hour)
	long, _ := h.admissions.Admit(ctx, "ns", "r1", testUser, "phone", 2*time.Hour)

	req, w := signalAs(h, "r1", testUser, "phone")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if got := openTicketOf(t, req).AdmitExp; got != long.Unix() || got == short.Unix() {
		t.Fatalf("AdmitExp = %d, want the latest valid admission's end %d", got, long.Unix())
	}

	h.admissions.SetRequireAdmission(ctx, "ns", false)
	req, _ = signalAs(h, "r1", testUser, "phone")
	if got := openTicketOf(t, req).AdmitExp; got != 0 {
		t.Fatalf("AdmitExp = %d, want 0 when admission is not required", got)
	}
}

// MEDIUM-2b: an expired muted admission is not purged, so a re-admission does not unmute.
func TestAdmissionStore_muteSurvivesExpiryAndReadmission(t *testing.T) {
	s, _, now := newSQLiteStore(t)
	ctx := context.Background()
	s.Admit(ctx, "ns", "r1", "alice", "", time.Minute)
	s.Admit(ctx, "ns", "r1", "bob", "", time.Minute)
	s.SetMuted(ctx, "ns", "r1", "alice", MediaAudio, true)

	*now = now.Add(time.Hour)                        // both admissions expired
	s.Admit(ctx, "ns", "r2", "carol", "", time.Hour) // any Admit purges expired rows
	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour)
	s.Admit(ctx, "ns", "r1", "bob", "", time.Hour)

	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", ""); !a.Valid || !a.Muted {
		t.Fatalf("alice after expiry and re-admission: %+v, want valid and still muted", a)
	}
	if a, _ := s.Lookup(ctx, "ns", "r1", "bob", ""); !a.Valid || a.Muted {
		t.Fatalf("bob: %+v, want valid, unmuted", a)
	}
	var left int
	rows := []struct {
		N int `db:"n"`
	}{}
	if err := s.db.Query(ctx, &rows, `SELECT COUNT(*) AS n FROM webrtc_admissions WHERE expires_at <= ?`, now.Unix()); err != nil {
		t.Fatal(err)
	}
	left = rows[0].N
	if left != 0 {
		t.Fatalf("%d expired rows remain", left)
	}
}

// F8: a table of the same name that predates migration 073 is refused with a
// message that says so, and nothing reads the tenant's rows as admissions.
func TestAdmissionStore_foreignTableIsRefusedWithAClearError(t *testing.T) {
	for name, ddl := range map[string]string{
		"missing columns": `CREATE TABLE webrtc_admissions (id INTEGER PRIMARY KEY, who TEXT)`,
		"no composite key": `CREATE TABLE webrtc_admissions (namespace TEXT, room TEXT, user_id TEXT, device_id TEXT NOT NULL DEFAULT '',
			expires_at INTEGER, revoked_at INTEGER, muted INTEGER, created_at INTEGER)`,
	} {
		t.Run(name, func(t *testing.T) {
			client, _ := rqlitetest.SQLite(t, `CREATE TABLE webrtc_settings (namespace TEXT NOT NULL PRIMARY KEY, require_admission INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL)`, ddl)
			s := NewAdmissionStore(client)
			ctx := context.Background()
			if _, err := s.Lookup(ctx, "ns", "r1", "alice", ""); err == nil || !strings.Contains(err.Error(), "webrtc_admissions") || !strings.Contains(err.Error(), "migration 073") {
				t.Fatalf("Lookup err = %v, want one naming the table and the migration", err)
			}
			if _, err := s.Admit(ctx, "ns", "r1", "alice", "", time.Hour); err == nil {
				t.Fatal("an admission was recorded in a table that is not the admission table")
			}
		})
	}
}

func TestSignalHandler_foreignTableIsNotRetryable(t *testing.T) {
	_, dir := threeSFUs(t)
	var target string
	h := gatewayFor(t, dir, &target)
	ctx := context.Background()
	h.admissions.db.Exec(ctx, `DROP TABLE webrtc_admissions`)
	h.admissions.db.Exec(ctx, `CREATE TABLE webrtc_admissions (id INTEGER PRIMARY KEY, who TEXT)`)

	_, w := signalAs(h, "r1", testUser, "")

	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), `"retryable":true`) || !strings.Contains(w.Body.String(), "webrtc_admissions") {
		t.Fatalf("status %d %s, want a 500 that names the table and is not retryable", w.Code, w.Body)
	}
	if target != "" {
		t.Fatal("a join was proxied")
	}
}

func TestAdmissionStore_schemaIsCheckedAgainAfterAFailure(t *testing.T) {
	s, _, _ := newSQLiteStore(t)
	ctx := context.Background()
	s.db.Exec(ctx, `ALTER TABLE webrtc_admissions RENAME TO webrtc_admissions_ok`)
	if _, err := s.Lookup(ctx, "ns", "r", "u", ""); err == nil {
		t.Fatal("a missing table was read")
	}
	s.db.Exec(ctx, `ALTER TABLE webrtc_admissions_ok RENAME TO webrtc_admissions`)
	if _, err := s.Lookup(ctx, "ns", "r", "u", ""); err != nil {
		t.Fatalf("after the table came back: %v", err)
	}
}

// MEDIUM-1: kick and mute go to every SFU of the namespace.
func twoSFUController(t *testing.T) (*WebRTCHandlers, *controlSFU, *controlSFU) {
	t.Helper()
	a, b := newControlSFU(t, "a"), newControlSFU(t, "b")
	h := controllerFor(t, a)
	h.SetSFUDirectory(staticDirectory{nodes: []SFUNode{a.node, b.node}})
	return h, a, b
}

func TestKick_reachesEverySFUNotOnlyTheRoomsOwner(t *testing.T) {
	h, a, b := twoSFUController(t)
	h.Admit(bg, "ns", "r1", "alice", "", time.Hour)

	if err := h.Kick(bg, "ns", "r1", "alice"); err != nil {
		t.Fatal(err)
	}
	if len(a.kicks) != 1 || len(b.kicks) != 1 {
		t.Fatalf("kicks: a=%d b=%d, want the kick on both", len(a.kicks), len(b.kicks))
	}
	if err := h.Mute(bg, "ns", "r1", "alice", true); err != nil {
		t.Fatal(err)
	}
	if len(a.mutes) != 1 || len(b.mutes) != 1 {
		t.Fatalf("mutes: a=%d b=%d, want the mute on both", len(a.mutes), len(b.mutes))
	}
}

func TestKick_anUnreachableSFUIsAnErrorAndTheOthersAreStillTold(t *testing.T) {
	h, a, b := twoSFUController(t)
	b.status = http.StatusInternalServerError
	h.Admit(bg, "ns", "r1", "alice", "", time.Hour)

	err := h.Kick(bg, "ns", "r1", "alice")

	if err == nil || !strings.Contains(err.Error(), "SFU b") || strings.Contains(err.Error(), "SFU a ") {
		t.Fatalf("err = %v, want it to name SFU b only", err)
	}
	if len(a.kicks) != 1 {
		t.Fatal("the reachable SFU was not told")
	}
	if l, _ := h.admissions.Lookup(bg, "ns", "r1", "alice", ""); !l.Revoked {
		t.Fatal("the revocation was undone")
	}

	b.srvClose()
	if err := h.Kick(bg, "ns", "r1", "alice"); err == nil || !strings.Contains(err.Error(), "failed to reach SFU b") {
		t.Fatalf("a stopped SFU: err = %v", err)
	}
}

// LOW-7: the controller acts on its own namespace only.
func TestController_refusesAnotherNamespace(t *testing.T) {
	sfu := newControlSFU(t, "a")
	h := controllerFor(t, sfu)
	h.Admit(bg, "ns", "r1", "alice", "", time.Hour)

	if _, err := h.Admit(bg, "other", "r1", "alice", "", time.Hour); err == nil || !strings.Contains(err.Error(), `"other"`) {
		t.Errorf("Admit in another namespace: %v", err)
	}
	if err := h.Kick(bg, "other", "r1", "alice"); err == nil {
		t.Error("Kick in another namespace was accepted")
	}
	if err := h.Mute(bg, "other", "r1", "alice", true); err == nil {
		t.Error("Mute in another namespace was accepted")
	}
	if len(sfu.kicks)+len(sfu.mutes) != 0 {
		t.Error("an SFU was told to act for another namespace")
	}
	if a, _ := h.admissions.Lookup(bg, "ns", "r1", "alice", ""); a.Revoked || a.Muted {
		t.Errorf("another namespace's call changed this one's records: %+v", a)
	}

	h.namespace = ""
	if err := h.Kick(bg, "ns", "r1", "alice"); err == nil {
		t.Error("a gateway with no namespace acted")
	}
}

// LOW-1: an SFU's report is served once.
func TestEventsHandler_aReplayedReportIsRefused(t *testing.T) {
	h, rec := eventHandlers(t)
	body := []byte(`{"type":"join","room":"r1","user_id":"0xalice","peer_id":"p1","at":"2027-01-01T00:00:00Z"}`)
	key, _ := ctrlauth.Key(testTURNSecret)
	mac := ctrlauth.Sign(key, testSink, http.MethodPost, ctrlauth.EventsPath, body, time.Now())
	send := func() int {
		req := httptest.NewRequest(http.MethodPost, ctrlauth.EventsPath, strings.NewReader(string(body)))
		req.Header.Set(ctrlauth.MACHeader, mac)
		w := httptest.NewRecorder()
		h.EventsHandler(w, req)
		return w.Code
	}

	if code := send(); code != http.StatusOK {
		t.Fatalf("first: %d", code)
	}
	if code := send(); code != http.StatusUnauthorized {
		t.Fatalf("replay: %d, want 401", code)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.got) != 1 {
		t.Fatalf("published %d times, want once", len(rec.got))
	}
}

// The platform's own table as migration 073 made it, before 075 added the
// generation: the refusal says the migration has not run yet. It used to tell
// the operator to rename or drop the platform's own table.
func TestAdmissionStore_aTableBefore075IsReportedAsUnmigrated(t *testing.T) {
	client, _ := rqlitetest.SQLite(t,
		`CREATE TABLE webrtc_settings (namespace TEXT NOT NULL PRIMARY KEY, require_admission INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE webrtc_admissions (namespace TEXT NOT NULL, room TEXT NOT NULL, user_id TEXT NOT NULL, device_id TEXT NOT NULL DEFAULT '',
			expires_at INTEGER NOT NULL, revoked_at INTEGER, muted INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL,
			PRIMARY KEY (namespace, room, user_id, device_id))`)
	s := NewAdmissionStore(client)
	_, err := s.Lookup(context.Background(), "ns", "r1", "alice", "")
	if err == nil || !strings.Contains(err.Error(), "migrations 075 and 095 have not been applied") || strings.Contains(err.Error(), "drop it") {
		t.Fatalf("Lookup err = %v, want one saying 075 and 095 are not applied yet, not to drop the table", err)
	}
}

// The platform's table as 075 left it, before 095 added video_muted.
func TestAdmissionStore_aTableBefore095IsReportedAsUnmigrated(t *testing.T) {
	client, _ := rqlitetest.SQLite(t,
		`CREATE TABLE webrtc_settings (namespace TEXT NOT NULL PRIMARY KEY, require_admission INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE webrtc_admissions (namespace TEXT NOT NULL, room TEXT NOT NULL, user_id TEXT NOT NULL, device_id TEXT NOT NULL DEFAULT '',
			expires_at INTEGER NOT NULL, revoked_at INTEGER, muted INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL,
			generation INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (namespace, room, user_id, device_id))`)
	s := NewAdmissionStore(client)
	_, err := s.Lookup(context.Background(), "ns", "r1", "alice", "")
	if err == nil || !strings.Contains(err.Error(), "lacks video_muted: migration 095 has not been applied") || strings.Contains(err.Error(), "drop it") {
		t.Fatalf("Lookup err = %v, want one saying 095 is not applied yet", err)
	}
}
