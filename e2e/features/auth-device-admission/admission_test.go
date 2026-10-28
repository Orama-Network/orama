//go:build e2e_fleet

package authdeviceadmission

import (
	"net/http"
	"testing"

	"github.com/Orama-Network/orama/e2e/harness/eventually"
	"github.com/Orama-Network/orama/e2e/harness/gw"
	"github.com/Orama-Network/orama/e2e/harness/wallet"
)

// The function device policy end to end (feat-973): the account's first device
// signs in to a full session; a later one to a restricted session that reaches
// its own session and the functions open to pending devices, and nothing else;
// one of the namespace's functions approves it (its next refresh is a full
// session) or denies it (its session ends on every gateway); an account whose
// devices were all revoked recovers through the function too.
func TestFunctionPolicy_aFunctionAdmitsTheAccountsLaterDevices(t *testing.T) {
	fx := setup(t)
	phone := gw.NewDevice(t, wallet.AlgEd25519)
	first := signIn(t, fx.c, fx.user, fx.n.Name, phone)

	t.Run("the first device is active", func(t *testing.T) {
		if first.DeviceState != stateActive {
			t.Fatalf("the account's first device signed in %q, want active", first.DeviceState)
		}
		if got := whoCalled(t, fx, first.AccessToken); got.DeviceState != stateActive || got.DeviceID != phone.ID() {
			t.Fatalf("a function was told %+v about the first device", got)
		}
	})

	laptop := gw.NewDevice(t, wallet.AlgES256)
	second := signIn(t, fx.c, fx.user, fx.n.Name, laptop)
	if second.DeviceState != statePending || second.DeviceID != laptop.ID() {
		t.Fatalf("a later device signed in %q (%s), want pending", second.DeviceState, second.DeviceID)
	}

	t.Run("a pending session reaches nothing but itself and the open functions", func(t *testing.T) {
		refusedPending(t, fx.c.MustSend(t, gw.Req{Path: pathDevices, Bearer: second.AccessToken}), "list devices")
		refusedPending(t, fx.c.MustSend(t, gw.Req{Method: http.MethodDelete, Path: pathDevices + "/" + phone.ID(), Bearer: second.AccessToken}), "revoke the first device")
		refusedPending(t, fx.c.MustSend(t, gw.Req{Path: pathSessions, Bearer: second.AccessToken}), "list sessions")
		refusedPending(t, fx.c.MustSend(t, gw.Req{Path: pathPubsubWS, Bearer: second.AccessToken}), "pubsub socket")
		refusedPending(t, fx.c.MustSend(t, gw.Req{Method: http.MethodPost, Path: pathPublish, Bearer: second.AccessToken,
			Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"topic":"e2e","data_base64":""}`)}), "pubsub publish")
		refusedPending(t, call(t, fx, closedFn, second.AccessToken, nil), "a function not open to pending devices")

		got := whoCalled(t, fx, second.AccessToken)
		if got.DeviceState != statePending || got.DeviceID != laptop.ID() || got.Subject == "" {
			t.Fatalf("the open function was told %+v, want the pending laptop", got)
		}
		if resp := fx.c.MustSend(t, gw.Req{Path: gw.PathWhoami, Bearer: second.AccessToken}); resp.Status != http.StatusOK {
			t.Errorf("whoami for a pending session: %d %s", resp.Status, resp.Body)
		}
		next, resp := refresh(t, fx, second, laptop)
		if next == nil || next.DeviceState != statePending {
			t.Fatalf("a pending session's refresh before the approval: %d %s", resp.Status, resp.Body)
		}
		second = next
	})

	t.Run("the function approves it and its next refresh is a full session", func(t *testing.T) {
		subject := whoCalled(t, fx, first.AccessToken).Subject
		if !decide(t, fx, first.AccessToken, "approve", subject, laptop.ID()) {
			t.Fatal("approve_device refused the account's pending device")
		}
		full, resp := refresh(t, fx, second, laptop)
		if full == nil || full.DeviceState != stateActive {
			t.Fatalf("the refresh after the approval: %d %s, want device_state active", resp.Status, resp.Body)
		}
		fx.c.MustSend(t, gw.Req{Path: pathDevices, Bearer: full.AccessToken}).Expect(t, http.StatusOK)
		if got := whoCalled(t, fx, full.AccessToken); got.DeviceState != stateActive {
			t.Errorf("after the approval a function was told %+v", got)
		}
		refusedPending(t, call(t, fx, closedFn, second.AccessToken, nil), "the still-pending access token on a closed function")
	})

	t.Run("the function denies one and its session ends", func(t *testing.T) {
		tablet := gw.NewDevice(t, wallet.AlgEd25519)
		third := signIn(t, fx.c, fx.user, fx.n.Name, tablet)
		if third.DeviceState != statePending {
			t.Fatalf("a third device signed in %q, want pending", third.DeviceState)
		}
		if !decide(t, fx, first.AccessToken, "deny", third.Subject, tablet.ID()) {
			t.Fatal("deny_device refused the account's pending device")
		}
		eventually.Require(t, pollEvery, revocationBudget, "the denied device's token is refused", func() (bool, error) {
			return call(t, fx, admitFn, third.AccessToken, nil).Status == http.StatusUnauthorized, nil
		})
		if next, resp := refresh(t, fx, third, tablet); next != nil {
			t.Fatalf("a denied device refreshed its session: %d %s", resp.Status, resp.Body)
		}
		if decide(t, fx, first.AccessToken, "deny", third.Subject, phone.ID()) {
			t.Error("deny_device revoked an active device")
		}
	})

	t.Run("an account with every device revoked recovers through the function", func(t *testing.T) {
		for _, d := range []*wallet.Device{phone, laptop} {
			fx.c.MustSend(t, gw.Req{Method: http.MethodDelete, Path: pathNSDevices + "/" + d.ID(), Bearer: fx.admin.AccessToken}).Expect(t, http.StatusOK)
		}
		fresh := gw.NewDevice(t, wallet.AlgEd25519)
		s := signIn(t, fx.c, fx.user, fx.n.Name, fresh)
		if s.DeviceState != statePending {
			t.Fatalf("after losing every device the next one signed in %q; recovery must wait for the function", s.DeviceState)
		}
		// The application's recovery check passes, and its function approves
		// the caller's own device.
		got := whoCalled(t, fx, s.AccessToken)
		if !decide(t, fx, s.AccessToken, "approve", got.Subject, got.DeviceID) {
			t.Fatal("the recovering device was not approved")
		}
		full, resp := refresh(t, fx, s, fresh)
		if full == nil || full.DeviceState != stateActive {
			t.Fatalf("the recovered device's refresh: %d %s", resp.Status, resp.Body)
		}
	})
}
