// Package wssession holds an open WebSocket to the credential that opened it.
//
// A WebSocket is authorized once, at the upgrade. Everything after that used to
// be on trust: a socket opened with a fifteen-minute token kept serving for as
// long as the client kept it open, and revoking the token — or signing the
// session out — reached every new request and no open socket.
//
// Every socket opened with a token is registered here with that token's
// claims, and a sweeper re-asks the two questions the upgrade asked, on a
// timer, for as long as the socket is open: has the token expired, and has it
// been revoked. A socket that fails either is closed with a code that says
// which. A socket opened with a bare API key is registered with the key's
// registry id and closed once the key is revoked (bug-702).
package wssession

import (
	"context"
	"sync"
	"time"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"go.uber.org/zap"
)

const (
	// CloseExpired is the close code for a socket whose token expired without
	// being refreshed. Private-use range (4000-4999); "reconnect with a fresh
	// token".
	CloseExpired = 4401

	// CloseRevoked is the close code for a socket whose token, session or
	// subject was revoked. Reconnecting with the same credential will be
	// refused; the client has to sign in again.
	CloseRevoked = 4403

	// CloseDeviceRevoked is the close code for a token-opened socket whose
	// token was refused because the device it is bound to was revoked
	// (feat-976). A close frame's reason holds at most 123 bytes, and a
	// device's revocation with its proof does not fit, so the frame says only
	// that: the client reads the revocation from the 403 DEVICE_REVOKED any
	// request with the same token answers (GET /v1/auth/whoami, or the
	// upgrade again), or its next sign-in, and checks it before it acts.
	CloseDeviceRevoked = 4410

	// CloseUnavailable is the close code for a socket the gateway could not
	// check against the revocation list for longer than maxUncheckedSocketAge.
	// The session is not known to be revoked; reconnecting is right, and is
	// refused with a retryable 503 until the list can be read again.
	CloseUnavailable = 4503

	// ReasonExpired, ReasonRevoked and ReasonUnavailable are the close reasons
	// that go with the three codes.
	ReasonExpired     = "token expired; reconnect with a fresh token"
	ReasonRevoked     = "session revoked; sign in again"
	ReasonUnavailable = "cannot check this session right now; reconnect shortly"

	// ReasonDeviceRevoked is the CloseDeviceRevoked reason.
	ReasonDeviceRevoked = "device revoked; any request with this token answers DEVICE_REVOKED with its revocation"

	// ReasonKeyRevoked is the CloseRevoked reason of a socket opened with an
	// API key that was revoked.
	ReasonKeyRevoked = "API key revoked; reconnect with a valid credential"

	// maxUncheckedSocketAge is how long an open socket is kept while the
	// revocation list cannot be read, counted from its last successful check
	// (the upgrade, a token refresh, or a sweep). A request is refused as soon
	// as the list is older than auth.RevocationStaleness; closing every socket
	// on that same blip would turn a short registry hiccup into a reconnect
	// storm, so sockets get longer. Not unbounded: a session revoked while the
	// registry is unreachable must not stay open for as long as the outage
	// lasts. Two minutes is about twelve times the request bound, and past any
	// blip that heals on its own.
	maxUncheckedSocketAge = 2 * time.Minute

	// ExpiryGrace is how long past its token's exp a socket is kept open. It
	// covers clock skew between the gateway that minted the token and this one,
	// and the round trip of a client refreshing the token on the open socket.
	ExpiryGrace = 120 * time.Second

	// SweepInterval is how often every open socket is re-checked. The list is
	// reloaded at the start of every sweep, so a revocation recorded anywhere
	// in the cluster closes the sockets it covers within this interval.
	SweepInterval = auth.RevocationRefreshInterval

	// CloseFrameTimeout bounds the close frame sent to a socket being ended.
	// The connection is closed after it whether or not the client read it.
	CloseFrameTimeout = time.Second

	// sweepCloseConcurrency is how many sockets one sweep closes at a time. A
	// close can take up to CloseFrameTimeout against a client that stopped
	// reading, so closing one at a time would let a few such clients hold up
	// every other revocation on the gateway.
	sweepCloseConcurrency = 64
)

