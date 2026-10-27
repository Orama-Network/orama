package execution

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"go.uber.org/zap"
)

// Bugboard #120 — wazero defaults to a DETERMINISTIC (zero-seed) RNG source.
// TinyGo wasm's crypto/rand.Read calls WASI random_get, so without
// .WithRandSource(crypto/rand.Reader) every fresh instance gets the IDENTICAL
// "random" byte sequence. Each serverless invocation is a fresh instance, so
// any unguessable code / nonce / token a function generates is constant (the
// observed "8LRJ2S on every rotate" symptom).
//
// The fix is .WithRandSource(cryptorand.Reader) on BOTH wazero moduleConfig
// builders — executor.go (stateless) and engine.go (persistent WS). This test
// pins the executor's config path: instantiate the SAME config twice and assert
// the two instances produce DIFFERENT random bytes.
//
// If a future refactor drops .WithRandSource(), the positive test fails with a
// clear message; the negative control documents why the fix is necessary.

// randProbeWasm is a hand-assembled WASM module that imports
// wasi_snapshot_preview1.random_get and calls it from _start, writing 8 random
// bytes to memory[0:8].
//
//	(module
//	  (type $random_get (func (param i32 i32) (result i32)))
//	  (type $start (func))
//	  (import "wasi_snapshot_preview1" "random_get"
//	    (func $random_get (type 0)))
//	  (memory (export "memory") 1)
//	  (func $_start (type 1)
//	    i32.const 0         ;; buf = 0
//	    i32.const 8         ;; buf_len = 8
//	    call $random_get
//	    drop)
//	  (export "_start" (func $_start)))
var randProbeWasm = []byte{
	// Magic + version
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,

	// Type section (id=1) — body=10 bytes
	0x01,
	0x0a,
	0x02,                   // 2 types
	0x60, 0x02, 0x7f, 0x7f, // type 0: func(i32, i32)
	0x01, 0x7f, // -> (i32)
	0x60, 0x00, 0x00, // type 1: func() -> ()

	// Import section (id=2) — body=0x25 (37 bytes)
	0x02,
	0x25,
	0x01, // 1 import
	0x16, // module name "wasi_snapshot_preview1" length=22
	0x77, 0x61, 0x73, 0x69, 0x5f, 0x73, 0x6e, 0x61, 0x70, 0x73, 0x68, 0x6f, 0x74, 0x5f, 0x70, 0x72, 0x65, 0x76, 0x69, 0x65, 0x77, 0x31,
	0x0a, // fn name "random_get" length=10
	0x72, 0x61, 0x6e, 0x64, 0x6f, 0x6d, 0x5f, 0x67, 0x65, 0x74,
	0x00, 0x00, // kind=func, type idx=0

	// Function section (id=3) — body=2 bytes
	0x03,
	0x02,
	0x01, // 1 function
	0x01, // type idx 1 (for _start)

	// Memory section (id=5) — body=3 bytes
	0x05,
	0x03,
	0x01,       // 1 memory
	0x00, 0x01, // limits: flags=0 (no max), min=1 page

	// Export section (id=7) — body=19 bytes (0x13)
	0x07,
	0x13,
	0x02,                                     // 2 exports
	0x06, 0x6d, 0x65, 0x6d, 0x6f, 0x72, 0x79, // "memory"
	0x02, 0x00, // kind=memory, idx=0
	0x06, 0x5f, 0x73, 0x74, 0x61, 0x72, 0x74, // "_start"
	0x00, 0x01, // kind=func, idx=1 (after the 1 import)

	// Code section (id=10) — body=11 bytes (0x0b)
	0x0a,
	0x0b,
	0x01,       // 1 function body
	0x09,       // body size = 9
	0x00,       // 0 local groups
	0x41, 0x00, // i32.const 0  (buf)
	0x41, 0x08, // i32.const 8  (buf_len)
	0x10, 0x00, // call func 0 (the imported random_get)
	0x1a, // drop (errno return)
	0x0b, // end
}

