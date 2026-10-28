package webrtc

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Orama-Network/orama/core/pkg/rqlite"
	"github.com/Orama-Network/orama/core/pkg/serverless"
	"github.com/Orama-Network/orama/core/pkg/sfu/ctrlauth"
)

// generationRetention is how long an expired or revoked admission is kept: its
// generation is what the next admission of the same user and room counts on, and
// a kick of it is judged by the SFUs for kickWindow (ctrlauth.TicketTTL plus a
// margin). Purged sooner, a user admitted again would restart at generation 1
// and be refused as kicked by a kick of a higher one.
const generationRetention = 2 * ctrlauth.TicketTTL

// mutedRetention is how long a muted admission is kept after it expires. A mute
// outlives its admission so that admitting the user again does not unmute them,
// but not for ever: the table would grow by one row per muted user and room
// without bound. A user re-admitted after this long is unmuted.
const mutedRetention = 7 * 24 * time.Hour

// AdmissionStore is where a namespace's WebRTC policy and the admissions its
// functions issued live: the namespace's own database (migrations 073, 075 and 095), read on
// the join path of its gateways. No SFU holds any of it, so it survives an SFU
// restart and is the same whichever node owns the room.
type AdmissionStore struct {
	db  rqlite.Client
	now func() time.Time

	// schemaOK is set once the tables are known to be the ones migration 073
	// makes (admission_schema.go).
	schemaMu sync.Mutex
	schemaOK bool
}

// NewAdmissionStore returns a store over the namespace's own database.
func NewAdmissionStore(db rqlite.Client) *AdmissionStore {
	return &AdmissionStore{db: db, now: time.Now}
}

// Admission is what the namespace has on record for one user in one room.
type Admission struct {
	// Valid is set when an admission applies to the caller's device, has
	// neither expired nor been revoked, and is not outranked by a newer
	// revocation of that device (judge).
	Valid bool
	// Revoked and Expired explain a refusal; Found is false when the namespace
	// never admitted this user to this room.
	Found, Revoked, Expired bool
	// Muted and VideoMuted are set when the namespace has muted this user's
	// audio, or their video, in this room.
	Muted, VideoMuted bool
	// ValidUntil is the unix second the latest valid admission ends (0 when
	// none is valid).
	ValidUntil int64
	// Generation is the newest generation among the valid admissions (0 when
	// none is valid, or when it predates generations): what a join ticket
	// carries so an SFU can tell it from a ticket of a revoked admission.
	Generation int64
}

type admissionRow struct {
	DeviceID   string `db:"device_id"`
	ExpiresAt  int64  `db:"expires_at"`
	RevokedAt  int64  `db:"revoked_at"`
	Muted      int    `db:"muted"`
	VideoMuted int    `db:"video_muted"`
	Gen        int64  `db:"generation"`
}

// Media is what a mute stops the forwarding of: a user's audio, or their
// video (camera and screen).
type Media int

const (
	MediaAudio Media = iota
	MediaVideo
)

// column is where the mute of m is recorded. It is one of two constants, never
// a value a caller supplies, so it is safe to build a statement with.
func (m Media) column() string {
	if m == MediaVideo {
		return "video_muted"
	}
	return "muted"
}

func (m Media) String() string {
	if m == MediaVideo {
		return "video"
	}
	return "audio"
}

// RequireAdmission reports whether the namespace admits only users its
// functions admitted. A namespace that never set the policy does not.
func (s *AdmissionStore) RequireAdmission(ctx context.Context, namespace string) (bool, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return false, err
	}
	var rows []struct {
		Require int `db:"require_admission"`
	}
	if err := s.db.Query(ctx, &rows, `SELECT require_admission FROM webrtc_settings WHERE namespace = ?`, namespace); err != nil {
		return false, fmt.Errorf("failed to read the WebRTC admission policy of namespace %q: %w", namespace, err)
	}
	return len(rows) > 0 && rows[0].Require != 0, nil
}

// SetRequireAdmission sets the namespace's admission policy.
func (s *AdmissionStore) SetRequireAdmission(ctx context.Context, namespace string, require bool) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	flag := 0
	if require {
		flag = 1
	}
	_, err := s.db.Exec(ctx, `INSERT INTO webrtc_settings (namespace, require_admission, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(namespace) DO UPDATE SET require_admission = excluded.require_admission, updated_at = excluded.updated_at`,
		namespace, flag, s.now().Unix())
	if err != nil {
		return fmt.Errorf("failed to save the WebRTC admission policy of namespace %q: %w", namespace, err)
	}
	return nil
}

