//go:build e2e_fleet

package realistic

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/Orama-Network/orama/e2e/harness/gw"
	"github.com/Orama-Network/orama/e2e/harness/wallet"
)

// User is a simulated end user of a tenant's app: a wallet admitted to the
// namespace, a device key its session is bound to, and the session, which it
// refreshes the way a client does (a device proof over the refresh token).
type User struct {
	Wallet *wallet.EVM
	Device *wallet.Device
	ns     string
	sub    string
	c      *gw.Client
	mu     sync.Mutex
	s      *gw.Session
}

// NewUsers admits count fresh wallets to tn's namespace with role (through
// `orama members add`, as the customer) and signs each in with an Ed25519
// device. Sign-ins are paced by the harness. Every grant is removed and every
// session ended at cleanup.
func NewUsers(t testing.TB, tn *Tenant, role string, count int) []*User {
	t.Helper()
	out := make([]*User, count)
	for i := range out {
		out[i] = newUser(t, tn, role)
	}
	return out
}

func newUser(t testing.TB, tn *Tenant, role string) *User {
	t.Helper()
	w, err := wallet.NewEVM()
	if err != nil {
		t.Fatal(err)
	}
	tn.N.CLI.MustOK(t, "members", "add", w.Address(), "--role", role, "--namespace", tn.N.Name)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
		defer cancel()
		if res, err := tn.N.CLI.Run(ctx, "members", "remove", w.Address(), "--namespace", tn.N.Name); err != nil || res.Exit != 0 {
			t.Errorf("cleanup: removing member %s: %v %s", w.Address(), err, res.Stderr)
		}
	})
	u := &User{Wallet: w, Device: gw.NewDevice(t, wallet.AlgEd25519), ns: tn.N.Name, c: gw.ForFleet(t, tn.F)}
	s, err := u.c.SignIn(t.Context(), w, tn.N.Name, u.Device)
	if err != nil {
		t.Fatalf("user %s could not sign in to %s: %v", w.Address(), tn.N.Name, err)
	}
	u.s, u.sub = s, s.Subject
	t.Cleanup(func() { u.logout(t) })
	return u
}

// Token is the user's current access token.
func (u *User) Token() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.s.AccessToken
}

// Subject is the wallet the gateway signed the user in as.
func (u *User) Subject() string { return u.sub }

// Refresh rotates the session with a device proof over the refresh token
// (docs/whitepaper/technical-reference/vol1/13-identity.md "Proving the device on later requests").
func (u *User) Refresh(ctx context.Context) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	proof, err := u.Device.NewProof(wallet.ProofRefresh, u.ns, u.s.RefreshToken)
	if err != nil {
		return err
	}
	next, _, err := u.c.Refresh(ctx, u.s.RefreshToken, u.ns, proof)
	if err != nil {
		return fmt.Errorf("refreshing %s's session: %w", u.Wallet.Address(), err)
	}
	if next.AccessToken == "" || next.RefreshToken == u.s.RefreshToken {
		return fmt.Errorf("refresh of %s returned no new session (refresh token rotated: %t)", u.Wallet.Address(), next.RefreshToken != u.s.RefreshToken)
	}
	u.s = next
	return nil
}

func (u *User) logout(t testing.TB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
	defer cancel()
	u.mu.Lock()
	s := u.s
	u.mu.Unlock()
	resp, err := u.c.LogoutEverywhere(ctx, s, u.ns, u.Device)
	if err != nil && (resp == nil || resp.Status != http.StatusUnauthorized) {
		t.Errorf("cleanup: logging %s out: %v", u.Wallet.Address(), err)
	}
}
