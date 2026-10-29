package serverless

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// feat-971: a function deployed with invocable_by stores its parents on the
// version's row, and every read path hands them back.
func TestRegister_invocableByRoundTrips(t *testing.T) {
	r, _ := newVersionedRegistry(t)
	ctx := context.Background()
	for _, def := range []*FunctionDefinition{
		{Name: "rpc-router", Namespace: "ns"},
		{Name: "jobs", Namespace: "ns"},
		{Name: "send-message", Namespace: "ns", InvocableBy: []string{"rpc-router", "jobs"}},
	} {
		if _, err := r.Register(ctx, def, []byte("wasm")); err != nil {
			t.Fatalf("deploy %s: %v", def.Name, err)
		}
	}
	want := []string{"rpc-router", "jobs"}
	got, err := r.Get(ctx, "ns", "send-message", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.InvocableBy, want) {
		t.Errorf("Get read back %q, want %q", got.InvocableBy, want)
	}
	if byVersion, err := r.Get(ctx, "ns", "send-message", 1); err != nil || !reflect.DeepEqual(byVersion.InvocableBy, want) {
		t.Errorf("Get by version read back %v, %v", byVersion, err)
	}
	if byID, err := r.GetByID(ctx, got.ID); err != nil || !reflect.DeepEqual(byID.InvocableBy, want) {
		t.Errorf("GetByID read back %v, %v", byID, err)
	}
	list, err := r.List(ctx, "ns")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range list {
		switch fn.Name {
		case "send-message":
			if !reflect.DeepEqual(fn.InvocableBy, want) {
				t.Errorf("List read back %q", fn.InvocableBy)
			}
		default:
			if fn.InvocableBy != nil {
				t.Errorf("%s names no parent and read back %q", fn.Name, fn.InvocableBy)
			}
		}
	}

	// A redeploy without the field opens the function again.
	if _, err := r.Register(ctx, &FunctionDefinition{Name: "send-message", Namespace: "ns"}, []byte("wasm")); err != nil {
		t.Fatal(err)
	}
	if fn, err := r.Get(ctx, "ns", "send-message", 0); err != nil || fn.InvocableBy != nil {
		t.Errorf("a redeploy naming no parent read back %v, %v", fn, err)
	}
}

// A parent the namespace does not have, or has only deleted, is refused at
// deploy and the function is not deployed.
func TestRegister_refusesAParentTheNamespaceDoesNotHave(t *testing.T) {
	r, _ := newVersionedRegistry(t)
	ctx := context.Background()
	for _, def := range []*FunctionDefinition{
		{Name: "rpc-router", Namespace: "ns"},
		{Name: "gone", Namespace: "ns"},
		{Name: "elsewhere", Namespace: "other"},
	} {
		if _, err := r.Register(ctx, def, []byte("wasm")); err != nil {
			t.Fatalf("deploy %s: %v", def.Name, err)
		}
	}
	if err := r.Delete(ctx, "ns", "gone", 0); err != nil {
		t.Fatal(err)
	}
	if err := r.SetEnabled(ctx, "ns", "rpc-router", false); err != nil {
		t.Fatal(err)
	}
	for _, parent := range []string{"rpc-routr", "gone", "elsewhere"} {
		_, err := r.Register(ctx, &FunctionDefinition{Name: "send-message", Namespace: "ns", InvocableBy: []string{"rpc-router", parent}}, []byte("wasm"))
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Field != fieldInvocableBy || !strings.Contains(ve.Message, parent) {
			t.Errorf("parent %q: got %v, want a ValidationError naming it", parent, err)
		}
	}
	if _, err := r.Get(ctx, "ns", "send-message", 0); !IsNotFound(err) {
		t.Errorf("a refused deploy left a function: %v", err)
	}
	// A disabled parent is still a function of the namespace.
	if _, err := r.Register(ctx, &FunctionDefinition{Name: "send-message", Namespace: "ns", InvocableBy: []string{"rpc-router"}}, []byte("wasm")); err != nil {
		t.Errorf("a disabled parent was refused: %v", err)
	}
}

