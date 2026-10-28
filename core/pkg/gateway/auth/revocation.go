package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Orama-Network/orama/core/pkg/client"
	"github.com/Orama-Network/orama/core/pkg/logging"
	"go.uber.org/zap"
)

// Revoking an API key stopped the key authenticating and did nothing to the
// JWTs already exchanged from it — up to fifteen minutes of full access after
// an operator had revoked the credential and been told it was done. Ending one
// session was not possible at all: logout dropped the refresh token and left
// the access token valid.
//
// A token is now checked against a list of revocations on every request. The
// list is small (only revocations whose tokens have not yet expired are in it)
// and is held in memory, refreshed on a timer, because a database round trip
// per request costs a cross-region hop and this runs on every authenticated
// call.
//
// A revocation takes effect within RevocationStaleness. Fifteen minutes became
// ten seconds, which is the point. The list is reloaded at half that, because
// the bound is not the interval alone: the copy a request reads is as old as
// the read that filled it began, which is up to one interval plus the time the
// reload takes, and on a loaded registry that is seconds (stagenet, 2026-09-30:
// a reload every ten seconds took fifteen to reach a gateway).

const (
	// RevocationStaleness is how long a revoked token may still be accepted,
	// the bound the docs promise.
	RevocationStaleness = 10 * time.Second

	// RevocationRefreshInterval is how often the in-memory list is reloaded.
	RevocationRefreshInterval = RevocationStaleness / 2

	// revocationPruneInterval is how often expired rows are deleted. They deny
	// nothing once past expires_at; this keeps the table the size of the
	// revocations still in flight.
	revocationPruneInterval = 1 * time.Hour

	// revocationReloadTimeout bounds one read of the table, and so the longest
	// a request whose copy is already past RevocationStaleness waits for an
	// answer. It is well under the bound: a read of a few hundred small rows is
	// milliseconds, so one that has taken three seconds is hung, and waiting
	// the full ten on every request during a registry hang only delays the 503
	// the client is going to get. It is also under RevocationRefreshInterval,
	// so one attempt ends before the next is due. It is enforced here, not left
	// to the registry client: a client that ignores its context would otherwise
	// hold every stale request forever.
	revocationReloadTimeout = 3 * time.Second

	// revocationRetryInterval is the least time between two reload attempts, so
	// a registry that is down costs one query a second, not one per request.
	revocationRetryInterval = 1 * time.Second
)

// ErrRevocationsUnavailable is returned when the revocation list is older than
// RevocationStaleness, or was never loaded, and the registry cannot be read to
// refresh it. Whether the token is revoked is then unknown, and an unknown
// state is never "not revoked": the caller refuses the request with a
// retryable 503, the way a grant that cannot be read is refused.
//
// Its text is what reaches clients, so it never carries why the registry could
// not be read: that holds internal addresses and stays in the log.
var ErrRevocationsUnavailable = errors.New("token revocations temporarily unavailable")

// revocation is one row: either a named token, or every token issued to a
// subject before a moment.
type revocation struct {
	jti          string
	subject      string
	issuedBefore int64
	expiresAt    int64
}

