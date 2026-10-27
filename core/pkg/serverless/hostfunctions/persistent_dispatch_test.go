package hostfunctions

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/rqlite"
	"github.com/Orama-Network/orama/core/pkg/serverless"
	"github.com/Orama-Network/orama/core/pkg/serverless/persistent"
	"go.uber.org/zap"
)

// The persistent-socket ordering contract, end to end in one process (bug-963):
// a real wazero router instance bound to a socket, real host functions, and a
// real engine running the routed op. Only the database is a stand-in, whose
// read takes opDelay the way a leader-routed read does.
//
// One socket's frames run one at a time, in arrival order: wazero cannot run
// two calls in one instance, nor set a call aside while it waits in a host
// function. So a router that runs each op with function_invoke answers the
// last of n ops after n × opDelay. A router that hands each op to
// function_invoke_async returns from its frame at once, and the ops run side
// by side in the namespace's execution slots.

const (
	dispatchOps     = 30
	dispatchOpDelay = 100 * time.Millisecond
	dispatchNS      = "ns"
	dispatchOpName  = "op"
)

// slowReadDB answers every read after dispatchOpDelay and records when.
type slowReadDB struct {
	rqlite.Client
	mu    sync.Mutex
	done  []time.Time
	reads chan struct{}
}

func (d *slowReadDB) Query(ctx context.Context, dest any, query string, args ...any) error {
	time.Sleep(dispatchOpDelay)
	d.mu.Lock()
	d.done = append(d.done, time.Now())
	d.mu.Unlock()
	d.reads <- struct{}{}
	return nil
}

// wasmOnlyRegistry serves the WASM of the two test modules by CID.
type wasmOnlyRegistry struct {
	serverless.FunctionRegistry
	wasm map[string][]byte
}

func (r *wasmOnlyRegistry) GetWASMBytes(_ context.Context, cid string) ([]byte, error) {
	b, ok := r.wasm[cid]
	if !ok {
		return nil, fmt.Errorf("no wasm %q", cid)
	}
	return b, nil
}

// engineInvoker runs function_invoke targets straight through the engine.
type engineInvoker struct {
	engine *serverless.Engine
	fns    map[string]*serverless.Function
}

func (i *engineInvoker) Invoke(ctx context.Context, req *serverless.InvokeRequest) (*serverless.InvokeResponse, error) {
	fn, ok := i.fns[req.FunctionName]
	if !ok {
		return nil, serverless.ErrFunctionNotFound
	}
	out, err := i.engine.Execute(ctx, fn, req.Input, &serverless.InvocationContext{
		Namespace: req.Namespace, FunctionName: fn.Name, WSClientID: req.WSClientID, TriggerType: req.TriggerType,
	})
	if err != nil {
		return nil, err
	}
	return &serverless.InvokeResponse{Output: out, Status: serverless.InvocationStatusSuccess}, nil
}

// opModule is a command module whose _start makes one db_query_v2 read.
func opModule() []byte {
	const query = "SELECT 1"
	return wasmModule{
		types: [][]byte{
			functype(nil, nil),
			functype([]byte{wasmI32, wasmI32, wasmI32, wasmI32}, []byte{wasmI64}),
			functype([]byte{wasmI32}, []byte{wasmI32}),
		},
		imports: []wasmImport{{name: "db_query_v2", typ: 1}},
		funcs: []wasmFunc{
			{typ: 0, body: concat(i32c(0), i32c(int32(len(query))), i32c(0), i32c(0), []byte{opCall, 0x00, opDrop, opEnd})},
			{typ: 2, body: bumpAlloc},
		},
		globals: []int32{1024},
		exports: []wasmExport{{"_start", exportFunc, 1}, {"orama_alloc", exportFunc, 2}, {"memory", exportMemory, 0}},
		data:    []wasmData{{0, []byte(query)}},
	}.encode()
}

// routerModule is a persistent router: ws_frame hands the frame to the
// function named "op" with invokeImport (function_invoke or
// function_invoke_async) and returns.
func routerModule(invokeImport string) []byte {
	result := []byte{wasmI64}
	if invokeImport == "function_invoke_async" {
		result = []byte{wasmI32}
	}
	return wasmModule{
		types: [][]byte{
			functype([]byte{wasmI32, wasmI32, wasmI32, wasmI32}, result),
			functype([]byte{wasmI32}, []byte{wasmI32}),
			functype([]byte{wasmI32, wasmI32}, []byte{wasmI32}),
			functype([]byte{wasmI32, wasmI32}, nil),
		},
		imports: []wasmImport{{name: invokeImport, typ: 0}},
		funcs: []wasmFunc{
			{typ: 1, body: bumpAlloc},                      // 1 orama_alloc
			{typ: 2, body: concat(i32c(0), []byte{opEnd})}, // 2 ws_open: accept
			{typ: 2, body: concat(i32c(0), i32c(int32(len(dispatchOpName))), // 3 ws_frame
				[]byte{opLocalGet, 0x00, opLocalGet, 0x01, opCall, 0x00, opDrop}, i32c(0), []byte{opEnd})},
			{typ: 3, body: []byte{opEnd}}, // 4 ws_close
		},
		globals: []int32{1024},
		exports: []wasmExport{
			{"orama_alloc", exportFunc, 1}, {"ws_open", exportFunc, 2}, {"ws_frame", exportFunc, 3},
			{"ws_close", exportFunc, 4}, {"memory", exportMemory, 0},
		},
		data: []wasmData{{0, []byte(dispatchOpName)}},
	}.encode()
}

