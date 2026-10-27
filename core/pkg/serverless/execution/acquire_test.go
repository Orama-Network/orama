package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
)

// acquireWithin acquires for ns, failing when it takes longer than d.
func acquireWithin(e *Executor, ns string, d time.Duration) (func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return e.acquire(ctx, ns)
}

// TestAcquire_aQueuedBurstDoesNotStarveAnotherNamespace: with two process
// slots (one per namespace), a second call from "a" queues on a's slot; it must
// not hold the second process slot while it waits, or "b" could never run.
func TestAcquire_aQueuedBurstDoesNotStarveAnotherNamespace(t *testing.T) {
	e := NewExecutor(nil, zap.NewNop(), 2, 0)
	releaseA, err := acquireWithin(e, "a", time.Second)
	if err != nil {
		t.Fatalf("first call of a: %v", err)
	}

	queued := make(chan struct{})
	go func() {
		defer close(queued)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if release, err := e.acquire(ctx, "a"); err == nil {
			release()
		}
	}()
	time.Sleep(50 * time.Millisecond) // let the second call of a start waiting

	releaseB, err := acquireWithin(e, "b", 500*time.Millisecond)
	if err != nil {
		t.Fatalf("b could not run while a's burst queued: %v", err)
	}
	releaseB()
	releaseA()
	<-queued
}

func TestAcquire_releaseFreesBothSlots(t *testing.T) {
	e := NewExecutor(nil, zap.NewNop(), 2, 0)
	for i := 0; i < 3; i++ {
		release, err := acquireWithin(e, "a", 200*time.Millisecond)
		if err != nil {
			t.Fatalf("call %d: a slot was not given back: %v", i, err)
		}
		release()
	}
	if len(e.sem) != 0 || len(e.nsSlot("a")) != 0 {
		t.Errorf("tokens still held: process %d, namespace %d", len(e.sem), len(e.nsSlot("a")))
	}
}

func TestAcquire_aCancelledWaitGivesBackTheNamespaceSlot(t *testing.T) {
	e := NewExecutor(nil, zap.NewNop(), 2, 0)
	hold := []func(){}
	for _, ns := range []string{"x", "y"} {
		release, err := acquireWithin(e, ns, time.Second)
		if err != nil {
			t.Fatalf("fill process slots: %v", err)
		}
		hold = append(hold, release)
	}
	if _, err := acquireWithin(e, "a", 50*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("with every process slot taken: want a deadline error, got %v", err)
	}
	if n := len(e.nsSlot("a")); n != 0 {
		t.Errorf("a's slot is still held after its wait was cancelled: %d", n)
	}
	for _, release := range hold {
		release()
	}
}

// TestAcquire_perNamespaceShare: by default one namespace gets half of the
// process slots; a namespace gateway, which runs one namespace, gives it every
// slot (bug-963: a namespace gateway used to run at most half of its slots).
func TestAcquire_perNamespaceShare(t *testing.T) {
	for _, tc := range []struct {
		name       string
		max, perNS int
		wantPerNS  int
	}{
		{"default is half", 10, 0, 5},
		{"single tenant takes every slot", 10, 10, 10},
		{"above the process cap is clamped", 4, 9, 4},
		{"never below one", 1, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewExecutor(nil, zap.NewNop(), tc.max, tc.perNS)
			var held []func()
			for i := 0; i < tc.wantPerNS; i++ {
				release, err := acquireWithin(e, "a", time.Second)
				if err != nil {
					t.Fatalf("slot %d of %d: %v", i+1, tc.wantPerNS, err)
				}
				held = append(held, release)
			}
			if _, err := acquireWithin(e, "a", 50*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("slot %d: want the namespace to be full, got %v", tc.wantPerNS+1, err)
			}
			for _, release := range held {
				release()
			}
		})
	}
}

func TestAcquire_unboundedExecutorNeedsNoSlot(t *testing.T) {
	e := NewExecutor(nil, zap.NewNop(), 0, 0)
	release, err := acquireWithin(e, "", time.Millisecond)
	if err != nil {
		t.Fatalf("unbounded: %v", err)
	}
	release()
}
