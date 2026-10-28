package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	authsvc "github.com/Orama-Network/orama/core/pkg/gateway/auth"
)

// Under the function policy the namespace's functions admit its users'
// devices, so a device link, which another device approves, is refused at
// every step (feat-973): it cannot start, a link started under another policy
// cannot be approved or collected after the change, and the device it would
// have let in stays pending.
func TestDeviceLink_refusedUnderTheFunctionPolicy(t *testing.T) {
	f := newFlow(t)
	f.owner("0xowner")
	if code, body := f.setPolicy(map[string]any{"sign_in": "open", "device_policy": "approval"}); code != http.StatusOK {
		t.Fatalf("policy: %d %v", code, body)
	}
	w := newWallet(t)
	phone, laptop, tablet := newTestDeviceKey(t), newTestDeviceKey(t), newTestDeviceKey(t)
	code, first := f.signIn(w, phone)
	if code != http.StatusOK {
		t.Fatalf("the first device: %d %v", code, first)
	}
	link := func(d *testDeviceKey) (string, string) {
		code, pending := f.signIn(w, d)
		if code != http.StatusAccepted {
			t.Fatalf("a later device under approval: %d %v", code, pending)
		}
		return pending["user_code"].(string), pending["device_code"].(string)
	}
	approve := func(userCode string) (int, map[string]any) {
		return f.do(f.h.DeviceByIDHandler, http.MethodPost, "/v1/auth/devices/approve",
			map[string]any{"user_code": userCode, "device_proof": proof(phone, authsvc.DeviceProofApprove, userCode)},
			f.claimsOf(first))
	}
	laptopUser, laptopDevice := link(laptop)
	if code, body := approve(laptopUser); code != http.StatusOK {
		t.Fatalf("approve under approval: %d %v", code, body)
	}
	tabletUser, _ := link(tablet)

	if code, body := f.setPolicy(map[string]any{"device_policy": "function"}); code != http.StatusOK {
		t.Fatalf("function policy: %d %v", code, body)
	}
	refused := func(what string, code int, body map[string]any) {
		t.Helper()
		if code != http.StatusForbidden || body["code"] != ErrCodeDeviceLinkFunctionPolicy {
			t.Errorf("%s under the function policy: %d %v", what, code, body)
		}
	}

	code, body := f.do(f.h.DeviceAuthorizationHandler, http.MethodPost, "/v1/auth/device",
		map[string]any{"namespace": flowNamespace, "device_key": json.RawMessage(newTestDeviceKey(t).jwk)}, nil)
	refused("a seedless start", code, body)

	code, body = approve(tabletUser)
	refused("approving a link started before", code, body)

	code, body = f.do(f.h.DeviceTokenHandler, http.MethodPost, "/v1/auth/device/token",
		map[string]any{"device_code": laptopDevice, "device_proof": proof(laptop, authsvc.DeviceProofClaim, laptopDevice)}, nil)
	refused("collecting a link approved before", code, body)

	for _, d := range []*testDeviceKey{laptop, tablet} {
		if got, err := f.svc.Device(context.Background(), flowNamespace, d.id); err != nil || got.State != authsvc.DeviceStatePending {
			t.Errorf("a refused link's device: %+v, %v; want it still pending", got, err)
		}
	}
}
