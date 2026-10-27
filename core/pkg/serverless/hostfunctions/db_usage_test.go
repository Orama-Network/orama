package hostfunctions

import (
	"context"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/rqlite"
	"github.com/Orama-Network/orama/core/pkg/serverless"
)

// leaderHop stands in for a weak read from a follower: the leader is a round
// trip away, so every read takes at least that long.
const leaderHop = 30 * time.Millisecond

// TestDBUsage_eachDatabaseHostCallIsCountedWithItsTime (bug-963): an
// invocation's record splits out the database calls it made and their summed
// wall time, refused calls included, so a slow band on one gateway shows as
// time spent waiting on the database rather than in the function.
func TestDBUsage_eachDatabaseHostCallIsCountedWithItsTime(t *testing.T) {
	fake := &fakeBatchClient{respondQuery: func(ops []rqlite.BatchOp) ([]rqlite.OpResult, error) {
		time.Sleep(leaderHop)
		return []rqlite.OpResult{{Kind: rqlite.BatchOpQuery}}, nil
	}}
	h := newHFWithDB(fake)
	ctx, usage := serverless.WithDBUsage(nsCtx())

	read := []byte(`{"ops":[{"sql":"SELECT 1"}]}`)
	for i := 0; i < 2; i++ {
		if _, err := h.DBQueryBatch(ctx, read); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.DBTransaction(ctx, []byte(`not json`)); err == nil {
		t.Fatal("a malformed transaction was accepted")
	}

	if got := usage.Calls(); got != 3 {
		t.Errorf("recorded %d database calls, want 3", got)
	}
	if got := usage.Duration(); got < 2*leaderHop {
		t.Errorf("recorded %v in database calls, want at least two leader hops (%v)", got, 2*leaderHop)
	}
}

// TestDBUsage_aContextWithoutUsageRecordsNothing: a persistent frame carries
// no usage (it writes no invocation record); the host call still works.
func TestDBUsage_aContextWithoutUsageRecordsNothing(t *testing.T) {
	h := newHFWithDB(&fakeBatchClient{})
	if _, err := h.DBQueryBatch(nsCtx(), []byte(`{"ops":[{"sql":"SELECT 1"}]}`)); err != nil {
		t.Fatal(err)
	}
	serverless.RecordDBCall(context.Background(), time.Now())
}