// RevocationList is the set of tokens this gateway refuses.
type RevocationList struct {
	// registry resolves the database the revocations live in, at call time.
	// See SigningKeys.registry: a namespace gateway is told where its cluster
	// registry is after the auth service is built, and a list bound to the
	// handle it was built with consulted the tenant's own database — where a
	// revocation written on the index never appears.
	registry func() client.DatabaseClient
	logger   *logging.ColoredLogger

	mu        sync.RWMutex
	byJTI     map[string]int64 // jti -> expiry, so a stale entry can be dropped
	bySubject map[string]int64 // subject -> issued_before
	// lastRefresh is when the last SUCCESSFUL read began. A failed reload never
	// moves it: the copy is exactly as old as it is.
	lastRefresh time.Time
	loaded      bool
	// lastAttempt is when the last reload began, successful or not; it only
	// rate-limits retries. Why one failed is logged, never kept: the text
	// reaches nobody.
	lastAttempt time.Time
	// readSeq numbers reads as they begin and appliedSeq is the newest one whose
	// result is in the maps, so a read that finishes late cannot overwrite a
	// newer list.
	readSeq    uint64
	appliedSeq uint64
	// local holds the revocations this gateway recorded itself, so a reload
	// whose read began before one committed does not drop it.
	local []localRevocation
	// abandoned is true while a read Refresh gave up on at its deadline has not
	// returned. It keeps running until the registry client lets it go, so at
	// most one exists: a new attempt does not start another on top of it, which
	// under a registry that never answers would leak a goroutine and a
	// connection every interval. A read that is not abandoned ends within the
	// reload timeout, so this is the only way readers can pile up.
	abandoned bool
	// flight is the reload in progress, nil when none is. At most one runs per
	// list, however many requests find the copy stale.
	flight *reloadFlight

	// now is time.Now, replaced in tests.
	now func() time.Time
	// reloadTimeout is revocationReloadTimeout, shortened in tests.
	reloadTimeout time.Duration
}

// localRevocation is a revocation recorded by this gateway and the moment the
// write returned, which is not before it committed.
type localRevocation struct {
	rev revocation
	at  time.Time
}

// NewRevocationList builds the list a Service consults.
func NewRevocationList(registry func() client.DatabaseClient, logger *logging.ColoredLogger) *RevocationList {
	return &RevocationList{
		registry:  registry,
		logger:    logger,
		byJTI:     map[string]int64{},
		bySubject: map[string]int64{},
		now:       time.Now,

		reloadTimeout: revocationReloadTimeout,
	}
}

// nilListError is what a nil list answers. A list that does not exist knows
// nothing, and "nothing is known" is never "nothing is revoked": a gateway
// built without one refuses instead of waving every token through.
func nilListError() error {
	return fmt.Errorf("%w: this gateway has no revocation list", ErrRevocationsUnavailable)
}

