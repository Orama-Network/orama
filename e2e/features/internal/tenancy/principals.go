//go:build e2e_fleet

package tenancy

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Orama-Network/orama/e2e/harness/fleet"
	"github.com/Orama-Network/orama/e2e/harness/gw"
	"github.com/Orama-Network/orama/e2e/harness/ns"
	"github.com/Orama-Network/orama/e2e/harness/wallet"
)

const (
	// PathMembers grants a role (docs/whitepaper/technical-reference/appendices/i-api-surface.md "Namespace management").
	PathMembers = "/v1/namespace/members"
	// PathKeys mints a scoped API key (docs/whitepaper/technical-reference/appendices/i-api-surface.md "Namespace management").
	PathKeys = "/v1/namespace/keys"
	// Roles (docs/whitepaper/technical-reference/appendices/d-cli-reference.md "orama members").
	RoleRuntime = "runtime"
	RoleReader  = "reader"
	RoleAdmin   = "admin"
	// RoleDeveloper holds db:write and not members or namespace.
	RoleDeveloper = "developer"
	// cleanupBudget bounds one cleanup request.
	cleanupBudget = time.Minute
)

// Member grants a fresh wallet role in n and signs it in to n. Its sessions
// are ended and its grant removed at cleanup.
func Member(t testing.TB, n *ns.Namespace, role string) *gw.User {
	t.Helper()
	u, _ := grant(t, n, role, "", nil)
	return u
}

// DeviceMember is Member with the session bound to a fresh Ed25519 device,
// whose key the returned user holds (gw.User.Device).
func DeviceMember(t testing.TB, n *ns.Namespace, role string) *gw.User {
	t.Helper()
	u, _ := grant(t, n, role, "", gw.NewDevice(t, wallet.AlgEd25519))
	return u
}

// Selector is a runtime member whose grant is narrowed to a resource, and
// whether the gateway said it enforces the narrowing. POST
// /v1/namespace/members answers "enforced": false while the data plane
// cannot apply a selector (core/pkg/gateway/members_routes.go).
type Selector struct {
	Token    string
	Enforced bool
}

// SelectorMember grants a runtime role narrowed to resource.
func SelectorMember(t testing.TB, n *ns.Namespace, resource string) Selector {
	t.Helper()
	u, enforced := grant(t, n, RoleRuntime, resource, nil)
	return Selector{Token: u.Token(), Enforced: enforced}
}

func grant(t testing.TB, n *ns.Namespace, role, resource string, device *wallet.Device) (*gw.User, bool) {
	t.Helper()
	w, err := wallet.NewEVM()
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"wallet": w.Address(), "role": role}
	if resource != "" {
		body["resource"] = resource
	}
	var granted struct {
		Enforced *bool `json:"enforced"`
	}
	if err := Post(t, n.Client, PathMembers, Owner(n), body).Expect(t, http.StatusCreated).Decode(&granted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeMember(t, n, w.Address()) })
	s, err := n.Owner.Client.For(t).SignIn(t.Context(), w, n.Name, device)
	if err != nil {
		t.Fatalf("a %s member could not sign in to %s: %v", role, n.Name, err)
	}
	u := &gw.User{Wallet: w, Device: device, Session: s, Namespace: n.Name, Client: n.Owner.Client}
	t.Cleanup(func() { logout(t, u) })
	return u, granted.Enforced == nil || *granted.Enforced
}

// removeMember takes addr's grant away; already gone is fine.
func removeMember(t testing.TB, n *ns.Namespace, addr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
	defer cancel()
	resp, err := n.Client.Send(ctx, gw.Req{Method: http.MethodDelete, Path: PathMembers + "/" + addr, Bearer: n.Owner.Token()})
	if err != nil {
		t.Errorf("cleanup: failed to remove member %s from %s: %v", addr, n.Name, err)
		return
	}
	if resp.Status != http.StatusOK && resp.Status != http.StatusNotFound {
		t.Errorf("cleanup: removing member %s from %s answered %d", addr, n.Name, resp.Status)
	}
}