// Lookup reads what the namespace has on record for user, on device (""
// when the session is bound to none), in room. An admission for no particular
// device ("") applies to every device; one for a device applies to it alone.
func (s *AdmissionStore) Lookup(ctx context.Context, namespace, room, user, device string) (Admission, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return Admission{}, err
	}
	var rows []admissionRow
	err := s.db.Query(ctx, &rows, `SELECT device_id, expires_at, COALESCE(revoked_at, 0) AS revoked_at, muted, video_muted, generation
		FROM webrtc_admissions
		WHERE namespace = ? AND room = ? AND user_id = ? AND device_id IN (?, '')`,
		namespace, room, user, device)
	if err != nil {
		return Admission{}, fmt.Errorf("failed to read the WebRTC admissions of %q in room %q: %w", user, room, err)
	}
	return judge(rows, s.now().Unix()), nil
}

// nextGeneration is the generation of the next admission of a (namespace, room,
// user): one more than the newest on any device, 1 for the first. It takes the
// three as bound parameters, after the statement's own.
const nextGeneration = `(SELECT COALESCE(MAX(generation), 0) + 1 FROM webrtc_admissions WHERE namespace = ? AND room = ? AND user_id = ?)`

// judge picks the verdict over the rows that apply (the device's own and the
// one for every device): a valid one admits, unless a revocation of a newer
// generation outranks it. Every admit and every device kick takes the next
// generation, so the newer of the two decisions wins: a kick of the device
// refuses it although the user is admitted from every device, and admitting the
// user from every device again (or the device again) lets it back in. Rows that
// predate generations are all 0, and none outranks another.
func judge(rows []admissionRow, now int64) Admission {
	var a Admission
	var newestRevoked int64
	for _, r := range rows {
		if r.RevokedAt != 0 && r.Gen > newestRevoked {
			newestRevoked = r.Gen
		}
	}
	for _, r := range rows {
		a.Found = true
		a.Muted = a.Muted || r.Muted != 0
		a.VideoMuted = a.VideoMuted || r.VideoMuted != 0
		switch {
		case r.RevokedAt != 0:
			a.Revoked = true
		case r.ExpiresAt <= now:
			a.Expired = true
		case r.Gen < newestRevoked:
			a.Revoked = true
		default:
			a.Valid = true
			if r.ExpiresAt > a.ValidUntil {
				a.ValidUntil = r.ExpiresAt
			}
			if r.Gen > a.Generation {
				a.Generation = r.Gen
			}
		}
	}
	return a
}

// Admit records that user may join room for ttl, from device (or from any
// device when ""). Admitting again extends the admission and lifts a revocation;
// a mute on record stays. Admissions that expired are removed here, so the table
// holds what is live, except that an admission is kept for generationRetention
// after it ends (expired or revoked: removing it sooner would restart the
// user's generation count) and a muted one, audio or video, for mutedRetention
// after it expires (removing it sooner would unmute the user on their next
// admission).
//
// Every admission takes the next generation of its user in its room, whatever
// the device, in the same statement that writes it.
func (s *AdmissionStore) Admit(ctx context.Context, namespace, room, user, device string, ttl time.Duration) (time.Time, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return time.Time{}, err
	}
	now := s.now()
	expires := now.Add(ttl)
	if _, err := s.db.Exec(ctx, purgeEnded, purgeArgs(namespace, now)...); err != nil {
		return time.Time{}, fmt.Errorf("failed to remove expired WebRTC admissions of namespace %q: %w", namespace, err)
	}
	_, err := s.db.Exec(ctx, `INSERT INTO webrtc_admissions (namespace, room, user_id, device_id, expires_at, revoked_at, muted, created_at, generation)
		VALUES (?, ?, ?, ?, ?, NULL, 0, ?, `+nextGeneration+`)
		ON CONFLICT(namespace, room, user_id, device_id) DO UPDATE SET expires_at = excluded.expires_at, revoked_at = NULL,
		generation = excluded.generation`,
		namespace, room, user, device, expires.Unix(), now.Unix(), namespace, room, user)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to record the WebRTC admission of %q to room %q: %w", user, room, err)
	}
	return time.Unix(expires.Unix(), 0), nil
}

// purgeEnded removes the admissions Admit no longer keeps (see Admit); its
// arguments are purgeArgs.
const purgeEnded = `DELETE FROM webrtc_admissions WHERE namespace = ?
	AND MAX(expires_at, COALESCE(revoked_at, 0)) <= ? AND ((muted = 0 AND video_muted = 0) OR expires_at <= ?)`