// Denies reports whether a token must be refused.
//
// subjectKeys are the names this token's subject may have been revoked under.
// A wallet is revoked under itself; an API key is revoked under its hash,
// because that is what the revoking code has — a JWT exchanged from a key
// carries the raw key as its subject, and RevokeKey only ever sees the hash.
// The caller derives both rather than this list knowing how keys are hashed.
//
// A token with no jti was minted before tokens carried one. It is still
// checked against the subject revocations, which is what covers a revoked key.
//
// The error is ErrRevocationsUnavailable when the list is too old to answer and
// cannot be refreshed; the bool is then meaningless and the caller must refuse.
func (r *RevocationList) Denies(claims *JWTClaims, subjectKeys []string) (bool, error) {
	if r == nil {
		return false, nilListError()
	}
	if claims == nil {
		return false, nil
	}
	if err := r.refreshIfStale(); err != nil {
		return false, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if claims.Jti != "" {
		if _, denied := r.byJTI[claims.Jti]; denied {
			return true, nil
		}
	}
	// A revoked device or an ended session is refused whenever its token was
	// minted: neither can mint another after the revocation, so there is no
	// newer grant for an issue-time boundary to protect.
	if r.deniesDeviceLocked(claims.Namespace, claims.Did) {
		return true, nil
	}
	if key := bindingRevocationKey(sessionRevocationPrefix, claims.Sid); key != "" {
		if _, denied := r.bySubject[key]; denied {
			return true, nil
		}
	}
	for _, key := range subjectKeys {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		issuedBefore, denied := r.bySubject[key]
		if !denied {
			continue
		}
		// A token minted after the revocation is a new grant — a fresh login,
		// or a new key — and the revocation of the old one does not reach it.
		//
		// The boundary is inclusive because `iat` has one-second resolution: a
		// token minted in the same second as the revocation is exactly what an
		// operator revoking a key means to catch. The cost is that signing in
		// again within that same second is refused and has to be retried,
		// which is the right way round.
		if claims.Iat <= issuedBefore {
			return true, nil
		}
	}
	return false, nil
}

// RevokeSubject refuses every token already issued to a subject.
//
// ttl is how long the tokens it covers may still be valid; the row is pruned
// after that, because past it there is nothing left to deny.
func (r *RevocationList) RevokeSubject(ctx context.Context, subject, reason string, ttl time.Duration) error {
	if r == nil {
		return fmt.Errorf("no revocation list: a revoked credential's tokens would keep working")
	}
	subject = strings.ToLower(strings.TrimSpace(subject))
	if subject == "" {
		return fmt.Errorf("cannot revoke tokens for an empty subject")
	}
	now := r.now()
	return r.insert(ctx, revocation{
		subject:      subject,
		issuedBefore: now.Unix(),
		expiresAt:    now.Add(ttl).Unix(),
	}, reason)
}

// A device or a session is revoked under a subject no wallet or key can have:
// a prefix neither ever carries.
const (
	deviceRevocationPrefix  = "device:"
	sessionRevocationPrefix = "session:"
)

// bindingRevocationKey is the subject a device or session is revoked under, or
// "" when the token is not bound to one.
func bindingRevocationKey(prefix, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	return strings.ToLower(prefix + id)
}

// MaxDeviceIssuedLifetime is the longest anything a device issued can live
// without going back to the device: a capability it minted (feat-264). An
// access token lives far less.
const MaxDeviceIssuedLifetime = 7 * 24 * time.Hour

// RevokeDevice refuses every token bound to a device of namespace, and every
// capability it issued. The entry outlives all of them; past that, the
// device's tombstone is what refuses it, since nothing new can be minted for
// it. It used to last one access-token lifetime, which let a capability the
// device had handed out work again an hour after the device was revoked.
//
// The entry names the namespace (deviceRevocationKey): a device id is a key's
// thumbprint, and an entry naming the id alone would refuse the same key
// enrolled in another namespace for up to seven days.
//
// It is one row carrying both keys (feat-978: remove the legacy half once no
// gateway before 0.3.13 is left). Its subject is the legacy "device:<id>",
// the only column gateways before 0.3.13 match a device on, so they refuse
// the device at once during a rolling upgrade rather than when its tokens
// expire. Its jti is the scoped "device:<namespace>:<id>", which no token's
// jti can be (a token's is random, a capability's starts "cap:"); this
// gateway reads a row with such a jti as the scoped entry alone
// (applyRevocation), so the legacy subject does not refuse the key in other
// namespaces here. One row, so the two cannot be written apart, and its
// unique jti makes a repeated revocation write nothing.
func (r *RevocationList) RevokeDevice(ctx context.Context, namespace, deviceID string) error {
	key := deviceRevocationKey(namespace, deviceID)
	if key == "" {
		return fmt.Errorf("cannot revoke a device with no id")
	}
	now := r.now()
	return r.insert(ctx, revocation{
		jti:          key,
		subject:      legacyDeviceRevocationKey(deviceID),
		issuedBefore: now.Unix(),
		expiresAt:    now.Add(MaxDeviceIssuedLifetime).Unix(),
	}, "device revoked")
}

// DeniesDevice reports whether a device of namespace is on the list. A token
// Denies refused, bound to a device this reports, was refused for its device.
// It answers from the copy Denies just checked, and never reads the registry;
// whether the device is told DEVICE_REVOKED is the registry's to confirm
// (Service.DeviceRevocationRecord).
func (r *RevocationList) DeniesDevice(namespace, deviceID string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.deniesDeviceLocked(namespace, deviceID)
}

// deniesDeviceLocked is DeniesDevice under r.mu. It also honours an entry
// naming the device id alone (legacyDeviceRevocationKey, feat-978): gateways
// before 0.3.13 wrote those, and one written by such a gateway, during a
// rolling upgrade or before it, lives MaxDeviceIssuedLifetime.
func (r *RevocationList) deniesDeviceLocked(namespace, deviceID string) bool {
	for _, key := range []string{deviceRevocationKey(namespace, deviceID), legacyDeviceRevocationKey(deviceID)} {
		if _, denied := r.bySubject[key]; key != "" && denied {
			return true
		}
	}
	return false
}

// deviceRevocationKey is the subject a device of namespace is revoked under:
// "device:<namespace>:<id>", the namespace read as a session names it (none
// is the lobby). "" for no device.
func deviceRevocationKey(namespace, deviceID string) string {
	if strings.TrimSpace(deviceID) == "" {
		return ""
	}
	return bindingRevocationKey(deviceRevocationPrefix+sessionNamespace(namespace)+":", deviceID)
}

// legacyDeviceRevocationKey is the subject gateways before 0.3.13 revoked a
// device under, "device:<id>". Written only as the subject of the dual row
// RevokeDevice writes, for those gateways; an entry under it with no scoped
// jti, which one of them wrote, refuses the device in every namespace
// (feat-978: drop both once no gateway before 0.3.13 is left and the last
// such entry has expired).
func legacyDeviceRevocationKey(deviceID string) string {
	return bindingRevocationKey(deviceRevocationPrefix, deviceID)
}

// RevokeSessionID refuses every access token of one session, whichever
// rotation of its refresh token minted it.
func (r *RevocationList) RevokeSessionID(ctx context.Context, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("cannot revoke a session with no id; it was issued before sessions carried one")
	}
	return r.RevokeSubject(ctx, sessionRevocationPrefix+sessionID, "session ended", MaxTokenLifetime)
}

