package boot

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fastOpts keeps the converge loop honest but quick: real timers, tiny values.
func fastOpts() Options {
	return Options{
		BaseBackoff:    time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
		HealthInterval: time.Millisecond,
	}
}

// recorder counts calls and lets a component be flipped from failing to
// succeeding while the supervisor is running.
type recorder struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (r *recorder) reconcile(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.err
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *recorder) setErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// runUntil starts Run in a goroutine and stops it once cond holds or the
// deadline passes. It returns whether cond ever held.
func runUntil(t *testing.T, s *Supervisor, cond func() bool) bool {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	ok := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !ok {
		select {
		case err := <-done:
			cancel()
			t.Fatalf("Run returned early: %v", err)
		case <-time.After(time.Millisecond):
			ok = cond()
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of cancellation")
	}
	return ok
}

func TestSupervisor_reconcilesInDependencyOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(name string) func(context.Context) error {
		return func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			order = append(order, name)
			return nil
		}
	}

	// Registered out of the order they must run in would be a graph error, so
	// the interesting assertion is that one pass runs the whole chain.
	s := New(nil, fastOpts())
	s.Add(Component{Name: "a", Reconcile: record("a")})
	s.Add(Component{Name: "b", DependsOn: []string{"a"}, Reconcile: record("b")})
	s.Add(Component{Name: "c", DependsOn: []string{"a", "b"}, Reconcile: record("c")})

	if !runUntil(t, s, func() bool { return s.Snapshot().AllReady() }) {
		t.Fatalf("components did not converge: %v", s.Snapshot().NotReady())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || order[0] != "a" || order[1] != "b" || order[2] != "c" {
		t.Fatalf("reconcile order = %v, want [a b c]", order)
	}
}

func TestSupervisor_failingComponentDoesNotBlockIndependents(t *testing.T) {
	bad := &recorder{err: errors.New("nope")}
	good := &recorder{}

	s := New(nil, fastOpts())
	s.Add(Component{Name: "bad", Reconcile: bad.reconcile})
	s.Add(Component{Name: "good", Reconcile: good.reconcile})

	if !runUntil(t, s, func() bool { return good.count() > 0 && bad.count() > 3 }) {
		t.Fatalf("independent component never converged (bad=%d good=%d)", bad.count(), good.count())
	}

	snap := s.Snapshot()
	byName := map[string]ComponentStatus{}
	for _, c := range snap.Components {
		byName[c.Name] = c
	}
	if byName["good"].Status != StatusReady {
		t.Fatalf("good status = %q, want ready", byName["good"].Status)
	}
	if byName["bad"].Status != StatusPending {
		t.Fatalf("bad status = %q, want pending", byName["bad"].Status)
	}
	if byName["bad"].LastErr == nil {
		t.Fatal("bad component should carry its last error")
	}
	if snap.AllReady() {
		t.Fatal("AllReady must be false while one component is failing")
	}
}