func purgeArgs(namespace string, now time.Time) []any {
	return []any{namespace, now.Add(-generationRetention).Unix(), now.Add(-mutedRetention).Unix()}
}

// AdmitWithMutes is Admit stating the user's mutes in room (feat-972): the
// admission is written muted as mutes says, and every other admission of the
// user in the room takes the same mutes, in one transaction, so the first join
// the admission lets in is judged with them and its ticket carries them. The
// mutes are the user's, as webrtc_mute sets them, not the device's: judge
// reads a device's admission together with the user's admission for every
// device.
func (s *AdmissionStore) AdmitWithMutes(ctx context.Context, namespace, room, user, device string, ttl time.Duration,
	mutes serverless.WebRTCMutes) (time.Time, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return time.Time{}, err
	}
	now := s.now()
	expires := now.Add(ttl)
	audio, video := flagOf(mutes.Muted), flagOf(mutes.VideoMuted)
	res, err := s.db.Batch(ctx, []rqlite.BatchOp{{
		Kind: rqlite.BatchOpExec, SQL: purgeEnded, Args: purgeArgs(namespace, now),
	}, {
		Kind: rqlite.BatchOpExec,
		SQL: `INSERT INTO webrtc_admissions (namespace, room, user_id, device_id, expires_at, revoked_at, muted, video_muted, created_at, generation)
		VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?, ` + nextGeneration + `)
		ON CONFLICT(namespace, room, user_id, device_id) DO UPDATE SET expires_at = excluded.expires_at, revoked_at = NULL,
		muted = excluded.muted, video_muted = excluded.video_muted, generation = excluded.generation`,
		Args: []any{namespace, room, user, device, expires.Unix(), audio, video, now.Unix(), namespace, room, user},
	}, {
		Kind: rqlite.BatchOpExec,
		SQL:  `UPDATE webrtc_admissions SET muted = ?, video_muted = ? WHERE namespace = ? AND room = ? AND user_id = ?`,
		Args: []any{audio, video, namespace, room, user},
	}})
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to record the WebRTC admission of %q to room %q with its mutes: %w", user, room, err)
	}
	if !res.Committed {
		return time.Time{}, fmt.Errorf("the WebRTC admission of %q to room %q with its mutes was rolled back at statement %d: %s",
			user, room, res.FailedIndex, res.Error)
	}
	return time.Unix(expires.Unix(), 0), nil
}

// StaleMutes returns the media whose mute, as mutes states it, an SFU may not
// hold for user in room yet: a ticket issued on the user's admissions, or the
// connection one let in, carries the mutes on record when it was issued, so a
// medium the records hold in another state on any admission of the user is
// stale. A user with no admission in a namespace that requires one has neither
// ticket nor connection, and nothing is stale: the first join's ticket carries
// the mutes. In a namespace that admits without an admission, such a user may
// be connected in whatever state a webrtc_mute left, so both media are.
func (s *AdmissionStore) StaleMutes(ctx context.Context, namespace, room, user string, mutes serverless.WebRTCMutes) ([]Media, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	var rows []struct {
		N        int64 `db:"n"`
		MinAudio int   `db:"min_audio"`
		MaxAudio int   `db:"max_audio"`
		MinVideo int   `db:"min_video"`
		MaxVideo int   `db:"max_video"`
	}
	err := s.db.Query(ctx, &rows, `SELECT COUNT(*) AS n, COALESCE(MIN(muted), 0) AS min_audio, COALESCE(MAX(muted), 0) AS max_audio,
		COALESCE(MIN(video_muted), 0) AS min_video, COALESCE(MAX(video_muted), 0) AS max_video
		FROM webrtc_admissions WHERE namespace = ? AND room = ? AND user_id = ?`, namespace, room, user)
	if err != nil {
		return nil, fmt.Errorf("failed to read the WebRTC mutes of %q in room %q: %w", user, room, err)
	}
	if len(rows) == 0 || rows[0].N == 0 {
		require, err := s.RequireAdmission(ctx, namespace)
		if err != nil || require {
			return nil, err
		}
		return []Media{MediaAudio, MediaVideo}, nil
	}
	r := rows[0]
	var stale []Media
	if a := flagOf(mutes.Muted); r.MinAudio != a || r.MaxAudio != a {
		stale = append(stale, MediaAudio)
	}
	if v := flagOf(mutes.VideoMuted); r.MinVideo != v || r.MaxVideo != v {
		stale = append(stale, MediaVideo)
	}
	return stale, nil
}