// Denier answers whether a token, verified when the socket opened, has been
// revoked since. *auth.Service is the one the gateway uses.
type Denier interface {
	// RefreshRevocations reloads the revocation list, so the sweep that
	// follows applies every revocation recorded before it.
	RefreshRevocations(ctx context.Context)
	// RevocationsUsable reports, without touching the registry or waiting on
	// it, whether the list can answer now. A sweep asks once, so a hung
	// registry costs it one question, not one wait per socket.
	RevocationsUsable() bool
	// Revoked returns an error when the list is too old to say; the sweep then
	// treats the socket as unchecked (see maxUncheckedSocketAge).
	Revoked(claims *auth.JWTClaims) (bool, error)
	// RevokedKey answers the same for a socket opened with a bare API key,
	// by the key's registry id.
	RevokedKey(id int64) (bool, error)
	// DeviceRevoked reports, for a token Revoked refused, whether it was
	// refused because its device was revoked. It reads the copy of the list
	// Revoked just checked.
	DeviceRevoked(claims *auth.JWTClaims) bool
}

// Gate re-judges an open socket against its namespace's device attestation
// policy (feat-941). A socket the attestation middleware judged at its upgrade
// is held to the policy for as long as it is open, so enforcing a policy, or
// raising its minimum build, reaches the sockets already open.
type Gate interface {
	// AttestationRefusal returns the wire code (ATTESTATION_REQUIRED, ...)
	// the namespace's policy now refuses the socket's token with, "" when it
	// admits it or does not enforce. An error means the policy cannot be read
	// now; the socket is left open until the next sweep.
	AttestationRefusal(namespace string, claims *auth.JWTClaims, now time.Time) (string, error)
}

// Closer ends one socket: a close frame carrying code and reason, then the
// connection. It must be safe to call from a goroutine other than the one
// reading the socket.
type Closer func(code int, reason string)

// Registry is every token-authorized socket open on this gateway.
type Registry struct {
	// gate is the attestation gate, nil when this gateway judges none.
	gate Gate

	mu      sync.Mutex
	next    uint64
	sockets map[uint64]*Socket
	logger  *zap.Logger
	// now is time.Now, replaced in tests.
	now func() time.Time
}

// NewRegistry builds an empty registry. logger records each socket the
// sweeper closes.
func NewRegistry(logger *zap.Logger) *Registry {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Registry{sockets: map[uint64]*Socket{}, logger: logger, now: time.Now}
}

// SetGate gives the registry the attestation gate its sweeps consult. Call it
// before the sweeper runs.
func (r *Registry) SetGate(g Gate) { r.gate = g }

// Register starts holding a socket to claims. A request with no token claims —
// an API key, or no credential on a public function — has no token to expire
// or revoke, and Register returns nil; every Socket method is safe on nil.
//
// The claims are copied: the caller's may be request-scoped.
func (r *Registry) Register(claims *auth.JWTClaims, closeFn Closer) *Socket {
	return r.register(claims, closeFn, false)
}

// RegisterCapability starts holding a socket opened with a capability to the
// capability's revocation claims (capability.Claims.RevocationClaims): it
// closes when the capability, or the device that issued it, is revoked, with
// CloseRevoked, since its holder is not that device.
func (r *Registry) RegisterCapability(claims *auth.JWTClaims, closeFn Closer) *Socket {
	return r.register(claims, closeFn, true)
}

// register builds the socket whole before a sweep can see it.
func (r *Registry) register(claims *auth.JWTClaims, closeFn Closer, capability bool) *Socket {
	if claims == nil || closeFn == nil {
		return nil
	}
	// The upgrade checked the token against the list, so the socket starts out
	// checked.
	s := &Socket{registry: r, claims: *claims, close: closeFn, checkedAt: r.now(), capability: capability}

	r.add(s)
	return s
}