func TestValidateInvocableBy(t *testing.T) {
	many := make([]string, MaxInvocableBy+1)
	for i := range many {
		many[i] = "fn" + strings.Repeat("x", i)
	}
	refused := map[string]*FunctionDefinition{
		"not a name":    {Name: "child", InvocableBy: []string{"../router"}},
		"empty name":    {Name: "child", InvocableBy: []string{""}},
		"a comma":       {Name: "child", InvocableBy: []string{"a,b"}},
		"itself":        {Name: "child", InvocableBy: []string{"child"}},
		"twice":         {Name: "child", InvocableBy: []string{"router", "router"}},
		"too many":      {Name: "child", InvocableBy: many},
		"internal":      {Name: "child", IsInternal: true, InvocableBy: []string{"router"}},
		"ws_persistent": {Name: "child", WSPersistent: true, InvocableBy: []string{"router"}},
		"capability":    {Name: "child", WSAuth: WSAuthCapability, InvocableBy: []string{"router"}},
	}
	for name, def := range refused {
		var ve *ValidationError
		if err := validateInvocableBy(def); !errors.As(err, &ve) || ve.Field != fieldInvocableBy {
			t.Errorf("%s: got %v, want a ValidationError for invocable_by", name, err)
		}
	}
	for _, def := range []*FunctionDefinition{
		{Name: "child"},
		{Name: "child", IsInternal: true},
		{Name: "child", IsPublic: true, InvocableBy: []string{"router"}},
		{Name: "child", InvocableBy: many[:MaxInvocableBy]},
	} {
		if err := validateInvocableBy(def); err != nil {
			t.Errorf("%+v refused: %v", def, err)
		}
	}
}

// The invoker admits a function that declares invocable_by only to the
// parents it names, itself, the gateway and an admin; a client and any other
// function are refused by name, and a named parent's call is still the
// caller's.
func TestInvoke_invocableByAdmitsOnlyItsParents(t *testing.T) {
	child := &Function{ID: "fn-id", Namespace: "anchat", Name: "send-message", InvocableBy: []string{"rpc-router"}}
	inv := &Invoker{servedNamespace: "anchat", registry: &invokeMockRegistry{fn: child}, logger: zap.NewNop()}
	user := func(parent string) *InvokeRequest {
		return &InvokeRequest{Namespace: "anchat", FunctionName: "send-message",
			CallerWallet: "0xuser", CallerHasInvoke: true, ParentFunction: parent}
	}

	for name, req := range map[string]*InvokeRequest{
		"a client":               user(""),
		"another function":       user("search"),
		"a parent with no grant": {Namespace: "anchat", FunctionName: "send-message", ParentFunction: "rpc-router"},
	} {
		_, err := inv.Invoke(cancelledCtx(), req)
		if !errors.Is(err, ErrUnauthorized) {
			t.Errorf("%s got past authorization: %v", name, err)
		}
		if name != "a parent with no grant" && !errors.Is(err, ErrFunctionNotDirectlyInvocable) {
			t.Errorf("%s: %v, want ErrFunctionNotDirectlyInvocable", name, err)
		}
	}

	admin := user("")
	admin.CallerIsAdmin = true
	system := &InvokeRequest{Namespace: "anchat", FunctionName: "send-message", SystemOriginated: true}
	for name, req := range map[string]*InvokeRequest{
		"the parent": user("rpc-router"),
		"itself":     user("send-message"),
		"an admin":   admin,
		"a trigger":  system,
	} {
		if _, err := inv.Invoke(cancelledCtx(), req); errors.Is(err, ErrUnauthorized) {
			t.Errorf("%s was refused: %v", name, err)
		}
	}
}

// The WebSocket upgrade's check: a client is refused, an admin is not, and a
// function that names no parent admits every client.
func TestRefuseClientInvocation(t *testing.T) {
	child := &Function{Name: "send-message", InvocableBy: []string{"rpc-router"}}
	if err := RefuseClientInvocation(child, false); !errors.Is(err, ErrFunctionNotDirectlyInvocable) || !strings.Contains(err.Error(), "rpc-router") {
		t.Errorf("a client: %v", err)
	}
	if err := RefuseClientInvocation(child, true); err != nil {
		t.Errorf("an admin: %v", err)
	}
	if err := RefuseClientInvocation(&Function{Name: "open"}, false); err != nil {
		t.Errorf("a function naming no parent: %v", err)
	}
}