// RevokeToken refuses one token.
func (r *RevocationList) RevokeToken(ctx context.Context, jti string, expiresAt int64, reason string) error {
	if r == nil {
		return fmt.Errorf("no revocation list: this token would keep working")
	}
	jti = strings.TrimSpace(jti)
	if jti == "" {
		return fmt.Errorf("cannot revoke a token with no id; it was minted before tokens carried one")
	}
	return r.insert(ctx, revocation{jti: jti, expiresAt: expiresAt}, reason)
}

// MaxLiveCapabilityRevocations is how many revocations of one namespace's
// capabilities may be in force at once. A capability revocation lasts until
// the capability it names expires (a week at most), and every gateway reloads
// the whole list every few seconds, so without a ceiling one tenant looping
// mint and revoke grows that reload for every tenant. With it, the list holds
// at most this many capability rows per namespace however a tenant behaves;
// a revocation stops counting the moment the capability it names expires.
const MaxLiveCapabilityRevocations = 1000

// ErrCapabilityRevocationLimit is a capability revocation refused because its
// namespace already has MaxLiveCapabilityRevocations in force.
var ErrCapabilityRevocationLimit = fmt.Errorf(
	"this namespace already has %d capability revocations in force; each stops counting when the capability it names expires",
	MaxLiveCapabilityRevocations)

