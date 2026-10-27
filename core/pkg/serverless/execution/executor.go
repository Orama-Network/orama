package execution

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"go.uber.org/zap"
)

// InstantiateTiming captures how long the per-invocation wazero
// InstantiateModule call took (running TinyGo _start / package init), and how
// long the call waited for an execution slot before it. It rides the ctx so
// the engine can split the execute phase into slot wait, cold-start
// (instantiate) and handler work (run) — the distinction that pins the
// bugboard #27 cold-start floor, and tells a burst queued on the namespace's
// slots from a slow handler (bug-963). Nil collector = not measured.
type InstantiateTiming struct {
	QueueNs       int64
	InstantiateNs int64
}

type instantiateTimingKey struct{}

// WithInstantiateTiming returns a ctx carrying a fresh InstantiateTiming that
// ExecuteModule will fill in. The caller reads it back after ExecuteModule.
func WithInstantiateTiming(ctx context.Context) (context.Context, *InstantiateTiming) {
	t := &InstantiateTiming{}
	return context.WithValue(ctx, instantiateTimingKey{}, t), t
}

func instantiateTimingFrom(ctx context.Context) *InstantiateTiming {
	t, _ := ctx.Value(instantiateTimingKey{}).(*InstantiateTiming)
	return t
}

type namespaceKey struct{}

// WithNamespace tags an execution ctx so the limiter can cap one tenant
// without letting it fill the process-wide semaphore (bugboard #85).
func WithNamespace(ctx context.Context, ns string) context.Context {
	return context.WithValue(ctx, namespaceKey{}, ns)
}

func namespaceFrom(ctx context.Context) string {
	ns, _ := ctx.Value(namespaceKey{}).(string)
	return ns
}

// Executor handles WASM module execution.
type Executor struct {
	runtime wazero.Runtime
	logger  *zap.Logger
	sem     chan struct{} // process-wide limiter
	perNS   map[string]chan struct{}
	perNSN  int
	mu      sync.Mutex
}

// NewExecutor creates a new Executor.
// maxConcurrent limits simultaneous module instantiations (0 = unlimited).
// perNamespace caps one namespace's share of them; 0 means half of
// maxConcurrent (at least 1), the split a gateway running several namespaces
// needs. A value above maxConcurrent is clamped to it.
func NewExecutor(runtime wazero.Runtime, logger *zap.Logger, maxConcurrent, perNamespace int) *Executor {
	var sem chan struct{}
	perNSN := 0
	if maxConcurrent > 0 {
		sem = make(chan struct{}, maxConcurrent)
		perNSN = perNamespace
		if perNSN <= 0 {
			perNSN = maxConcurrent / 2
		}
		if perNSN < 1 {
			perNSN = 1
		}
		if perNSN > maxConcurrent {
			perNSN = maxConcurrent
		}
	}
	return &Executor{
		runtime: runtime,
		logger:  logger,
		sem:     sem,
		perNS:   make(map[string]chan struct{}),
		perNSN:  perNSN,
	}
}

