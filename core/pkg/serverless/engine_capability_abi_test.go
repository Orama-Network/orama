package serverless

import (
	"context"
	"slices"
	"testing"

	"github.com/tetratelabs/wazero/api"
	"go.uber.org/zap"
)

// feat-264: the capability host calls are part of the host ABI, a contract
// with deployed WASM, so their signatures are pinned under every module name.
func TestEngine_HostModule_CapabilityCallsExported(t *testing.T) {
	engine, err := NewEngine(nil, NewMockRegistry(), NewMockHostServices(), zap.NewNop())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer func() { _ = engine.runtime.Close(context.Background()) }()

	i32, i64 := api.ValueTypeI32, api.ValueTypeI64
	want := map[string]struct{ params, results []api.ValueType }{
		// (resource ptr, resource len, ttl seconds) -> ptr<<32|len of the JSON, 0 on failure
		"capability_mint": {[]api.ValueType{i32, i32, i64}, []api.ValueType{i64}},
		// (id ptr, id len) -> 1 revoked, 0 failed
		"capability_revoke": {[]api.ValueType{i32, i32}, []api.ValueType{i32}},
		// () -> ptr<<32|len of the JSON, empty for a credential caller
		"get_caller_capability": {nil, []api.ValueType{i64}},
		// (cid ptr, cid len, count, ttl seconds) -> ptr<<32|len of the JSON, 0 on failure
		"storage_fetch_cap_mint": {[]api.ValueType{i32, i32, i32, i64}, []api.ValueType{i64}},
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

// bugboard #726: the WebRTC host calls are part of the host ABI too.
func TestEngine_HostModule_WebRTCCallsExported(t *testing.T) {
	engine, err := NewEngine(nil, NewMockRegistry(), NewMockHostServices(), zap.NewNop())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer func() { _ = engine.runtime.Close(context.Background()) }()

	i32, i64 := api.ValueTypeI32, api.ValueTypeI64
	want := map[string]struct{ params, results []api.ValueType }{
		// (room ptr, len, user ptr, len, device ptr, len, ttl seconds) -> ptr<<32|len of the JSON, 0 on failure
		"webrtc_admit": {[]api.ValueType{i32, i32, i32, i32, i32, i32, i64}, []api.ValueType{i64}},
		// webrtc_admit's arguments, then muted, video muted -> as webrtc_admit (feat-972)
		"webrtc_admit_with_mutes": {[]api.ValueType{i32, i32, i32, i32, i32, i32, i64, i32, i32}, []api.ValueType{i64}},
		// (room ptr, len, user ptr, len) -> 1 done, 0 failed
		"webrtc_kick": {[]api.ValueType{i32, i32, i32, i32}, []api.ValueType{i32}},
		// (room ptr, len, user ptr, len, device ptr, len) -> 1 done, 0 failed
		"webrtc_kick_device": {[]api.ValueType{i32, i32, i32, i32, i32, i32}, []api.ValueType{i32}},
		// (room ptr, len, user ptr, len, muted) -> 1 done, 0 failed
		"webrtc_mute":       {[]api.ValueType{i32, i32, i32, i32, i32}, []api.ValueType{i32}},
		"webrtc_mute_video": {[]api.ValueType{i32, i32, i32, i32, i32}, []api.ValueType{i32}},
		// (room ptr, len, user ptr, len, device ptr, len) -> ptr<<32|len of the JSON, 0 on failure
		"webrtc_admission": {[]api.ValueType{i32, i32, i32, i32, i32, i32}, []api.ValueType{i64}},
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