// RevokeCapability refuses one capability of namespace, named jti on the list,
// until expiresAt. It is refused with ErrCapabilityRevocationLimit when the
// namespace is at its ceiling. A capability already revoked writes nothing.
//
// The ceiling and the write are one statement, so concurrent revokes cannot
// overshoot it between a count and an insert.
func (r *RevocationList) RevokeCapability(ctx context.Context, namespace, jti string, expiresAt int64, reason string) error {
	if r == nil {
		return fmt.Errorf("no revocation list: this capability would keep working")
	}
	namespace, jti = strings.TrimSpace(namespace), strings.TrimSpace(jti)
	if namespace == "" || jti == "" {
		return fmt.Errorf("a capability revocation names its namespace and its id")
	}
	db := r.database()
	if db == nil {
		return fmt.Errorf("no database: the revocation cannot be recorded, so the capability would keep working")
	}
	internalCtx := client.WithInternalAuth(ctx)
	res, err := db.Query(internalCtx,
		`INSERT INTO revoked_tokens(jti, namespace, issued_before, expires_at, reason)
		 SELECT ?, ?, 0, ?, ?
		  WHERE (SELECT COUNT(*) FROM revoked_tokens WHERE namespace = ? AND expires_at > ?) < ?
		 ON CONFLICT(jti) DO NOTHING`,
		jti, namespace, expiresAt, reason, namespace, r.now().Unix(), MaxLiveCapabilityRevocations)
	if err != nil {
		return fmt.Errorf("record the revocation: %w", err)
	}
	if res == nil || res.RowsAffected == 0 {
		// Nothing written: the capability is already on the list, or the
		// namespace is at its ceiling. Only the first is a revocation.
		existing, err := db.Query(internalCtx, `SELECT 1 FROM revoked_tokens WHERE jti = ? LIMIT 1`, jti)
		if err != nil {
			return fmt.Errorf("read back the revocation: %w", err)
		}
		if existing == nil || len(existing.Rows) == 0 {
			return ErrCapabilityRevocationLimit
		}
	}
	r.applyLocal(revocation{jti: jti, expiresAt: expiresAt})
	return nil
}

func (r *RevocationList) insert(ctx context.Context, rev revocation, reason string) error {
	db := r.database()
	if db == nil {
		return fmt.Errorf("no database: the revocation cannot be recorded, so the token would keep working")
	}
	// A token already on the list keeps its row: the write is the check, so
	// two concurrent revokes of one token cannot write two rows.
	internalCtx := client.WithInternalAuth(ctx)
	if _, err := db.Query(internalCtx,
		`INSERT INTO revoked_tokens(jti, subject, issued_before, expires_at, reason)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(jti) DO NOTHING`,
		nullable(rev.jti), nullable(rev.subject), rev.issuedBefore, rev.expiresAt, reason,
	); err != nil {
		return fmt.Errorf("record the revocation: %w", err)
	}
	r.applyLocal(rev)
	return nil
}

// applyLocal applies a revocation this gateway recorded here immediately
// rather than waiting for the next refresh: the gateway that performed the
// revocation should not be the last to honour it.
//
// A reload whose read began before the write committed would replace the maps
// without it, so the revocation is also kept in local and re-applied over any
// reload that began no later than now.
func (r *RevocationList) applyLocal(rev revocation) {
	r.mu.Lock()
	r.apply(rev)
	r.local = append(r.local, localRevocation{rev: rev, at: r.now()})
	r.mu.Unlock()
}

// apply puts one revocation into the maps. The caller holds r.mu.
func (r *RevocationList) apply(rev revocation) {
	applyRevocation(r.byJTI, r.bySubject, rev)
}

// applyRevocation puts one revocation into byJTI and bySubject. A row whose
// jti is a scoped device key is RevokeDevice's dual row: it is that scoped
// entry and nothing else here, its legacy subject being for gateways before
// 0.3.13 (feat-978).
func applyRevocation(byJTI, bySubject map[string]int64, rev revocation) {
	subject := strings.ToLower(strings.TrimSpace(rev.subject))
	if strings.HasPrefix(rev.jti, deviceRevocationPrefix) {
		subject = rev.jti
	} else if rev.jti != "" {
		byJTI[rev.jti] = rev.expiresAt
	}
	if subject != "" {
		if existing, ok := bySubject[subject]; !ok || rev.issuedBefore > existing {
			bySubject[subject] = rev.issuedBefore
		}
	}
}

// nullable turns "" into nil so the column holds NULL rather than an empty
// string, which would match an empty jti or subject.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// reloadFlight is one reload in progress; done closes when it has finished,
// whether or not it succeeded.
type reloadFlight struct{ done chan struct{} }