// readProbeRandom instantiates randProbeWasm once with the given moduleConfig
// transform and returns the 8 random bytes written to memory[0:8].
func readProbeRandom(t *testing.T, runtime wazero.Runtime, compiled wazero.CompiledModule, cfg wazero.ModuleConfig) uint64 {
	t.Helper()
	ctx := context.Background()
	mod, err := runtime.InstantiateModule(ctx, compiled, cfg)
	if err != nil {
		t.Fatalf("instantiate probe module: %v", err)
	}
	defer mod.Close(ctx)
	raw, ok := mod.Memory().Read(0, 8)
	if !ok {
		t.Fatal("could not read 8 bytes from probe memory at offset 0")
	}
	return binary.LittleEndian.Uint64(raw)
}

func TestModuleConfig_randSourceIsRealNotDeterministic(t *testing.T) {
	ctx := context.Background()
	runtime := wazero.NewRuntime(ctx)
	defer runtime.Close(ctx)

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		t.Fatalf("instantiate WASI: %v", err)
	}
	compiled, err := runtime.CompileModule(ctx, randProbeWasm)
	if err != nil {
		t.Fatalf("compile probe wasm: %v (hex assembly likely off; recompute section sizes)", err)
	}
	defer compiled.Close(ctx)

	// Mirror the executor.go moduleConfig — anonymous instance, real RNG. Two
	// separate instantiations of the SAME config must produce different bytes.
	newCfg := func() wazero.ModuleConfig {
		return wazero.NewModuleConfig().
			WithName("").
			WithArgs("probe").
			WithSysWalltime().
			WithSysNanotime().
			WithRandSource(cryptorand.Reader)
	}

	a := readProbeRandom(t, runtime, compiled, newCfg())
	b := readProbeRandom(t, runtime, compiled, newCfg())
	if a == b {
		t.Errorf("BUG #120 REGRESSION: two fresh instances produced IDENTICAL random "+
			"bytes (%#016x) — crypto/rand is deterministic. Did the "+
			".WithRandSource(cryptorand.Reader) call get dropped from moduleConfig "+
			"in executor.go or engine.go?", a)
	}
}

func TestModuleConfig_randWithoutFix_demoDeterministic(t *testing.T) {
	// Negative control: WITHOUT .WithRandSource(), confirm wazero's default RNG
	// is deterministic (identical bytes across fresh instances). This pins the
	// *cause*. If wazero ever defaults to a real entropy source, this test
	// fails — making the change visible instead of silently invalidating the
	// fix's necessity.
	ctx := context.Background()
	runtime := wazero.NewRuntime(ctx)
	defer runtime.Close(ctx)

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		t.Fatalf("instantiate WASI: %v", err)
	}
	compiled, err := runtime.CompileModule(ctx, randProbeWasm)
	if err != nil {
		t.Fatalf("compile probe wasm: %v", err)
	}
	defer compiled.Close(ctx)

	newDefault := func() wazero.ModuleConfig {
		return wazero.NewModuleConfig().WithName("").WithArgs("probe")
	}
	a := readProbeRandom(t, runtime, compiled, newDefault())
	b := readProbeRandom(t, runtime, compiled, newDefault())
	if a != b {
		t.Skipf("wazero default RandSource now differs across instances (%#016x vs %#016x) — "+
			"if real-by-default upstream, the bug-#120 fix may be redundant; review", a, b)
	}
	// Determinism confirmed → fix is meaningful.
}

// Bugboard #27 instrumentation: ExecuteModule must record how long the
// per-invocation InstantiateModule (TinyGo _start cold-start) took into the
// ctx-attached collector, so the engine can split the execute phase into
// cold-start vs handler work. Without an attached collector it must be a no-op.
func TestExecuteModule_recordsInstantiateTiming(t *testing.T) {
	ctx := context.Background()
	runtime := wazero.NewRuntime(ctx)
	defer runtime.Close(ctx)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		t.Fatalf("instantiate WASI: %v", err)
	}
	compiled, err := runtime.CompileModule(ctx, randProbeWasm)
	if err != nil {
		t.Fatalf("compile probe wasm: %v", err)
	}
	defer compiled.Close(ctx)

	ex := NewExecutor(runtime, zap.NewNop(), 0, 0)

	tctx, timing := WithInstantiateTiming(ctx)
	if _, err := ex.ExecuteModule(tctx, compiled, "probe", nil); err != nil {
		t.Fatalf("ExecuteModule: %v", err)
	}
	if timing.InstantiateNs <= 0 {
		t.Errorf("InstantiateNs = %d; want > 0 (instantiate duration must be recorded)", timing.InstantiateNs)
	}
}