func (e *Executor) nsSlot(ns string) chan struct{} {
	if e.perNSN <= 0 || ns == "" {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	ch := e.perNS[ns]
	if ch == nil {
		ch = make(chan struct{}, e.perNSN)
		e.perNS[ns] = ch
	}
	return ch
}

// acquire takes ns's slot, then a process-wide one, and returns the release of
// both. The namespace slot comes first: a burst from one namespace queues on
// its own slot without holding a process-wide token, so it cannot take every
// token while it waits and starve the other namespaces.
func (e *Executor) acquire(ctx context.Context, ns string) (func(), error) {
	slot := e.nsSlot(ns)
	if slot != nil {
		select {
		case slot <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if e.sem != nil {
		select {
		case e.sem <- struct{}{}:
		case <-ctx.Done():
			if slot != nil {
				<-slot
			}
			return nil, ctx.Err()
		}
	}
	return func() {
		if e.sem != nil {
			<-e.sem
		}
		if slot != nil {
			<-slot
		}
	}, nil
}

// ExecuteModule instantiates and runs a WASM module with the given input.
// The invocation's identity rides ctx; there is nothing to set on the host
// services, which hold no per-invocation state.
//
// Bug #221 fix: each invocation gets an ANONYMOUS module instance (no name
// in the wazero runtime registry). Previously we used the function name
// as the module name, which made wazero refuse the second instantiation
// with "module[<fn>] has already been instantiated" any time:
//
//   - Two invocations of the same function ran concurrently
//   - A re-deploy happened while a previous invocation was still mid-flight
//   - An instance.Close() raced with the next instantiation
//
// Anonymous instances eliminate the collision class entirely. The
// function name is still surfaced via WithArgs(moduleName) so WASI
// `argv[0]` shows it inside the function (matches the prior behavior
// that user code may depend on).
func (e *Executor) ExecuteModule(ctx context.Context, compiled wazero.CompiledModule, moduleName string, input []byte) ([]byte, error) {
	// Set invocation context for host functions

	// Create buffers for stdin/stdout (WASI uses these for I/O)
	stdin := bytes.NewReader(input)
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	// Create module configuration with WASI stdio.
	//
	// WithName("") = anonymous instance, NOT registered under the runtime's
	// global module name table. argv[0] still carries the function name for
	// WASI-consuming code that reads os.Args[0] (TinyGo's runtime + apps
	// that log it). See doc comment above for the bug-#221 rationale.
	moduleConfig := wazero.NewModuleConfig().
		WithName("").
		WithStdin(stdin).
		WithStdout(stdout).
		WithStderr(stderr).
		WithArgs(moduleName). // argv[0] is the program name
		// Bugboard #27 — wazero defaults to fake/sentinel clocks. Without
		// these opt-ins, TinyGo's time.Now() returns ~2022-01-01T00:00:00.001Z
		// frozen on every read, silently poisoning timestamps in every
		// invocation that uses time.Now() (receipts, audit rows, cursor cmp).
		// Same fix applied at engine.go for the persistent-WS path.
		WithSysWalltime().
		WithSysNanotime().
		// Bugboard #120 — same class as #27. Without WithRandSource, wazero
		// uses a deterministic zero-seed RNG, so TinyGo's crypto/rand.Read
		// returns IDENTICAL bytes on every fresh instance (and every
		// invocation is a fresh instance). That makes any unguessable ID /
		// code / nonce / token constant. Wire in the host CSPRNG so
		// crypto/rand (and auto-seeded math/rand) work. Same fix at
		// engine.go for the persistent-WS path.
		WithRandSource(cryptorand.Reader)

	// Enforce the function's own memory limit for this invocation only: the
	// allocator rides the instantiate ctx, so it never touches another function.
	limitMB := memoryLimitMBFrom(ctx)
	startRefused := false
	if limitMB > 0 {
		if err := checkMinMemory(compiled, limitMB); err != nil {
			return nil, err
		}
		ctx = experimental.WithMemoryAllocator(ctx, cappedAllocator{limitBytes: uint64(limitMB) * bytesPerMB, startRefused: &startRefused})
	}

	queueStart := time.Now()
	release, err := e.acquire(ctx, namespaceFrom(ctx))
	if t := instantiateTimingFrom(ctx); t != nil {
		t.QueueNs = time.Since(queueStart).Nanoseconds()
	}
	if err != nil {
		return nil, err
	}
	defer release()

	// Instantiate and run the module (WASI _start will be called automatically).
	// Time the instantiate so the engine can attribute cold-start vs handler
	// work (bugboard #27 cold-start floor); no-op when no collector is attached.
	instStart := time.Now()
	instance, err := e.instantiate(ctx, compiled, moduleConfig, &startRefused, limitMB)
	if t := instantiateTimingFrom(ctx); t != nil {
		t.InstantiateNs = time.Since(instStart).Nanoseconds()
	}
	if err != nil {
		// Check if stderr has any output
		if stderr.Len() > 0 {
			e.logger.Warn("WASM stderr output", zap.String("stderr", stderr.String()))
		}
		return nil, fmt.Errorf("failed to instantiate module: %w", err)
	}
	defer instance.Close(ctx)

	// For WASI modules, the output is already in stdout buffer
	// The _start function was called during instantiation
	output := stdout.Bytes()

	// Log stderr if any
	if stderr.Len() > 0 {
		e.logger.Debug("WASM stderr", zap.String("stderr", stderr.String()))
	}

	return output, nil
}

// CallHandleFunction calls the main 'handle' export in the WASM module.
// This is an alternative execution path for modules that export a 'handle' function.
func (e *Executor) CallHandleFunction(ctx context.Context, instance api.Module, input []byte) ([]byte, error) {
	// Get the 'handle' function export
	handleFn := instance.ExportedFunction("handle")
	if handleFn == nil {
		return nil, fmt.Errorf("WASM module does not export 'handle' function")
	}

	// Get memory export
	memory := instance.ExportedMemory("memory")
	if memory == nil {
		return nil, fmt.Errorf("WASM module does not export 'memory'")
	}

	// Get malloc/free exports for memory management
	mallocFn := instance.ExportedFunction("malloc")
	freeFn := instance.ExportedFunction("free")

	var inputPtr uint32
	var inputLen = uint32(len(input))

	if mallocFn != nil && len(input) > 0 {
		// Allocate memory for input
		results, err := mallocFn.Call(ctx, uint64(inputLen))
		if err != nil {
			return nil, fmt.Errorf("malloc failed: %w", err)
		}
		inputPtr = uint32(results[0])

		// Write input to memory
		if !memory.Write(inputPtr, input) {
			return nil, fmt.Errorf("failed to write input to WASM memory")
		}

		// Defer free if available
		if freeFn != nil {
			defer func() {
				_, _ = freeFn.Call(ctx, uint64(inputPtr))
			}()
		}
	}

	// Call handle(input_ptr, input_len)
	// Returns: output_ptr (packed with length in upper 32 bits)
	results, err := handleFn.Call(ctx, uint64(inputPtr), uint64(inputLen))
	if err != nil {
		return nil, fmt.Errorf("handle function error: %w", err)
	}

	if len(results) == 0 {
		return nil, nil // No output
	}

	// Parse result - assume format: lower 32 bits = ptr, upper 32 bits = len
	result := results[0]
	outputPtr := uint32(result & 0xFFFFFFFF)
	outputLen := uint32(result >> 32)

	if outputLen == 0 {
		return nil, nil
	}

	// Read output from memory
	output, ok := memory.Read(outputPtr, outputLen)
	if !ok {
		return nil, fmt.Errorf("failed to read output from WASM memory")
	}

	// Make a copy (memory will be freed)
	outputCopy := make([]byte, len(output))
	copy(outputCopy, output)

	return outputCopy, nil
}

// WriteToGuest allocates memory in the guest WASM module and writes data to it.
// Returns a packed uint64 with ptr in upper 32 bits and length in lower 32 bits.
func (e *Executor) WriteToGuest(ctx context.Context, mod api.Module, data []byte) uint64 {
	if len(data) == 0 {
		return 0
	}
	// Try to find a non-conflicting allocator first, fallback to malloc
	malloc := mod.ExportedFunction("orama_alloc")
	if malloc == nil {
		malloc = mod.ExportedFunction("malloc")
	}

	if malloc == nil {
		e.logger.Warn("WASM module missing malloc/orama_alloc export, cannot return string/bytes to guest")
		return 0
	}
	results, err := malloc.Call(ctx, uint64(len(data)))
	if err != nil {
		e.logger.Error("failed to call malloc in WASM module", zap.Error(err))
		return 0
	}
	ptr := uint32(results[0])
	if !mod.Memory().Write(ptr, data) {
		e.logger.Error("failed to write to WASM memory")
		return 0
	}
	return (uint64(ptr) << 32) | uint64(len(data))
}

// ReadFromGuest reads a string from guest memory.
func (e *Executor) ReadFromGuest(mod api.Module, ptr, size uint32) ([]byte, bool) {
	return mod.Memory().Read(ptr, size)
}

// UnmarshalJSONFromGuest reads and unmarshals JSON data from guest memory.
func (e *Executor) UnmarshalJSONFromGuest(mod api.Module, ptr, size uint32, v interface{}) error {
	data, ok := mod.Memory().Read(ptr, size)
	if !ok {
		return fmt.Errorf("failed to read from guest memory")
	}
	return json.Unmarshal(data, v)
}

// instantiate instantiates compiled. A memory whose start the function's limit
// refused makes wazero panic slicing the buffer it was denied; that panic, and
// only that one, is the module not fitting its limit and is returned as an
// error. Any other panic is not this package's to interpret and goes on.
func (e *Executor) instantiate(ctx context.Context, compiled wazero.CompiledModule, cfg wazero.ModuleConfig, startRefused *bool, limitMB int) (instance api.Module, err error) {
	defer func() {
		if r := recover(); r != nil {
			if !*startRefused {
				panic(r)
			}
			instance, err = nil, instantiateRefused(limitMB)
		}
	}()
	return e.runtime.InstantiateModule(ctx, compiled, cfg)
}
