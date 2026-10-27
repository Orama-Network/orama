package gw

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Orama-Network/orama/e2e/harness/fleet"
	"github.com/Orama-Network/orama/e2e/harness/wallet"
)

// LobbyNamespace is where a challenge with no namespace signs in: it belongs to
// nobody, and its session reaches only POST /v1/namespaces.
const LobbyNamespace = "default"

// logoutBudget bounds the logout a user's cleanup performs.
const logoutBudget = 30 * time.Second

// refreshBudget bounds the refresh Token performs on a stale session.
const refreshBudget = 30 * time.Second

// refreshRetryAfter is how long Token waits after a failed refresh before
// trying again, so a refused refresh is not resent on every request.
const refreshRetryAfter = 30 * time.Second

// ForFleet returns the client for the run's public gateway, attributed to t.
func ForFleet(t testing.TB, f *fleet.Fleet) *Client {
	t.Helper()
	c, err := New(f.State.GatewayURL, f.State.CAFile, f.Recorder())
	if err != nil {
		t.Fatalf("failed to build the gateway client: %v", err)
	}
	return c.For(t)
}

// NamespaceURL is a namespace's own gateway, https://ns-<name>.<base domain>.
func NamespaceURL(st *fleet.State, namespace string) string {
	return "https://ns-" + namespace + "." + st.BaseDomain
}

// NamespacePinned is c aimed at namespace's own gateway and pinned to one
// node's public address ip (SNI and Host stay ns-<name>.<base domain>).
func (c *Client) NamespacePinned(st *fleet.State, namespace, ip string) *Client {
	return c.WithBase(NamespaceURL(st, namespace)).PinTo(ip)
}

// User is a signed-in throwaway wallet.
type User struct {
	Wallet    *wallet.EVM
	Device    *wallet.Device
	Session   *Session
	Namespace string
	// Client is the public gateway client, attributed to the test.
	Client *Client
	// mu serialises Token's refresh: a refresh rotates the refresh token, so
	// two concurrent refreshes would spend it twice.
	mu sync.Mutex
	// refreshFailedAt is when the last refresh failed, zero after a success.
	refreshFailedAt time.Time
}

// UserOption configures NewUser.
type UserOption func(*userConfig)

type userConfig struct {
	deviceAlg string
	unpaced   bool
}

// WithDevice binds the session to a fresh device key (wallet.AlgEd25519 or wallet.AlgES256).
func WithDevice(alg string) UserOption {
	return func(c *userConfig) { c.deviceAlg = alg }
}

// Unpaced makes the user's Client unpaced (see (*Client).Unpaced) once it has
// signed in; the sign-in itself stays paced. ONLY for rate-limiter tests.
func Unpaced() UserOption {
	return func(c *userConfig) { c.unpaced = true }
}

// NewUser creates a wallet and signs it in to namespace ("" or LobbyNamespace
// for the lobby). The namespace must already admit the wallet. A cleanup logs
// every session of the wallet out.
func NewUser(t testing.TB, f *fleet.Fleet, namespace string, opts ...UserOption) *User {
	t.Helper()
	var cfg userConfig
	for _, o := range opts {
		o(&cfg)
	}
	w, err := wallet.NewEVM()
	if err != nil {
		t.Fatal(err)
	}
	u := &User{Wallet: w, Namespace: namespace, Client: ForFleet(t, f)}
	if cfg.deviceAlg != "" {
		u.Device = NewDevice(t, cfg.deviceAlg)
	}
	chNamespace := namespace
	if chNamespace == LobbyNamespace {
		chNamespace = ""
	}
	u.Session, err = u.Client.SignIn(t.Context(), w, chNamespace, u.Device)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.unpaced {
		u.Client = u.Client.Unpaced()
	}
	t.Cleanup(func() { u.logoutAll(t) })
	return u
}

// NewDevice generates a device key of alg (wallet.AlgEd25519 or wallet.AlgES256).
func NewDevice(t testing.TB, alg string) *wallet.Device {
	t.Helper()
	var d *wallet.Device
	var err error
	switch alg {
	case wallet.AlgEd25519:
		d, err = wallet.NewEd25519Device()
	case wallet.AlgES256:
		d, err = wallet.NewES256Device()
	default:
		t.Fatalf("unknown device algorithm %q", alg)
	}
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (u *User) logoutAll(t testing.TB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), logoutBudget)
	defer cancel()
	u.mu.Lock()
	defer u.mu.Unlock()
	resp, err := u.Client.LogoutEverywhere(ctx, u.Session, u.Session.Namespace, u.Device)
	if err != nil && (resp == nil || resp.Status != 401) {
		// 401 means a test already ended the session, which is not a leak.
		t.Errorf("cleanup: failed to log %s out: %v", u.Wallet.Address(), err)
	}
}

// Token is the current access token. A session far enough into its lifetime is
// refreshed first, so a test that outlives one access token (a package that
// provisions namespaces one after another, a chaos test waiting out a fault)
// keeps a valid credential. A failed refresh is recorded in the run's
// evidence and retried after refreshRetryAfter, and the stale token is
// returned: the request it is used for then fails with AUTH_EXPIRED.
func (u *User) Token() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.Session.Stale(time.Now()) {
		u.refreshLocked()
	}
	return u.Session.AccessToken
}

func (u *User) refreshLocked() {
	now := time.Now()
	if !u.refreshFailedAt.IsZero() && now.Sub(u.refreshFailedAt) < refreshRetryAfter {
		return
	}
	if err := u.rotate(); err != nil {
		u.refreshFailedAt = now
		u.Client.note("refresh of "+u.Session.Subject+"'s session", err)
		return
	}
	u.refreshFailedAt = time.Time{}
}

// rotate refreshes the session in place. A refresh answers only the rotated
// credentials, so everything else the session carries (its API key, device,
// subject) is kept.
func (u *User) rotate() error {
	ctx, cancel := context.WithTimeout(context.Background(), refreshBudget)
	defer cancel()
	var proof *wallet.Proof
	if u.Device != nil {
		p, err := u.Device.NewProof(wallet.ProofRefresh, u.Session.Namespace, u.Session.RefreshToken)
		if err != nil {
			return fmt.Errorf("failed to sign the refresh proof: %w", err)
		}
		proof = p
	}
	next, _, err := u.Client.Refresh(ctx, u.Session.RefreshToken, u.Session.Namespace, proof)
	if err != nil {
		return err
	}
	if next.AccessToken == "" || next.RefreshToken == "" {
		return fmt.Errorf("the refresh answered no access or refresh token")
	}
	s := *u.Session
	s.AccessToken, s.RefreshToken, s.ExpiresIn, s.issuedAt = next.AccessToken, next.RefreshToken, next.ExpiresIn, next.issuedAt
	if next.TokenType != "" {
		s.TokenType = next.TokenType
	}
	u.Session = &s
	return nil
}