// RegisterKey starts holding a socket opened with a bare API key to the key's
// revocation: revoking the key closes it at the next sweep, as revoking a
// token closes the sockets the token opened. keyID is the key's registry id
// (api_keys.id); a socket with none — no credential at all, on a public
// function — has nothing to revoke, and RegisterKey returns nil.
//
// The socket holds no account and no token: it cannot be refreshed, and has
// no expiry of its own.
func (r *Registry) RegisterKey(keyID int64, closeFn Closer) *Socket {
	if keyID <= 0 || closeFn == nil {
		return nil
	}
	s := &Socket{registry: r, keyID: keyID, close: closeFn, checkedAt: r.now()}
	r.add(s)
	return s
}

func (r *Registry) add(s *Socket) {
	r.mu.Lock()
	r.next++
	s.id = r.next
	r.sockets[s.id] = s
	r.mu.Unlock()
}

// Len is how many sockets are registered.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sockets)
}

// closeJob is one socket a sweep has decided to end, and why.
type closeJob struct {
	socket *Socket
	claims auth.JWTClaims
	code   int
	reason string
}

// Sweep closes every socket whose token expired more than ExpiryGrace ago, has
// been revoked, or has gone unchecked for longer than maxUncheckedSocketAge,
// and reports how many it closed.
//
// Expired sockets are closed first and without asking the list anything, so a
// registry that does not answer never delays them. The list is then probed
// once; if it cannot answer, no socket is checked against it this pass and each
// is held only until its last successful check is too old. The registry lock is
// held only to copy the set: a revocation check reads a shared list and a close
// writes to the network. Closes run in parallel, up to sweepCloseConcurrency at
// once, and the sweep waits for them.
func (r *Registry) Sweep(now time.Time, d Denier) int {
	r.mu.Lock()
	open := make([]*Socket, 0, len(r.sockets))
	for _, s := range r.sockets {
		open = append(open, s)
	}
	r.mu.Unlock()

	var expiredJobs []closeJob
	var live []*Socket
	for _, s := range open {
		if claims := s.Claims(); expired(claims.Exp, now) {
			expiredJobs = append(expiredJobs, closeJob{s, claims, CloseExpired, ReasonExpired})
		} else {
			live = append(live, s)
		}
	}
	closed := r.closeAll(expiredJobs)
	if len(live) == 0 {
		return closed
	}
	return closed + r.closeAll(r.judgeLive(live, now, d))
}

// judgeLive decides which sockets that have not expired must close: revoked
// ones, and ones the list could not vouch for within maxUncheckedSocketAge.
func (r *Registry) judgeLive(live []*Socket, now time.Time, d Denier) []closeJob {
	usable := d.RevocationsUsable()
	var (
		jobs       []closeJob
		unknown    int
		unknownErr error
	)
	for _, s := range live {
		claims := s.Claims()
		if usable {
			revoked, err := s.revoked(d, &claims)
			if err == nil {
				s.markChecked(now)
				if revoked {
					code, reason := s.revokedClose(d, &claims)
					jobs = append(jobs, closeJob{s, claims, code, reason})
				} else if code := r.attestationRefusal(s, &claims, now); code != "" {
					jobs = append(jobs, closeJob{s, claims, CloseRevoked, code})
				}
				continue
			}
			unknownErr = err
		}
		unknown++
		if now.Sub(s.lastChecked()) > maxUncheckedSocketAge {
			jobs = append(jobs, closeJob{s, claims, CloseUnavailable, ReasonUnavailable})
		}
	}
	if unknown > 0 {
		r.logger.Warn("could not tell whether open WebSockets were revoked; they are closed once unchecked for too long",
			zap.Int("sockets", unknown), zap.Duration("max_unchecked", maxUncheckedSocketAge), zap.Error(unknownErr))
	}
	return jobs
}

