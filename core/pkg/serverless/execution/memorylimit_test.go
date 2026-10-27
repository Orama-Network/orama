package execution

import (
	"context"
	"strings"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"go.uber.org/zap"
)

func uleb(v uint32) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			out = append(out, b|0x80)
			continue
		}
		return append(out, b)
	}
}

func section(id byte, body []byte) []byte {
	out := []byte{id}
	out = append(out, uleb(uint32(len(body)))...)
	return append(out, body...)
}

// growProbeWasm assembles:
//
//	(module
//	  (memory (export "memory") minPages)
//	  (func (export "_start")
//	    (if (i32.eq (memory.grow (i32.const growPages)) (i32.const -1))
//	      (then unreachable))))
//
// _start traps when the host refuses the grow, like a guest runtime that
// panics on out-of-memory.
func growProbeWasm(minPages, growPages uint32) []byte {
	// i32.const takes a signed LEB128; growPages stays below 2^20 here, so an
	// unsigned LEB with the sign bit clear in the last byte is equivalent.
	grow := uleb(growPages)
	if grow[len(grow)-1]&0x40 != 0 {
		grow[len(grow)-1] |= 0x80
		grow = append(grow, 0x00)
	}
	code := []byte{0x00, 0x41} // no locals; i32.const
	code = append(code, grow...)
	code = append(code, 0x40, 0x00, 0x41, 0x7f, 0x46, 0x04, 0x40, 0x00, 0x0b, 0x0b)

	mod := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	mod = append(mod, section(1, []byte{0x01, 0x60, 0x00, 0x00})...)
	mod = append(mod, section(3, []byte{0x01, 0x00})...)
	mod = append(mod, section(5, append([]byte{0x01, 0x00}, uleb(minPages)...))...)
	mod = append(mod, section(7, []byte{
		0x02,
		0x06, 'm', 'e', 'm', 'o', 'r', 'y', 0x02, 0x00,
		0x06, '_', 's', 't', 'a', 'r', 't', 0x00, 0x00,
	})...)
	body := append(uleb(uint32(len(code))), code...)
	return append(mod, section(10, append([]byte{0x01}, body...))...)
}

func runGrowProbe(t *testing.T, minPages, growPages uint32, limitMB int) error {
	t.Helper()
	ctx := context.Background()
	runtime := wazero.NewRuntime(ctx)
	defer runtime.Close(ctx)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		t.Fatalf("instantiate WASI: %v", err)
	}
	compiled, err := runtime.CompileModule(ctx, growProbeWasm(minPages, growPages))
	if err != nil {
		t.Fatalf("compile probe wasm: %v", err)
	}
	defer compiled.Close(ctx)

	ex := NewExecutor(runtime, zap.NewNop(), 0, 0)
	_, err = ex.ExecuteModule(WithMemoryLimitMB(ctx, limitMB), compiled, "probe", nil)
	return err
}

func TestExecuteModule_growUnderFunctionLimitWorks(t *testing.T) {
	// 1 page + 31 pages = 2 MB, exactly the 2 MB limit.
	if err := runGrowProbe(t, 1, 31, 2); err != nil {
		t.Fatalf("grow within limit failed: %v", err)
	}
}

func TestExecuteModule_growOverFunctionLimitFailsInvocation(t *testing.T) {
	// 1 + 32 pages = 2 MB + 64 KB > 2 MB limit; well under any runtime-wide cap.
	if err := runGrowProbe(t, 1, 32, 2); err == nil {
		t.Fatal("grow past the function's memory limit succeeded; want the invocation to fail")
	}
}

func TestExecuteModule_noLimitLeavesGrowthToRuntimeCap(t *testing.T) {
	// limit 0 = no per-function cap attached; the same 32-page grow works.
	if err := runGrowProbe(t, 1, 32, 0); err != nil {
		t.Fatalf("grow without a per-function limit failed: %v", err)
	}
}

func TestExecuteModule_minimumAboveLimitIsRefused(t *testing.T) {
	// 48 pages = 3 MB declared minimum, limit 2 MB.
	err := runGrowProbe(t, 48, 0, 2)
	if err == nil {
		t.Fatal("module whose minimum memory exceeds the limit was instantiated")
	}
	if !strings.Contains(err.Error(), "memory limit of 2 MB") {
		t.Errorf("error %q does not name the limit", err)
	}
}

func TestCappedMemory_reallocateBoundaries(t *testing.T) {
	m := cappedAllocator{limitBytes: 100}.Allocate(10, 0)
	buf := m.Reallocate(10)
	buf[9] = 7
	grown := m.Reallocate(100)
	if len(grown) != 100 || grown[9] != 7 {
		t.Fatalf("grow lost data or length: len=%d", len(grown))
	}
	if m.Reallocate(101) != nil {
		t.Error("Reallocate past the limit returned a buffer")
	}
	if m.Reallocate(0) == nil {
		t.Error("Reallocate(0) must succeed")
	}
}

// hiddenMemoryWasm is a module whose only memory is NOT exported and starts at
// minPages, with an empty _start: checkMinMemory, which reads exported
// memories, cannot see it.
func hiddenMemoryWasm(minPages uint32) []byte {
	mod := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	mod = append(mod, section(1, []byte{0x01, 0x60, 0x00, 0x00})...)
	mod = append(mod, section(3, []byte{0x01, 0x00})...)
	mod = append(mod, section(5, append([]byte{0x01, 0x00}, uleb(minPages)...))...)
	mod = append(mod, section(7, []byte{0x01, 0x06, '_', 's', 't', 'a', 'r', 't', 0x00, 0x00})...)
	body := append(uleb(2), 0x00, 0x0b) // no locals; end
	return append(mod, section(10, append([]byte{0x01}, body...))...)
}

// A memory that is not exported and starts above the function's limit made
// wazero panic on the start buffer the allocator refused, a panic any tenant
// could cause by deploying such a module with a small limit. It is an error.
func TestExecuteModule_hiddenMemoryAboveLimitIsAnErrorNotAPanic(t *testing.T) {
	ctx := context.Background()
	runtime := wazero.NewRuntime(ctx)
	t.Cleanup(func() { _ = runtime.Close(ctx) })
	compiled, err := runtime.CompileModule(ctx, hiddenMemoryWasm(64)) // 4 MB
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ex := NewExecutor(runtime, zap.NewNop(), 0, 0)
	_, err = ex.ExecuteModule(WithMemoryLimitMB(ctx, 1), compiled, "hidden", nil)
	if err == nil || !strings.Contains(err.Error(), "memory limit of 1 MB") {
		t.Fatalf("a hidden 4 MB memory under a 1 MB limit answered %v, want the limit refusal", err)
	}
	// Within the limit the same module runs.
	if _, err := ex.ExecuteModule(WithMemoryLimitMB(ctx, 8), compiled, "hidden-ok", nil); err != nil {
		t.Fatalf("a hidden 4 MB memory under an 8 MB limit: %v", err)
	}
}
