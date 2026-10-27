package serverless

import (
	"context"
	"sync/atomic"
	"time"
)

// DBUsage counts an invocation's database host calls and the wall time they
// took (bug-963). The same function answered in 20 ms on one gateway and in
// 250 ms on another: a read at the default "weak" consistency is answered by
// the namespace's raft leader, so on a gateway whose node is not the leader
// every read costs a round trip to it, and its invocation record could not
// show that. With the gateway's node and this split recorded, the record
// says where the time went.
//
// It rides the invocation's context like the publish counter: Execute attaches
// a fresh one, so a nested function_invoke counts in its own record, not in
// its parent's.
type DBUsage struct {
	calls atomic.Int64
	ns    atomic.Int64
}

type dbUsageKey struct{}

// WithDBUsage returns ctx carrying a fresh DBUsage, and the DBUsage.
func WithDBUsage(ctx context.Context) (context.Context, *DBUsage) {
	u := &DBUsage{}
	return context.WithValue(ctx, dbUsageKey{}, u), u
}

// RecordDBCall adds one database host call that began at start to the usage
// on ctx. A ctx without one (a persistent frame, a test) records nothing.
// Meant for a defer at the top of a database host function.
func RecordDBCall(ctx context.Context, start time.Time) {
	u, _ := ctx.Value(dbUsageKey{}).(*DBUsage)
	if u == nil {
		return
	}
	u.calls.Add(1)
	u.ns.Add(time.Since(start).Nanoseconds())
}

// Calls is the number of database host calls recorded.
func (u *DBUsage) Calls() int64 { return u.calls.Load() }

// Duration is the wall time the recorded calls took, summed.
func (u *DBUsage) Duration() time.Duration { return time.Duration(u.ns.Load()) }