// attestationRefusal is the code the socket's namespace's attestation policy
// now refuses it with, "" when it admits it, judges no such socket, or cannot
// be read this pass.
func (r *Registry) attestationRefusal(s *Socket, claims *auth.JWTClaims, now time.Time) string {
	namespace := s.attestationNS()
	if r.gate == nil || namespace == "" {
		return ""
	}
	code, err := r.gate.AttestationRefusal(namespace, claims, now)
	if err != nil {
		r.logger.Warn("could not judge an open WebSocket against its namespace's attestation policy; it stays open this pass",
			zap.String("namespace", namespace), zap.Error(err))
		return ""
	}
	return code
}

// closeAll ends the sockets in parallel and reports how many this call closed.
func (r *Registry) closeAll(jobs []closeJob) int {
	var (
		wg     sync.WaitGroup
		slots  = make(chan struct{}, sweepCloseConcurrency)
		mu     sync.Mutex
		closed int
	)
	for _, j := range jobs {
		wg.Add(1)
		slots <- struct{}{}
		go func(j closeJob) {
			defer wg.Done()
			defer func() { <-slots }()
			if !j.socket.end(j.code, j.reason) {
				return
			}
			mu.Lock()
			closed++
			mu.Unlock()
			r.logger.Info("closed a WebSocket whose credential no longer authorizes it",
				zap.Int("code", j.code),
				zap.Int64("api_key_id", j.socket.keyID),
				zap.String("subject", auth.RedactSubject(j.claims.Sub)),
				zap.String("jti", j.claims.Jti),
				zap.Int64("exp", j.claims.Exp))
		}(j)
	}
	wg.Wait()
	return closed
}

// Run reloads the revocation list and sweeps, every interval, until ctx is
// done.
func (r *Registry) Run(ctx context.Context, d Denier, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			// An idle gateway holds nothing to check, and reloading the list
			// for it would be a registry query every interval on every
			// gateway in the cluster for nothing.
			if r.Len() == 0 {
				continue
			}
			// Bounded by the interval: a registry that does not answer must
			// not stop expired sockets being closed on this pass.
			refreshCtx, cancel := context.WithTimeout(ctx, interval)
			d.RefreshRevocations(refreshCtx)
			cancel()
			r.Sweep(now, d)
		}
	}
}

func (r *Registry) remove(id uint64) {
	r.mu.Lock()
	delete(r.sockets, id)
	r.mu.Unlock()
}

// expired reports whether a token expiring at exp (unix seconds) is past its
// grace at now. A token with no exp has nothing to enforce: the gateway never
// mints one, and a hop that could not say gets MaxTokenLifetime instead.
func expired(exp int64, now time.Time) bool {
	if exp <= 0 {
		return false
	}
	return now.After(time.Unix(exp, 0).Add(ExpiryGrace))
}

// Socket is one registered WebSocket.
type Socket struct {
	registry *Registry
	id       uint64
	close    Closer

	// keyID is the registry id of the API key a key-opened socket was opened
	// with, 0 for a socket opened with a token or a capability.
	keyID int64
	// capability is a socket opened with a capability: its claims are the
	// capability's, naming the device that issued it, not a device the
	// holder is.
	capability bool

	mu     sync.Mutex
	claims auth.JWTClaims
	ended  bool
	// attestationNamespace is the namespace whose attestation policy holds the
	// socket, "" when none does (see HoldToAttestation).
	attestationNamespace string
	// checkedAt is when the revocation list last vouched for this socket.
	checkedAt time.Time
}

// revoked asks d whether the credential that opened the socket was revoked:
// its key for a key-opened socket, its token otherwise.
func (s *Socket) revoked(d Denier, claims *auth.JWTClaims) (bool, error) {
	if s.keyID > 0 {
		return d.RevokedKey(s.keyID)
	}
	return d.Revoked(claims)
}