// logout ends every session of u; a 401 means the test already ended it.
func logout(t testing.TB, u *gw.User) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
	defer cancel()
	s := u.Session
	resp, err := u.Client.Logout(ctx, s.AccessToken, s.RefreshToken, u.Namespace, true)
	if err != nil && (resp == nil || resp.Status != http.StatusUnauthorized) {
		t.Errorf("cleanup: failed to log %s out: %v", u.Wallet.Address(), err)
	}
}

// key is a minted API key.
type key struct {
	ID     int64  `json:"id"`
	APIKey string `json:"api_key"`
}

// APIKey mints a key with scope in n and revokes it at cleanup.
func APIKey(t testing.TB, n *ns.Namespace, scope string) string {
	t.Helper()
	return mintKey(t, n, scope).APIKey
}

// APIKeyDroppedWithNamespace mints a key with scope in n and leaves it to the
// namespace: a test that deletes n itself uses it, because a revoke at cleanup
// would then be sent to a gateway that no longer exists.
func APIKeyDroppedWithNamespace(t testing.TB, n *ns.Namespace, scope string) string {
	t.Helper()
	return newKey(t, n, scope).APIKey
}

// mintKey is APIKey returning the id too.
func mintKey(t testing.TB, n *ns.Namespace, scope string) key {
	t.Helper()
	k := newKey(t, n, scope)
	t.Cleanup(func() { revokeKey(t, n, k.ID) })
	return k
}

func newKey(t testing.TB, n *ns.Namespace, scope string) key {
	t.Helper()
	if n.Owner == nil {
		t.Fatalf("namespace %s has no HTTP owner session (created ViaOperator), so a key cannot be minted over HTTP; use the CLI", n.Name)
	}
	var k key
	resp := Post(t, n.Client, PathKeys, Owner(n), map[string]any{"scope": scope, "label": "e2e-" + scope})
	if err := resp.Expect(t, http.StatusCreated).Decode(&k); err != nil || k.APIKey == "" {
		t.Fatalf("minting a %q key returned no key: %v", scope, err)
	}
	return k
}

// revokeKey revokes id; already revoked (404) is fine.
func revokeKey(t testing.TB, n *ns.Namespace, id int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
	defer cancel()
	path := PathKeys + "/" + strconv.FormatInt(id, 10)
	r, err := n.Client.Send(ctx, gw.Req{Method: http.MethodDelete, Path: path, Bearer: n.Owner.Token()})
	if err != nil || (r.Status != http.StatusOK && r.Status != http.StatusNotFound) {
		t.Errorf("cleanup: failed to revoke key %d in %s: %v %v", id, n.Name, err, r)
	}
}

// OperatorMember gives a fresh wallet role in a namespace created with
// ns.ViaOperator (which has a signed-in CLI and no HTTP session), through
// `orama members add`, and signs it in, so a test can make HTTP calls in that
// namespace. The grant is removed and the sessions ended at cleanup.
func OperatorMember(t testing.TB, f *fleet.Fleet, n *ns.Namespace, role string) *gw.User {
	t.Helper()
	w, err := wallet.NewEVM()
	if err != nil {
		t.Fatal(err)
	}
	n.CLI.MustOK(t, "members", "add", w.Address(), "--role", role, "--namespace", n.Name)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
		defer cancel()
		if res, err := n.CLI.Run(ctx, "members", "remove", w.Address(), "--namespace", n.Name); err != nil || res.Exit != 0 {
			t.Errorf("cleanup: removing member %s from %s: %v %s", w.Address(), n.Name, err, res.Stderr)
		}
	})
	c := gw.ForFleet(t, f)
	s, err := c.SignIn(t.Context(), w, n.Name, nil)
	if err != nil {
		t.Fatalf("a %s member could not sign in to %s: %v", role, n.Name, err)
	}
	u := &gw.User{Wallet: w, Session: s, Namespace: n.Name, Client: c}
	t.Cleanup(func() { logout(t, u) })
	return u
}
