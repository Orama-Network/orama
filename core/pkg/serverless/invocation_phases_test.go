package serverless

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
)

// slowFetchRegistry is a MockRegistry whose WASM fetch takes fetchDelay, the
// way a cold module's fetch from IPFS does.
type slowFetchRegistry struct {
	*MockRegistry
	fetchDelay time.Duration
}

func (r *slowFetchRegistry) GetWASMBytes(ctx context.Context, cid string) ([]byte, error) {
	time.Sleep(r.fetchDelay)
	return r.MockRegistry.GetWASMBytes(ctx, cid)
}

// TestExecute_recordSaysWhereTheDurationWent (bug-963): every invocation
// record names the node that ran it and splits its duration, so a cold module
// shows as load time on its first call and not on the next.
func TestExecute_recordSaysWhereTheDurationWent(t *testing.T) {
	const fetchDelay = 40 * time.Millisecond
	registry := &slowFetchRegistry{MockRegistry: NewMockRegistry(), fetchDelay: fetchDelay}
	sink := &mockInvocationLogger{}
	cfg := DefaultConfig()
	cfg.NodeID = "12D3KooWnode"
	engine, err := NewEngine(cfg, registry, NewMockHostServices(), zap.NewNop(), WithInvocationLogger(sink))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(context.Background())

	fn := register(t, registry.MockRegistry, "ns", "f")
	for i := 0; i < 2; i++ {
		if _, err := engine.Execute(context.Background(), fn, nil, nil); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	eventually(t, 5*time.Second, func() bool { return sink.count() == 2 })

	sink.mu.Lock()
	defer sink.mu.Unlock()
	cold, warm := sink.records[0], sink.records[1]
	for _, r := range sink.records {
		if r.NodeID != cfg.NodeID {
			t.Errorf("record names node %q, want %q", r.NodeID, cfg.NodeID)
		}
		if r.QueueMS+r.LoadMS+r.DBMS > r.DurationMS {
			t.Errorf("phases %d+%d+%d ms exceed the duration %d ms", r.QueueMS, r.LoadMS, r.DBMS, r.DurationMS)
		}
	}
	if cold.LoadMS < fetchDelay.Milliseconds() {
		t.Errorf("the first call's load is %d ms, want at least the %v fetch", cold.LoadMS, fetchDelay)
	}
	if warm.LoadMS != 0 {
		t.Errorf("the second call's load is %d ms, want 0: the module was in memory", warm.LoadMS)
	}
}
