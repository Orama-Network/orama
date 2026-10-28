package auth

import (
	"context"
	"net/http"
	"testing"

	authsvc "github.com/Orama-Network/orama/core/pkg/gateway/auth"
)

// The function device policy (feat-973) through the handlers, against the
// real schema: the first device signs in to a full session, a later one to a
// restricted one that refreshes as pending until a function approves it.

func functionPolicyFlow(t *testing.T) (*flow, wallet) {
	t.Helper()
	f := newFlow(t)
	w := newWallet(t)
	f.member(w.address, authsvc.RoleRuntime)
	if _, err := f.svc.SetDevicePolicy(context.Background(), flowNamespace, authsvc.DevicePolicyFunction, "0xowner"); err != nil {
		t.Fatalf("policy: %v", err)
	}
	return f, w
}

func TestVerifyHandler_functionPolicyGivesALaterDeviceARestrictedSession(t *testing.T) {
	f, w := functionPolicyFlow(t)
	phone, laptop := newTestDeviceKey(t), newTestDeviceKey(t)

	code, first := f.signIn(w, phone)
	if code != http.StatusOK || first["device_state"] != authsvc.CallerDeviceActive {
		t.Fatalf("the account's first device: %d %v", code, first)
	}
	if f.claimsOf(first).PendingDevice() {
		t.Fatal("the first device's token is pending")
	}

	code, second := f.signIn(w, laptop)
	if code != http.StatusOK || second["device_state"] != authsvc.CallerDevicePending || second["device_id"] != laptop.id {
		t.Fatalf("a later device: %d %v, want a session with device_state pending", code, second)
	}
	claims := f.claimsOf(second)
	if !claims.PendingDevice() || claims.Did != laptop.id {
		t.Fatalf("a later device's token is %+v, want the pending audience", claims)
	}

	refresh := func() (int, map[string]any) {
		rt := second["refresh_token"].(string)
		code, body := f.do(f.h.RefreshHandler, http.MethodPost, "/v1/auth/refresh", map[string]any{
			"refresh_token": rt, "namespace": flowNamespace, "device_proof": proof(laptop, authsvc.DeviceProofRefresh, rt),
		}, nil)
		if code == http.StatusOK {
			second["refresh_token"] = body["refresh_token"]
		}
		return code, body
	}
	if code, body := refresh(); code != http.StatusOK || body["device_state"] != authsvc.CallerDevicePending {
		t.Fatalf("refresh before the approval: %d %v", code, body)
	}
	if err := f.svc.ApprovePendingDevice(context.Background(), flowNamespace, w.address, laptop.id, "function:device-join"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	code, body := refresh()
	if code != http.StatusOK || body["device_state"] != authsvc.CallerDeviceActive || f.claimsOf(body).PendingDevice() {
		t.Fatalf("refresh after the approval: %d %v, want a full session", code, body)
	}
}

// A pending session has no say over the account's other sessions or devices,
// and is told so by the code a client acts on (the route policy refuses it
// first; this is the handlers' own refusal).
func TestPendingSession_actsOnNothingButItself(t *testing.T) {
	f, w := functionPolicyFlow(t)
	phone, laptop := newTestDeviceKey(t), newTestDeviceKey(t)
	f.signIn(w, phone)
	_, second := f.signIn(w, laptop)
	pending := f.claimsOf(second)

	for name, c := range map[string]struct {
		handler      http.HandlerFunc
		method, path string
		body         any
	}{
		"list sessions":  {f.h.SessionsHandler, http.MethodGet, "/v1/auth/sessions", nil},
		"end a session":  {f.h.SessionByIDHandler, http.MethodDelete, "/v1/auth/sessions/1", nil},
		"list devices":   {f.h.DevicesHandler, http.MethodGet, "/v1/auth/devices", nil},
		"revoke device":  {f.h.DeviceByIDHandler, http.MethodDelete, "/v1/auth/devices/" + phone.id, nil},
		"approve a link": {f.h.DeviceByIDHandler, http.MethodPost, "/v1/auth/devices/approve", map[string]any{"user_code": "ABCD-EFGH"}},
		"logout all": {f.h.LogoutHandler, http.MethodPost, "/v1/auth/logout",
			map[string]any{"all": true, "namespace": flowNamespace, "device_proof": proof(laptop, authsvc.DeviceProofLogoutAll, laptop.id)}},
	} {
		code, body := f.do(c.handler, c.method, c.path, c.body, pending)
		if code != http.StatusForbidden || body["code"] != ErrCodeDevicePendingApproval {
			t.Errorf("%s: %d %v, want 403 %s", name, code, body, ErrCodeDevicePendingApproval)
		}
	}
	if d, err := f.svc.Device(context.Background(), flowNamespace, phone.id); err != nil || d.State != authsvc.DeviceStateActive {
		t.Errorf("the first device after the pending session's attempts: %+v, %v", d, err)
	}

	// Its own logout is its own.
	if code, body := f.do(f.h.LogoutHandler, http.MethodPost, "/v1/auth/logout",
		map[string]any{"refresh_token": second["refresh_token"], "namespace": flowNamespace}, pending); code != http.StatusOK {
		t.Errorf("a pending session could not end itself: %d %v", code, body)
	}
}

// whoami tells a pending device it is pending, and that it holds no grant.
func TestWhoamiHandler_aPendingSessionHoldsNoGrant(t *testing.T) {
	f, w := functionPolicyFlow(t)
	f.signIn(w, newTestDeviceKey(t))
	laptop := newTestDeviceKey(t)
	_, second := f.signIn(w, laptop)

	code, body := f.do(f.h.WhoamiHandler, http.MethodGet, "/v1/auth/whoami", nil, f.claimsOf(second))
	if code != http.StatusOK || body["device_state"] != authsvc.CallerDevicePending || body["device_id"] != laptop.id {
		t.Fatalf("whoami: %d %v", code, body)
	}
	if grants, _ := body["grants"].([]any); len(grants) != 0 {
		t.Errorf("a pending session reports grants %v", body["grants"])
	}
}

// A token bound to no device, minted before the namespace required devices,
// does not reach the devices the policy now protects for the rest of its life.
func TestSessionOwner_anUnboundTokenIsRefusedOnceDevicesAreRequired(t *testing.T) {
	f := newFlow(t)
	w := newWallet(t)
	f.member(w.address, authsvc.RoleRuntime)
	phone := newTestDeviceKey(t)
	f.signIn(w, phone)
	_, plain := f.signIn(w, nil)
	unbound := f.claimsOf(plain)

	if code, body := f.do(f.h.DevicesHandler, http.MethodGet, "/v1/auth/devices", nil, unbound); code != http.StatusOK {
		t.Fatalf("under optional an unbound session lists the account's devices: %d %v", code, body)
	}
	for _, policy := range []authsvc.DevicePolicy{authsvc.DevicePolicyRequired, authsvc.DevicePolicyApproval, authsvc.DevicePolicyFunction} {
		if _, err := f.svc.SetDevicePolicy(context.Background(), flowNamespace, policy, "0xowner"); err != nil {
			t.Fatal(err)
		}
		if code, body := f.do(f.h.DeviceByIDHandler, http.MethodDelete, "/v1/auth/devices/"+phone.id, nil, unbound); code != http.StatusForbidden || body["code"] != ErrCodeDeviceRequired {
			t.Errorf("%s: an unbound token revoking a device: %d %v, want 403 %s", policy, code, body, ErrCodeDeviceRequired)
		}
		if code, body := f.do(f.h.SessionsHandler, http.MethodGet, "/v1/auth/sessions", nil, unbound); code != http.StatusForbidden {
			t.Errorf("%s: an unbound token listing sessions: %d %v", policy, code, body)
		}
	}
	if d, err := f.svc.Device(context.Background(), flowNamespace, phone.id); err != nil || d.State != authsvc.DeviceStateActive {
		t.Errorf("the device after the unbound token's attempts: %+v, %v", d, err)
	}
}