// sendOpsOnOneSocket opens a persistent router instance, submits
// dispatchOps frames at once, and returns how long the last op took to finish.
func sendOpsOnOneSocket(t *testing.T, invokeImport string, perNamespace int) time.Duration {
	t.Helper()
	db := &slowReadDB{reads: make(chan struct{}, dispatchOps)}
	h := &HostFunctions{db: db, dbNamespace: dispatchNS, logger: zap.NewNop(),
		asyncInvokeSem: make(chan struct{}, asyncInvokeMaxInFlight)}

	cfg := serverless.DefaultConfig()
	cfg.MaxConcurrentPerNamespace = perNamespace
	registry := &wasmOnlyRegistry{wasm: map[string][]byte{"op-cid": opModule(), "router-cid": routerModule(invokeImport)}}
	engine, err := serverless.NewEngine(cfg, registry, h, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close(context.Background()) })
	op := &serverless.Function{ID: "op", Name: dispatchOpName, Namespace: dispatchNS, WASMCID: "op-cid", TimeoutSeconds: 30}
	h.SetInvoker(&engineInvoker{engine: engine, fns: map[string]*serverless.Function{dispatchOpName: op}})

	router := &serverless.Function{ID: "router", Name: "router", Namespace: dispatchNS, WASMCID: "router-cid", TimeoutSeconds: 30}
	invCtx := &serverless.InvocationContext{Namespace: dispatchNS, FunctionName: "router", WSClientID: "client-1", TriggerType: serverless.TriggerTypeWebSocket}
	module, err := engine.InstantiatePersistent(context.Background(), router, invCtx)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := persistent.NewInstance(module, persistent.Config{ClientID: "client-1", FunctionName: "router",
		Namespace: dispatchNS, FrameTimeoutSec: 30, InvocationContext: invCtx}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Open(context.Background(), persistent.WSOpenInput{ClientID: "client-1", Namespace: dispatchNS}); err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(context.Background())
	go inst.Run(runCtx)
	t.Cleanup(func() {
		stop()
		inst.Close(context.Background(), persistent.CloseReasonClientDisconnect)
	})

	start := time.Now()
	for i := 0; i < dispatchOps; i++ {
		if err := inst.Submit([]byte(fmt.Sprintf(`{"op":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.After(dispatchOps*dispatchOpDelay + 10*time.Second)
	for i := 0; i < dispatchOps; i++ {
		select {
		case <-db.reads:
		case <-deadline:
			t.Fatalf("only %d of %d ops finished", i, dispatchOps)
		}
	}
	return time.Since(start)
}

// TestPersistentSocket_aBlockingFrameHoldsTheSocketAnAsyncOneDoesNot pins the
// contract the functions docs state: 30 ops of 100 ms on one socket take
// 30 × 100 ms through function_invoke, and a few multiples of 100 ms through
// function_invoke_async, bounded by the namespace's execution slots — all ten
// of a namespace gateway's, where the half a shared gateway reserves for its
// other tenants used to cap it at five.
func TestPersistentSocket_aBlockingFrameHoldsTheSocketAnAsyncOneDoesNot(t *testing.T) {
	serial := dispatchOps * dispatchOpDelay

	blocking := sendOpsOnOneSocket(t, "function_invoke", 0)
	t.Logf("function_invoke: last of %d ops after %v", dispatchOps, blocking)
	if blocking < serial {
		t.Errorf("function_invoke: %d ops finished in %v, under the serial %v: frames of one socket overlapped", dispatchOps, blocking, serial)
	}

	shared := sendOpsOnOneSocket(t, "function_invoke_async", 0)
	t.Logf("function_invoke_async, half the slots (5 of 10): last of %d ops after %v", dispatchOps, shared)

	nsGateway := sendOpsOnOneSocket(t, "function_invoke_async", serverless.DefaultConfig().MaxConcurrentExecutions)
	t.Logf("function_invoke_async, a namespace gateway's 10 slots: last of %d ops after %v", dispatchOps, nsGateway)
	if limit := serial / 5; nsGateway >= limit {
		t.Errorf("function_invoke_async on a namespace gateway: %d ops took %v, want well under the serial %v (< %v)", dispatchOps, nsGateway, serial, limit)
	}
}
