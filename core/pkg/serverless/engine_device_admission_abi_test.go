package serverless

import (
	"context"
	"slices"
	"testing"

	"github.com/tetratelabs/wazero/api"
	"go.uber.org/zap"
)

// feat-973: the device-admission host calls are part of the host ABI, a
// contract with deployed WASM, so their signatures are pinned under every
// module name.
func TestEngine_HostModule_DeviceAdmissionCallsExported(t *testing.T) {
	engine, err := NewEngine(nil, NewMockRegistry(), NewMockHostServices(), zap.NewNop())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer func() { _ = engine.runtime.Close(context.Background()) }()

	i32, i64 := api.ValueTypeI32, api.ValueTypeI64
	want := map[string]struct{ params, results []api.ValueType }{
		// () -> ptr<<32|len of "pending", "active" or ""
		"get_caller_device_state": {nil, []api.ValueType{i64}},
		// (subject ptr, len, device ptr, len) -> 1 done, 0 failed
		"approve_device": {[]api.ValueType{i32, i32, i32, i32}, []api.ValueType{i32}},
		"deny_device":    {[]api.ValueType{i32, i32, i32, i32}, []api.ValueType{i32}},
	}
	for _, module := range []string{"env", "host", "orama"} {
		fns := engine.runtime.Module(module).ExportedFunctionDefinitions()
		for name, sig := range want {
			def, ok := fns[name]
			if !ok {
				t.Errorf("%s.%s is not exported", module, name)
				continue
			}
			if !slices.Equal(def.ParamTypes(), sig.params) || !slices.Equal(def.ResultTypes(), sig.results) {
				t.Errorf("%s.%s is (%v) -> %v, want (%v) -> %v",
					module, name, def.ParamTypes(), def.ResultTypes(), sig.params, sig.results)
			}
		}
	}
}