// refreshIfStale reloads the list when the in-memory copy is older than the
// refresh interval, with at most one reload in flight, and reports whether the
// copy it leaves is fit to answer from.
//
// The request that finds the copy stale and no reload running starts one. A
// request whose copy is still younger than RevocationStaleness never waits: it
// is answered from the copy, which is what the bound permits, so a hung
// registry costs it nothing. Only a request whose copy is past the bound — or
// that has no copy — waits, for the reload in flight rather than starting
// another, because it must never be served from that copy. Without this, a
// registry slower than the interval made every request its own blocking
// full-table read.
//
// The wait is bounded by the read's own deadline, which Refresh enforces
// whatever the registry client does. A copy older than RevocationStaleness
// after that wait — the reload failed, or none was attempted because one failed
// a moment ago — is ErrRevocationsUnavailable: the state is unknown, and unknown
// is not "not revoked".
func (r *RevocationList) refreshIfStale() error {
	r.mu.Lock()
	now := r.now()
	if r.loaded && now.Sub(r.lastRefresh) < RevocationRefreshInterval {
		r.mu.Unlock()
		return nil
	}
	f := r.flight
	if f == nil && now.Sub(r.lastAttempt) >= revocationRetryInterval {
		f = &reloadFlight{done: make(chan struct{})}
		r.flight = f
		go r.reload(f)
	}
	mustWait := f != nil && (!r.loaded || now.Sub(r.lastRefresh) >= RevocationStaleness)
	r.mu.Unlock()

	if mustWait {
		<-f.done
	}
	return r.usable()
}

// usable is nil while the copy is younger than RevocationStaleness.
func (r *RevocationList) usable() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.loaded && r.now().Sub(r.lastRefresh) < RevocationStaleness {
		return nil
	}
	// Why the registry could not be read is in the log (failed). It is not
	// here: this text reaches clients, and the cause holds internal addresses.
	return fmt.Errorf("%w: the list is older than %s", ErrRevocationsUnavailable, RevocationStaleness)
}

// Usable reports whether the list can answer now, from the copy it holds,
// without starting a reload or waiting for one. The socket sweeper asks it once
// per pass to learn whether the registry is reachable before it makes a
// per-socket decision, so a pass over many sockets during a hang costs one
// question, not one wait per socket.
func (r *RevocationList) Usable() bool {
	return r != nil && r.usable() == nil
}

// reload runs one flight to completion and releases the waiters. Refresh logs a
// failure and records it, and every request then meets it through usable, so
// the error is not dropped here.
func (r *RevocationList) reload(f *reloadFlight) {
	defer func() {
		r.mu.Lock()
		r.flight = nil
		r.mu.Unlock()
		close(f.done)
	}()
	_ = r.Refresh(context.Background())
}

// revocationRead is what one read of the table returned.
type revocationRead struct {
	byJTI     map[string]int64
	bySubject map[string]int64
	err       error
}

