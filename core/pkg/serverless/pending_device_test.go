package serverless

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"
)

// feat-973: a pending device's restricted session runs only the functions the
// namespace opened to pending devices, whichever way the function is reached.

func TestPendingDeviceMayInvoke(t *testing.T) {
	closed := &Function{Name: "send-message"}
	open := &Function{Name: "device-join", OpenToPendingDevices: true}
	for _, tc := range []struct {
		fn      *Function
		state   string
		refused bool
	}{
		{closed, CallerDevicePending, true},
		{open, CallerDevicePending, false},
		{closed, CallerDeviceActive, false},
		{closed, "", false},
	} {
		err := PendingDeviceMayInvoke(tc.fn, tc.state)
		if got := IsDevicePendingApproval(err); got != tc.refused {
			t.Errorf("%s for %q: refused = %v (%v), want %v", tc.fn.Name, tc.state, got, err, tc.refused)
		}
	}
}

// The check is the invoker's, after authorization, so it covers HTTP, socket
// frames and a nested function_invoke alike — the nested call carries the
// caller's device state from its parent (hostfunctions/context.go).
//
// It composes with invocable_by (feat-971) as two separate rules, each judged
// on every hop: invocable_by says which functions may call the child,
// open_to_pending_devices says whether a pending session may run it at all. A
// pending device calling an open router that forwards to a handler reaches the
// handler only when the handler is open to pending devices too, so a router
// cannot launder a pending session into a function the namespace kept from
// it; and a handler open to pending devices but invocable only by the router
// is still refused to the pending device directly.
func TestInvoke_aPendingDeviceIsJudgedOnEveryHop(t *testing.T) {
	pending := func(fn, parent string) *InvokeRequest {
		return &InvokeRequest{Namespace: "anchat", FunctionName: fn, CallerWallet: "0xuser", CallerHasInvoke: true,
			CallerDeviceID: "device-2", CallerDeviceState: CallerDevicePending, ParentFunction: parent}
	}
	invoker := func(fn *Function) *Invoker {
		return &Invoker{servedNamespace: "anchat", registry: &invokeMockRegistry{fn: fn}, logger: zap.NewNop()}
	}

	closedHandler := &Function{ID: "h1", Namespace: "anchat", Name: "send-message", IsPublic: true, InvocableBy: []string{"rpc-router"}}
	if _, err := invoker(closedHandler).Invoke(cancelledCtx(), pending("send-message", "rpc-router")); !IsDevicePendingApproval(err) {
		t.Errorf("an open router forwarded a pending session to a closed handler: %v", err)
	}

	openHandler := &Function{ID: "h2", Namespace: "anchat", Name: "device-join-step", IsPublic: true,
		InvocableBy: []string{"rpc-router"}, OpenToPendingDevices: true}
	if _, err := invoker(openHandler).Invoke(cancelledCtx(), pending("device-join-step", "rpc-router")); IsDevicePendingApproval(err) || errors.Is(err, ErrUnauthorized) {
		t.Errorf("the router's call to an open handler was refused: %v", err)
	}
	if _, err := invoker(openHandler).Invoke(cancelledCtx(), pending("device-join-step", "")); !errors.Is(err, ErrFunctionNotDirectlyInvocable) {
		t.Errorf("a pending device reached a router-only handler directly: %v", err)
	}

	openRouter := &Function{ID: "r", Namespace: "anchat", Name: "rpc-router", IsPublic: true, OpenToPendingDevices: true}
	if _, err := invoker(openRouter).Invoke(cancelledCtx(), pending("rpc-router", "")); IsDevicePendingApproval(err) || errors.Is(err, ErrUnauthorized) {
		t.Errorf("a pending device was refused the open router: %v", err)
	}

	active := pending("send-message", "rpc-router")
	active.CallerDeviceState = CallerDeviceActive
	if _, err := invoker(closedHandler).Invoke(cancelledCtx(), active); IsDevicePendingApproval(err) || errors.Is(err, ErrUnauthorized) {
		t.Errorf("an active device was refused through the router: %v", err)
	}
}

// A function deployed open to pending devices stores it on the version's row,
// and every read path hands it back; a redeploy without it closes it again.
func TestRegister_openToPendingDevicesRoundTrips(t *testing.T) {
	r, _ := newVersionedRegistry(t)
	ctx := context.Background()
	if _, err := r.Register(ctx, &FunctionDefinition{Name: "device-join", Namespace: "ns", OpenToPendingDevices: true}, []byte("wasm")); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, "ns", "device-join", 0)
	if err != nil || !got.OpenToPendingDevices {
		t.Fatalf("Get read back %+v, %v", got, err)
	}
	if byID, err := r.GetByID(ctx, got.ID); err != nil || !byID.OpenToPendingDevices {
		t.Errorf("GetByID read back %+v, %v", byID, err)
	}
	if list, err := r.List(ctx, "ns"); err != nil || len(list) != 1 || !list[0].OpenToPendingDevices {
		t.Errorf("List read back %+v, %v", list, err)
	}
	if _, err := r.Register(ctx, &FunctionDefinition{Name: "device-join", Namespace: "ns"}, []byte("wasm")); err != nil {
		t.Fatal(err)
	}
	if fn, err := r.Get(ctx, "ns", "device-join", 0); err != nil || fn.OpenToPendingDevices {
		t.Errorf("a redeploy without the field read back %+v, %v", fn, err)
	}
}
