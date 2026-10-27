package webrtc

import (
	"context"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/rqlite/rqlitetest"
)

// newRqliteStore is a store over a real rqlited: rqlite is not SQLite, and the
// upsert, the NULL handling and the types are what these tests are about.
func newRqliteStore(t *testing.T) *AdmissionStore {
	t.Helper()
	client := rqlitetest.Start(t)
	for _, stmt := range admissionDDL(t) {
		if _, err := client.Exec(context.Background(), stmt); err != nil {
			t.Fatalf("apply ddl: %v", err)
		}
	}
	return NewAdmissionStore(client)
}

func TestAdmissionStore_requireAdmissionDefaultsToOffAndRoundTrips(t *testing.T) {
	s, _, _ := newSQLiteStore(t)
	ctx := context.Background()

	if on, err := s.RequireAdmission(ctx, "ns"); err != nil || on {
		t.Fatalf("a namespace that never set the policy: on=%v err=%v, want off", on, err)
	}
	for _, want := range []bool{true, false, true} {
		if err := s.SetRequireAdmission(ctx, "ns", want); err != nil {
			t.Fatal(err)
		}
		if got, err := s.RequireAdmission(ctx, "ns"); err != nil || got != want {
			t.Fatalf("after setting %v: got %v err=%v", want, got, err)
		}
	}
	if on, _ := s.RequireAdmission(ctx, "another"); on {
		t.Error("one namespace's policy leaked into another")
	}
}

func TestAdmissionStore_admitThenLookup(t *testing.T) {
	s, _, now := newSQLiteStore(t)
	ctx := context.Background()

	expires, err := s.Admit(ctx, "ns", "r1", "alice", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(time.Hour); !expires.Equal(want) {
		t.Fatalf("expires = %v, want %v", expires, want)
	}
	if a, err := s.Lookup(ctx, "ns", "r1", "alice", "any-device"); err != nil || !a.Valid {
		t.Fatalf("an admission for no device did not admit a device: %+v err=%v", a, err)
	}
	for name, args := range map[string][3]string{
		"other room": {"r2", "alice", ""},
		"other user": {"r1", "bob", ""},
	} {
		if a, _ := s.Lookup(ctx, "ns", args[0], args[1], args[2]); a.Valid || a.Found {
			t.Errorf("%s was admitted: %+v", name, a)
		}
	}
	if a, _ := s.Lookup(ctx, "other-ns", "r1", "alice", ""); a.Valid {
		t.Error("an admission of one namespace admitted in another")
	}
}

func TestAdmissionStore_deviceBoundAdmissionAdmitsThatDeviceOnly(t *testing.T) {
	s, _, _ := newSQLiteStore(t)
	ctx := context.Background()
	if _, err := s.Admit(ctx, "ns", "r1", "alice", "phone", time.Hour); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", "phone"); !a.Valid {
		t.Error("the named device was not admitted")
	}
	for _, device := range []string{"laptop", ""} {
		if a, _ := s.Lookup(ctx, "ns", "r1", "alice", device); a.Valid {
			t.Errorf("device %q was admitted on an admission for the phone", device)
		}
	}
}

func TestAdmissionStore_expiryRevocationAndReadmission(t *testing.T) {
	s, _, now := newSQLiteStore(t)
	ctx := context.Background()
	s.Admit(ctx, "ns", "r1", "alice", "", time.Minute)

	*now = now.Add(time.Minute) // exactly at the expiry: over
	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", ""); a.Valid || !a.Expired || a.Revoked {
		t.Fatalf("at expiry: %+v, want expired", a)
	}

	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour)
	if _, err := s.Revoke(ctx, "ns", "r1", "alice"); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", ""); a.Valid || !a.Revoked {
		t.Fatalf("after revoke: %+v, want revoked", a)
	}

	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour) // the namespace decides to let them back
	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", ""); !a.Valid || a.Revoked {
		t.Fatalf("after re-admission: %+v, want valid", a)
	}
}

func TestAdmissionStore_revokeOfAnUnadmittedUserIsNotAnError(t *testing.T) {
	s, _, _ := newSQLiteStore(t)
	if gen, err := s.Revoke(context.Background(), "ns", "r1", "nobody"); err != nil || gen != 0 {
		t.Fatalf("revoke with nothing on record: %v", err)
	}
}