// Refresh reloads the list from the database.
//
// A failed load leaves the previous list in place and does not age it: the
// copy stays as old as the read that filled it began, so a list that cannot be
// refreshed stops answering once it passes RevocationStaleness rather than being
// stamped fresh. It does not clear the list either: forgetting the revocations
// because one query failed would turn a database blip into every revoked token
// working again.
//
// The read runs in its own goroutine and is abandoned at the deadline, because
// the registry client may ignore its context; an abandoned read's result is
// discarded, never applied over a newer list.
func (r *RevocationList) Refresh(ctx context.Context) error {
	// The list is as old as the read that filled it began, not as old as the
	// moment the read returned.
	started := r.now()
	r.mu.Lock()
	r.readSeq++
	seq := r.readSeq
	r.lastAttempt = started
	r.mu.Unlock()

	db := r.database()
	if db == nil {
		return r.failed(errors.New("no registry database is configured on this gateway"))
	}
	r.mu.Lock()
	stuck := r.abandoned
	r.mu.Unlock()
	if stuck {
		return r.failed(fmt.Errorf("%w: a read of the registry that timed out has still not returned", ErrRevocationsUnavailable))
	}

	ctx, cancel := context.WithTimeout(ctx, r.reloadTimeout)
	defer cancel()
	result := make(chan revocationRead, 1)
	attempt := &readAttempt{}
	go func() {
		read := readRevocations(ctx, db, started)
		r.mu.Lock()
		attempt.finished = true
		if attempt.abandoned {
			r.abandoned = false
		}
		r.mu.Unlock()
		result <- read
	}()

	var read revocationRead
	select {
	case read = <-result:
	case <-ctx.Done():
		r.mu.Lock()
		if !attempt.finished {
			attempt.abandoned, r.abandoned = true, true
		}
		r.mu.Unlock()
		read.err = fmt.Errorf("the registry did not answer within %s: %w", r.reloadTimeout, ctx.Err())
	}
	if read.err != nil {
		return r.failed(read.err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if seq < r.appliedSeq {
		return nil // a newer read is already in the maps
	}
	r.appliedSeq = seq
	r.byJTI, r.bySubject = read.byJTI, read.bySubject
	r.lastRefresh = started
	r.loaded = true
	// A revocation recorded at or after this read began may not be in it.
	kept := r.local[:0]
	for _, l := range r.local {
		if !l.at.Before(started) {
			r.apply(l.rev)
			kept = append(kept, l)
		}
	}
	r.local = kept
	return nil
}

// readAttempt is one read goroutine's state, guarded by RevocationList.mu.
type readAttempt struct{ finished, abandoned bool }

// failed logs why a reload did not produce a list. Nothing about
// the copy changes, so it ages towards RevocationStaleness.
func (r *RevocationList) failed(err error) error {
	if r.logger != nil {
		r.logger.ComponentWarn(logging.ComponentGeneral,
			"could not reload the token revocations; requests are refused once the list is older than its bound",
			zap.Duration("bound", RevocationStaleness), zap.Error(err))
	}
	return fmt.Errorf("reload the token revocations: %w", err)
}

// readRevocations reads the rows whose tokens have not expired.
func readRevocations(ctx context.Context, db client.DatabaseClient, started time.Time) revocationRead {
	res, err := db.Query(client.WithInternalAuth(ctx),
		"SELECT jti, subject, issued_before, expires_at FROM revoked_tokens WHERE expires_at > ?", started.Unix())
	if err != nil {
		return revocationRead{err: err}
	}
	read := revocationRead{byJTI: map[string]int64{}, bySubject: map[string]int64{}}
	if res == nil {
		return read
	}
	for _, row := range res.Rows {
		if len(row) < 4 {
			continue
		}
		jti, _ := row[0].(string)
		subject, _ := row[1].(string)
		applyRevocation(read.byJTI, read.bySubject, revocation{
			jti:          jti,
			subject:      subject,
			issuedBefore: toInt64(row[2]),
			expiresAt:    toInt64(row[3]),
		})
	}
	return read
}

// Prune deletes rows whose tokens have all expired. Returns how many went.
func (r *RevocationList) Prune(ctx context.Context) error {
	db := r.database()
	if db == nil {
		return nil
	}
	internalCtx := client.WithInternalAuth(ctx)
	if _, err := db.Query(internalCtx, "DELETE FROM revoked_tokens WHERE expires_at <= ?", r.now().Unix()); err != nil {
		return fmt.Errorf("prune expired revocations: %w", err)
	}
	return nil
}

// StartPruning removes expired rows on a timer until ctx is done.
func (r *RevocationList) StartPruning(ctx context.Context) {
	if r == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(revocationPruneInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := r.Prune(ctx); err != nil && r.logger != nil {
					r.logger.ComponentWarn(logging.ComponentGeneral,
						"could not prune expired token revocations", zap.Error(err))
				}
			}
		}
	}()
}

func (r *RevocationList) database() client.DatabaseClient {
	if r == nil || r.registry == nil {
		return nil
	}
	return r.registry()
}