// HoldToAttestation holds the socket to namespace's attestation policy from
// here on: a sweep closes it with 4403 and the policy's code once the policy
// enforces and does not admit its token. Call it right after registering, for
// a socket whose upgrade the attestation middleware judged; it is safe on nil.
func (s *Socket) HoldToAttestation(namespace string) {
	if s == nil || namespace == "" {
		return
	}
	s.mu.Lock()
	s.attestationNamespace = namespace
	s.mu.Unlock()
}

func (s *Socket) attestationNS() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attestationNamespace
}

// revokedClose is the close code and reason of a socket whose credential was
// revoked. A token-opened socket whose device was revoked is told so
// (CloseDeviceRevoked); a capability's socket is not, because its holder is
// the correspondent the device handed it to, not the device.
func (s *Socket) revokedClose(d Denier, claims *auth.JWTClaims) (int, string) {
	switch {
	case s.keyID > 0:
		return CloseRevoked, ReasonKeyRevoked
	case !s.capability && d.DeviceRevoked(claims):
		return CloseDeviceRevoked, ReasonDeviceRevoked
	}
	return CloseRevoked, ReasonRevoked
}

func (s *Socket) markChecked(at time.Time) {
	s.mu.Lock()
	s.checkedAt = at
	s.mu.Unlock()
}

func (s *Socket) lastChecked() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkedAt
}

// Claims is a copy of the claims the socket is currently held to.
func (s *Socket) Claims() auth.JWTClaims {
	if s == nil {
		return auth.JWTClaims{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claims
}

// Expired reports whether the socket's token is past its grace at now. The
// persistent read loop asks this per frame so a frame arriving between sweeps
// is not served either.
func (s *Socket) Expired(now time.Time) bool {
	if s == nil {
		return false
	}
	return expired(s.Claims().Exp, now)
}

// HoldsAccount reports whether the socket was opened for an account — with a
// token — rather than with an API key, a capability, or no credential.
func (s *Socket) HoldsAccount() bool {
	return s != nil && s.Claims().Sub != ""
}

// CheckRefresh reports whether the socket may be held to claims from here on,
// without holding it to them. A caller that has more to change before the
// refresh takes effect asks this first and calls Refresh once the rest has
// succeeded.
//
// A token for a different subject is refused: a refresh keeps a socket open, it
// does not hand it to somebody else, and everything the socket was opened with
// — the function's ws_open, the identity the instance was bound to — was for
// the original one. A socket held to no account — one opened with a capability —
// cannot take one on.
func (s *Socket) CheckRefresh(claims *auth.JWTClaims) error {
	if s == nil {
		return ErrNotRefreshable
	}
	if claims == nil {
		return ErrNoClaims
	}
	if !s.HoldsAccount() {
		return ErrNotRefreshable
	}
	current := s.Claims()
	if claims.Sub != current.Sub {
		return ErrSubjectChanged
	}
	if claims.Did != current.Did {
		return ErrDeviceChanged
	}
	return nil
}

// Refresh holds the socket to a fresh token from here on, under the rules
// CheckRefresh states.
func (s *Socket) Refresh(claims *auth.JWTClaims) error {
	if err := s.CheckRefresh(claims); err != nil {
		return err
	}
	// The caller verified the token, which asked the list, so the socket is
	// checked as of now.
	s.mu.Lock()
	s.claims = *claims
	s.checkedAt = s.registry.now()
	s.mu.Unlock()
	return nil
}

// Unregister stops holding the socket. Call it when the socket's handler
// returns, however it returns.
func (s *Socket) Unregister() {
	if s == nil {
		return
	}
	s.registry.remove(s.id)
}

// end closes the socket once, however many sweeps find it, and reports
// whether this call was the one that closed it.
func (s *Socket) end(code int, reason string) bool {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return false
	}
	s.ended = true
	s.mu.Unlock()
	s.close(code, reason)
	return true
}