func TestAdmissionStore_muteSurvivesReadmissionAndIsPerRoomAndUser(t *testing.T) {
	s, _, _ := newSQLiteStore(t)
	ctx := context.Background()
	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour)
	s.Admit(ctx, "ns", "r1", "bob", "", time.Hour)
	s.Admit(ctx, "ns", "r2", "alice", "", time.Hour)

	if err := s.SetMuted(ctx, "ns", "r1", "alice", MediaAudio, true); err != nil {
		t.Fatal(err)
	}
	s.Admit(ctx, "ns", "r1", "alice", "", 2*time.Hour)

	for _, c := range []struct {
		room, user string
		muted      bool
	}{{"r1", "alice", true}, {"r1", "bob", false}, {"r2", "alice", false}} {
		if a, _ := s.Lookup(ctx, "ns", c.room, c.user, ""); a.Muted != c.muted {
			t.Errorf("%s/%s muted = %v, want %v", c.room, c.user, a.Muted, c.muted)
		}
	}
	s.SetMuted(ctx, "ns", "r1", "alice", MediaAudio, false)
	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", ""); a.Muted {
		t.Error("unmuting did not take")
	}
}

func TestAdmissionStore_admitRemovesExpiredAdmissions(t *testing.T) {
	s, db, now := newSQLiteStore(t)
	ctx := context.Background()
	s.Admit(ctx, "ns", "old", "alice", "", time.Minute)
	*now = now.Add(time.Hour)
	s.Admit(ctx, "ns", "new", "alice", "", time.Minute)

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM webrtc_admissions`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows = %d err=%v, want only the live admission", n, err)
	}
}

func TestJudge_anyValidRowAdmits(t *testing.T) {
	now := int64(1000)
	a := judge([]admissionRow{
		{DeviceID: "", ExpiresAt: 500},
		{DeviceID: "phone", ExpiresAt: 2000},
		{DeviceID: "x", ExpiresAt: 2000, RevokedAt: 900},
	}, now)
	if !a.Valid || !a.Expired || !a.Revoked || !a.Found {
		t.Fatalf("verdict = %+v", a)
	}
	if got := judge(nil, now); got.Found || got.Valid {
		t.Fatalf("no rows: %+v", got)
	}
}

// The same behaviour on a real rqlited, where the upsert and the NULL
// revoked_at travel through its HTTP API.
func TestAdmissionStore_onRealRqlite(t *testing.T) {
	s := newRqliteStore(t)
	ctx := context.Background()

	if err := s.SetRequireAdmission(ctx, "ns", true); err != nil {
		t.Fatal(err)
	}
	if on, err := s.RequireAdmission(ctx, "ns"); err != nil || !on {
		t.Fatalf("policy = %v err=%v", on, err)
	}
	if _, err := s.Admit(ctx, "ns", "r1", "alice", "", time.Hour); err != nil {
		t.Fatal(err)
	}
	if a, err := s.Lookup(ctx, "ns", "r1", "alice", "dev"); err != nil || !a.Valid {
		t.Fatalf("admission = %+v err=%v", a, err)
	}
	s.SetMuted(ctx, "ns", "r1", "alice", MediaAudio, true)
	s.Revoke(ctx, "ns", "r1", "alice")
	a, err := s.Lookup(ctx, "ns", "r1", "alice", "")
	if err != nil || a.Valid || !a.Revoked || !a.Muted {
		t.Fatalf("after mute and revoke: %+v err=%v", a, err)
	}
	if _, err := s.Admit(ctx, "ns", "r1", "alice", "", time.Hour); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", ""); !a.Valid || a.Revoked {
		t.Fatalf("after re-admission: %+v", a)
	}
	// feat-964: the device revocation's upsert and the video mute's column.
	s.SetMuted(ctx, "ns", "r1", "alice", MediaVideo, true)
	gen, err := s.RevokeDevice(ctx, "ns", "r1", "alice", "did-phone")
	if err != nil || gen == 0 {
		t.Fatalf("RevokeDevice = %d, %v", gen, err)
	}
	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", "did-phone"); a.Valid || !a.Revoked || !a.VideoMuted {
		t.Fatalf("the kicked device: %+v, want revoked and video-muted", a)
	}
	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", "did-tablet"); !a.Valid {
		t.Fatalf("another device after the phone's kick: %+v", a)
	}
}

// LOW-D: muted rows are bounded too.
func TestAdmissionStore_mutedRowsArePurgedAfterTheRetention(t *testing.T) {
	s, db, now := newSQLiteStore(t)
	ctx := context.Background()
	count := func() int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM webrtc_admissions`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	s.Admit(ctx, "ns", "r1", "muted", "", time.Hour)
	s.SetMuted(ctx, "ns", "r1", "muted", MediaAudio, true)
	s.Admit(ctx, "ns", "r1", "plain", "", time.Hour)

	// Expired, within the retention: the plain row goes, the muted one stays and
	// is still muted when the user is admitted again.
	*now = now.Add(time.Hour + mutedRetention/2)
	s.Admit(ctx, "ns", "r2", "other", "", time.Hour)
	if got := count(); got != 2 {
		t.Fatalf("%d rows within the retention, want the muted one and the new admission", got)
	}
	s.Admit(ctx, "ns", "r1", "muted", "", time.Hour)
	if a, _ := s.Lookup(ctx, "ns", "r1", "muted", ""); !a.Valid || !a.Muted {
		t.Fatalf("re-admitted within the retention: %+v, want valid and still muted", a)
	}

	// Expired for longer than the retention: gone, and the user starts unmuted.
	*now = now.Add(time.Hour + mutedRetention + time.Minute)
	s.Admit(ctx, "ns", "r3", "later", "", time.Hour)
	if got := count(); got != 1 {
		t.Fatalf("%d rows past the retention, want only the new admission", got)
	}
	s.Admit(ctx, "ns", "r1", "muted", "", time.Hour)
	if a, _ := s.Lookup(ctx, "ns", "r1", "muted", ""); !a.Valid || a.Muted {
		t.Fatalf("re-admitted past the retention: %+v, want valid and unmuted", a)
	}
}