func TestSupervisor_dependentStaysBlockedThenConverges(t *testing.T) {
	dep := &recorder{err: errors.New("not yet")}
	child := &recorder{}

	s := New(nil, fastOpts())
	s.Add(Component{Name: "dep", Reconcile: dep.reconcile})
	s.Add(Component{Name: "child", DependsOn: []string{"dep"}, Reconcile: child.reconcile})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// While dep fails, child must never be attempted.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if child.count() != 0 {
			t.Fatalf("child reconciled %d times while its dependency was failing", child.count())
		}
		time.Sleep(time.Millisecond)
	}
	if dep.count() < 2 {
		t.Fatalf("dependency was retried only %d times", dep.count())
	}

	dep.setErr(nil)

	waitFor(t, "the graph to converge after the dependency recovered", func() bool {
		return s.Snapshot().AllReady()
	})

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestSupervisor_healthRegressionReconcilesAgainAndBlocksDependents(t *testing.T) {
	var healthy sync.Map
	healthy.Store("ok", true)

	svc := &recorder{}
	child := &recorder{}

	s := New(nil, fastOpts())
	s.Add(Component{
		Name:      "svc",
		Reconcile: svc.reconcile,
		Health: func(context.Context) error {
			if v, _ := healthy.Load("ok"); v == true {
				return nil
			}
			return errors.New("unhealthy")
		},
	})
	s.Add(Component{Name: "child", DependsOn: []string{"svc"}, Reconcile: child.reconcile})

	// The dependent must go back to blocked, not merely be reconciled again:
	// a component whose dependency is unhealthy has no business running.
	// Blocked lasts only until the next pass reconciles svc, so a poll of
	// Snapshot can miss it; every status change is published, so the test
	// watches those instead.
	var regressed atomic.Bool
	childBlocked := make(chan struct{})
	var markBlocked sync.Once
	s.OnChange(func(snap Snapshot) {
		if !regressed.Load() {
			return
		}
		for _, c := range snap.Components {
			if c.Name == "child" && c.Status == StatusBlocked {
				markBlocked.Do(func() { close(childBlocked) })
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	waitFor(t, "initial convergence", func() bool { return s.Snapshot().AllReady() })

	reconcilesBefore := svc.count()
	childBefore := child.count()
	regressed.Store(true)
	healthy.Store("ok", false)

	waitFor(t, "health regression to reconcile svc again", func() bool { return svc.count() > reconcilesBefore })
	select {
	case <-childBlocked:
	case <-time.After(waitForTimeout):
		t.Fatal("child was never marked blocked while svc was unhealthy")
	}
	healthy.Store("ok", true)
	waitFor(t, "child to be reconciled again once svc recovers", func() bool {
		return child.count() > childBefore
	})

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestSupervisor_cancelStopsPromptly(t *testing.T) {
	// A component that never converges keeps the loop busy with backoff.
	bad := &recorder{err: errors.New("nope")}
	s := New(nil, Options{BaseBackoff: time.Hour, MaxBackoff: time.Hour})
	s.Add(Component{Name: "bad", Reconcile: bad.reconcile})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	waitFor(t, "first attempt", func() bool { return bad.count() > 0 })

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of cancellation")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
}

func TestSupervisor_rejectsInvalidGraphs(t *testing.T) {
	noop := func(context.Context) error { return nil }

	tests := []struct {
		name string
		add  func(*Supervisor)
	}{
		{"unnamed", func(s *Supervisor) { s.Add(Component{Reconcile: noop}) }},
		{"no reconcile", func(s *Supervisor) { s.Add(Component{Name: "a"}) }},
		{"duplicate", func(s *Supervisor) {
			s.Add(Component{Name: "a", Reconcile: noop})
			s.Add(Component{Name: "a", Reconcile: noop})
		}},
		{"forward dependency", func(s *Supervisor) {
			s.Add(Component{Name: "a", DependsOn: []string{"b"}, Reconcile: noop})
			s.Add(Component{Name: "b", Reconcile: noop})
		}},
		{"self dependency", func(s *Supervisor) {
			s.Add(Component{Name: "a", DependsOn: []string{"a"}, Reconcile: noop})
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New(nil, fastOpts())
			tc.add(s)
			if s.Err() == nil {
				t.Fatal("expected a registration error")
			}
			if err := s.Run(context.Background()); err == nil {
				t.Fatal("Run must refuse an invalid graph")
			}
		})
	}
}

func TestSupervisor_emptyGraphIdlesUntilCancelled(t *testing.T) {
	s := New(nil, fastOpts())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := s.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run returned %v, want context.DeadlineExceeded", err)
	}
}

func TestSupervisor_onChangeFiresWithSnapshot(t *testing.T) {
	var mu sync.Mutex
	var seen []bool

	s := New(nil, fastOpts())
	s.OnChange(func(snap Snapshot) {
		mu.Lock()
		seen = append(seen, snap.AllReady())
		mu.Unlock()
	})
	s.Add(Component{Name: "a", Reconcile: func(context.Context) error { return nil }})

	runUntil(t, s, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) > 0 && seen[len(seen)-1]
	})

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("OnChange never fired")
	}
}

func TestWithTimeout_boundsTheAttemptButNotTheRunContext(t *testing.T) {
	var inner context.Context
	fn := WithTimeout(10*time.Millisecond, func(ctx context.Context) error {
		inner = ctx
		<-ctx.Done()
		return ctx.Err()
	})

	run, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := fn(run)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("attempt error = %v, want DeadlineExceeded", err)
	}
	if inner == nil {
		t.Fatal("wrapped function never ran")
	}
	if run.Err() != nil {
		t.Fatal("the attempt deadline must not cancel the run context")
	}
}

func TestOptions_withDefaults(t *testing.T) {
	got := Options{}.withDefaults()
	if got.BaseBackoff != DefaultBaseBackoff || got.MaxBackoff != DefaultMaxBackoff || got.HealthInterval != DefaultHealthInterval {
		t.Fatalf("zero Options did not take defaults: %+v", got)
	}

	got = Options{BaseBackoff: time.Minute, MaxBackoff: time.Second}.withDefaults()
	if got.MaxBackoff != time.Minute {
		t.Fatalf("MaxBackoff below BaseBackoff should be raised to it, got %s", got.MaxBackoff)
	}
}

// waitForTimeout bounds every wait for the supervisor to reach a state.
const waitForTimeout = 5 * time.Second

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitForTimeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