// flagOf is how a mute is stored: 1 when muted, 0 when not.
func flagOf(muted bool) int {
	if muted {
		return 1
	}
	return 0
}

// Revoke ends every admission of user to room, whatever the device, and returns
// the newest generation it revoked: the kick tells the SFUs, which refuse a
// ticket issued on that generation or an older one. A user with no admission is
// not an error (the namespace may not require admission): the generation is 0,
// and the SFUs then judge a ticket by the clocks.
func (s *AdmissionStore) Revoke(ctx context.Context, namespace, room, user string) (int64, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return 0, err
	}
	_, err := s.db.Exec(ctx, `UPDATE webrtc_admissions SET revoked_at = ? WHERE namespace = ? AND room = ? AND user_id = ?`,
		s.now().Unix(), namespace, room, user)
	if err != nil {
		return 0, fmt.Errorf("failed to revoke the WebRTC admissions of %q in room %q: %w", user, room, err)
	}
	return s.newestGeneration(ctx, namespace, room, user)
}

// RevokeDevice ends the admission of one device of user to room, and refuses
// that device although an admission for every device of the user is valid: the
// device's row is marked revoked (written when it has none) and takes the next
// generation, which outranks every admission made before it (judge). The row is
// kept at least as long as the admission for every device it outranks, so the
// purge cannot let the device back in. The user's other devices keep their
// admissions. It returns the generation the SFUs refuse the device's tickets up
// to, as Revoke does.
func (s *AdmissionStore) RevokeDevice(ctx context.Context, namespace, room, user, device string) (int64, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return 0, err
	}
	now := s.now().Unix()
	ofUser := `FROM webrtc_admissions WHERE namespace = ? AND room = ? AND user_id = ?`
	_, err := s.db.Exec(ctx, `INSERT INTO webrtc_admissions (namespace, room, user_id, device_id, expires_at, revoked_at, muted, video_muted, created_at, generation)
		VALUES (?, ?, ?, ?, MAX(?, (SELECT COALESCE(MAX(expires_at), 0) `+ofUser+` AND device_id = '')), ?,
		(SELECT COALESCE(MAX(muted), 0) `+ofUser+`), (SELECT COALESCE(MAX(video_muted), 0) `+ofUser+`), ?, `+nextGeneration+`)
		ON CONFLICT(namespace, room, user_id, device_id) DO UPDATE SET revoked_at = excluded.revoked_at,
		generation = excluded.generation, expires_at = MAX(webrtc_admissions.expires_at, excluded.expires_at)`,
		namespace, room, user, device, now, namespace, room, user, now,
		namespace, room, user, namespace, room, user, now, namespace, room, user)
	if err != nil {
		return 0, fmt.Errorf("failed to revoke the WebRTC admission of device %q of %q in room %q: %w", device, user, room, err)
	}
	return s.newestGeneration(ctx, namespace, room, user)
}

// newestGeneration is the highest generation the user holds in the room,
// revoked or not: what a kick tells the SFUs. An admission made between the
// revocation and this read can only raise it, which refuses that admission's
// tickets until the next one: a false refusal, never a ticket the kick should
// have stopped let through. Reading only revoked rows could miss the generation
// the revocation wrote, when a re-admit rewrote that row in between.
func (s *AdmissionStore) newestGeneration(ctx context.Context, namespace, room, user string) (int64, error) {
	var rows []struct {
		Gen int64 `db:"gen"`
	}
	err := s.db.Query(ctx, &rows, `SELECT COALESCE(MAX(generation), 0) AS gen FROM webrtc_admissions
		WHERE namespace = ? AND room = ? AND user_id = ?`, namespace, room, user)
	if err != nil {
		return 0, fmt.Errorf("failed to read the revoked generation of %q in room %q: %w", user, room, err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Gen, nil
}

// SetMuted records the mute of m on every admission of user to room, so it
// holds when they rejoin. With no admission on record there is nothing to keep
// it on: the mute then lasts as long as the peer's connection.
func (s *AdmissionStore) SetMuted(ctx context.Context, namespace, room, user string, m Media, muted bool) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE webrtc_admissions SET `+m.column()+` = ? WHERE namespace = ? AND room = ? AND user_id = ?`,
		flagOf(muted), namespace, room, user)
	if err != nil {
		return fmt.Errorf("failed to record the %s mute of %q in room %q: %w", m, user, room, err)
	}
	return nil
}