func TestAdmissionStore_purgeLeavesLiveAndOtherNamespacesAlone(t *testing.T) {
	s, db, now := newSQLiteStore(t)
	ctx := context.Background()
	s.Admit(ctx, "other", "r1", "alice", "", time.Hour)
	s.SetMuted(ctx, "other", "r1", "alice", MediaAudio, true)
	s.Admit(ctx, "ns", "r1", "live", "", 30*24*time.Hour)
	s.SetMuted(ctx, "ns", "r1", "live", MediaAudio, true)

	*now = now.Add(2 * mutedRetention)
	s.Admit(ctx, "ns", "r2", "bob", "", time.Hour)

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM webrtc_admissions WHERE user_id IN ('alice','live')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows = %d err=%v, want the live muted one and the other namespace's", n, err)
	}
}

func TestAdmissionStore_generationCountsEveryAdmitOfAUserInARoom(t *testing.T) {
	s, _, _ := newSQLiteStore(t)
	ctx := context.Background()
	gen := func(room, user, device string) int64 {
		a, err := s.Lookup(ctx, "ns", room, user, device)
		if err != nil {
			t.Fatal(err)
		}
		return a.Generation
	}
	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour)
	if g := gen("r1", "alice", ""); g != 1 {
		t.Fatalf("first admission generation = %d, want 1", g)
	}
	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour)
	if g := gen("r1", "alice", ""); g != 2 {
		t.Fatalf("re-admission generation = %d, want 2", g)
	}
	// Another device is another row but the same count: a kick revokes both, so
	// a later admission must outrank both.
	s.Admit(ctx, "ns", "r1", "alice", "phone", time.Hour)
	if g := gen("r1", "alice", "phone"); g != 3 {
		t.Fatalf("second device generation = %d, want 3", g)
	}
	s.Admit(ctx, "ns", "r2", "alice", "", time.Hour)
	s.Admit(ctx, "ns", "r1", "bob", "", time.Hour)
	if g := gen("r2", "alice", ""); g != 1 || gen("r1", "bob", "") != 1 {
		t.Errorf("another room or user shares the count: r2/alice=%d", g)
	}
}

func TestAdmissionStore_revokeReturnsTheRevokedGenerationAndReadmissionOutranksIt(t *testing.T) {
	s, _, _ := newSQLiteStore(t)
	ctx := context.Background()
	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour)
	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour)
	s.Admit(ctx, "ns", "r1", "alice", "phone", time.Hour) // generation 3

	revoked, err := s.Revoke(ctx, "ns", "r1", "alice")
	if err != nil || revoked != 3 {
		t.Fatalf("Revoke = %d, %v, want 3", revoked, err)
	}
	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour)
	a, _ := s.Lookup(ctx, "ns", "r1", "alice", "")
	if !a.Valid || a.Generation <= revoked {
		t.Fatalf("after re-admission: %+v, want valid with a generation above %d", a, revoked)
	}
}

func TestAdmissionStore_aRevokedAdmissionKeepsItsGenerationThroughThePurge(t *testing.T) {
	s, _, now := newSQLiteStore(t)
	ctx := context.Background()
	s.Admit(ctx, "ns", "r1", "alice", "", time.Second)
	s.Admit(ctx, "ns", "r1", "alice", "", time.Second) // generation 2
	*now = now.Add(5 * time.Second)
	revoked, _ := s.Revoke(ctx, "ns", "r1", "alice") // expired, then kicked
	*now = now.Add(generationRetention - time.Second)
	s.Admit(ctx, "ns", "other", "bob", "", time.Hour) // purges what it may
	s.Admit(ctx, "ns", "r1", "alice", "", time.Hour)
	if a, _ := s.Lookup(ctx, "ns", "r1", "alice", ""); a.Generation <= revoked {
		t.Fatalf("generation %d after a purge, want above the kicked %d", a.Generation, revoked)
	}
}