// RevokeSession refuses one access token from now on.
//
// Logging out dropped the refresh token and left the access token valid until
// it expired, so "log me out" meant "stop me getting a new token" rather than
// "stop this one working". A token minted before tokens carried a jti cannot be
// named, and says so.
func (s *Service) RevokeSession(ctx context.Context, claims *JWTClaims) error {
	if claims == nil {
		return fmt.Errorf("no token to revoke")
	}
	if claims.Jti == "" {
		return fmt.Errorf("this token was issued before tokens carried an id and cannot be revoked on its own; " +
			"log out of every session instead")
	}
	return s.revocations.RevokeToken(ctx, claims.Jti, claims.Exp, "session ended")
}

// RevokeAllSessions refuses every access token already issued to a subject.
func (s *Service) RevokeAllSessions(ctx context.Context, subject string) error {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return fmt.Errorf("no subject to revoke")
	}
	// Recorded under both names for the same reason the verifier looks under
	// both: a wallet subject is itself, a key subject is known by its hash.
	if err := s.revocations.RevokeSubject(ctx, subject, "all sessions ended", maxExchangedTokenLifetime); err != nil {
		return err
	}
	if hashed := s.HashAPIKey(subject); hashed != "" && hashed != subject {
		return s.revocations.RevokeSubject(ctx, hashed, "all sessions ended", maxExchangedTokenLifetime)
	}
	return nil
}

// Revocations exposes the list so the gateway can start its pruner and, in a
// test, drive a refresh.
func (s *Service) Revocations() *RevocationList { return s.revocations }

// Revoked reports whether a token, already verified, has been revoked since —
// by its jti, or under any name its subject may have been revoked by.
//
// ParseAndVerifyJWT asks this once, when a token is presented. An open
// WebSocket was authorized by a token presented once, at the upgrade, so the
// socket sweeper asks it again for as long as the socket stays open.
//
// The error is ErrRevocationsUnavailable when the list is too old to say.
func (s *Service) Revoked(claims *JWTClaims) (bool, error) {
	if claims == nil {
		return false, nil
	}
	return s.revocations.Denies(claims, s.revocationSubjectKeys(claims.Sub))
}

// RevocationsUsable reports whether the list can answer from the copy it holds,
// without reading the registry or waiting on it. See RevocationList.Usable.
func (s *Service) RevocationsUsable() bool { return s.revocations.Usable() }

// RefreshRevocations reloads the revocation list now rather than when it next
// goes stale. The socket sweeper calls it before each pass, so a pass applies
// every revocation recorded before it began.
//
// A failure is logged by the list and leaves it aging; Revoked is what reports
// it, once the list is too old to answer from.
func (s *Service) RefreshRevocations(ctx context.Context) {
	if s.revocations == nil {
		return
	}
	_ = s.revocations.Refresh(ctx)
}

// DeniesSubject reports whether a credential presented under any of these
// subjects has been revoked.
//
// Denies compares a token's issue time against the revocation, because a token
// minted after it is a new grant. A raw API key has no issue time to compare —
// the string either is the revoked credential or is not — so any live
// revocation of the subject denies.
//
// This is what closes the window the API-key cache opens: a key's namespace and
// grants are cached for a minute, so a revoked key kept working for up to that
// long. The list is replicated and reloaded every ten seconds, so it is the
// shorter of the two.
//
// The error is ErrRevocationsUnavailable, as for Denies.
func (r *RevocationList) DeniesSubject(subjects ...string) (bool, error) {
	if r == nil {
		return false, nilListError()
	}
	if err := r.refreshIfStale(); err != nil {
		return false, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, subject := range subjects {
		subject = strings.ToLower(strings.TrimSpace(subject))
		if subject == "" {
			continue
		}
		if _, denied := r.bySubject[subject]; denied {
			return true, nil
		}
	}
	return false, nil
}
