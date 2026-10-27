package serverless

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// nopCommandWASM is a command module whose _start does nothing. tag goes into
// a custom section, so each tag is a distinct module with its own compile and
// its own compilation cache entry.
func nopCommandWASM(tag string) []byte {
	b := []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
		0x03, 0x02, 0x01, 0x00,
		0x07, 0x0a, 0x01, 0x06, 0x5f, 0x73, 0x74, 0x61, 0x72, 0x74, 0x00, 0x00,
		0x0a, 0x04, 0x01, 0x02, 0x00, 0x0b,
	}
	// Custom section 0: size, the name "t" as a byte vector, then tag as the
	// section's payload.
	return append(b, append([]byte{0x00, byte(2 + len(tag)), 0x01, 't'}, tag...)...)
}

// cacheEntries lists the committed entries under the compilation cache.
func cacheEntries(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && !strings.HasSuffix(d.Name(), compileCacheTempSuffix) {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func waitForEntries(t *testing.T, dir string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := len(cacheEntries(t, dir))
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("compilation cache holds %d entries, want %d", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func prewarmEngine(t *testing.T, dir string) (*Engine, *MockRegistry) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.CompilationCacheDir = dir
	registry := NewMockRegistry()
	engine, err := NewEngine(cfg, registry, NewMockHostServices(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close(context.Background()) })
	return engine, registry
}

func register(t *testing.T, r *MockRegistry, ns, name string) *Function {
	t.Helper()
	def := &FunctionDefinition{Name: name, Namespace: ns, MemoryLimitMB: 64, TimeoutSeconds: 5}
	if _, err := r.Register(context.Background(), def, nopCommandWASM(ns+"/"+name)); err != nil {
		t.Fatal(err)
	}
	fn, err := r.Get(context.Background(), ns, name, 0)
	if err != nil {
		t.Fatal(err)
	}
	return fn
}

// TestPrewarm_compilesTheNamespaceToDiskNotMemory: the served namespace's
// functions are compiled before any call, into the on-disk cache only (a
// warm-up loads nothing into the in-memory module cache); another namespace's
// are not; a deploy kicks a sweep without waiting for the interval.
func TestPrewarm_compilesTheNamespaceToDiskNotMemory(t *testing.T) {
	dir := t.TempDir()
	engine, registry := prewarmEngine(t, dir)
	fn := register(t, registry, "ns", "a")
	register(t, registry, "ns", "b")
	register(t, registry, "other", "c")

	engine.StartPrewarm("ns")
	waitForEntries(t, dir, 2)
	if n := engine.moduleCache.Size(); n != 0 {
		t.Errorf("the warm-up loaded %d modules into memory, want 0", n)
	}

	register(t, registry, "ns", "deployed-later")
	engine.KickPrewarm()
	waitForEntries(t, dir, 3)

	if _, err := engine.Execute(context.Background(), fn, nil, nil); err != nil {
		t.Fatalf("a prewarmed function did not run: %v", err)
	}
	if n := engine.moduleCache.Size(); n != 1 {
		t.Errorf("after one call %d modules are in memory, want 1", n)
	}
}

// TestPrewarm_needsTheDiskCache: without a compilation cache directory the
// warm-up would have to hold every module in memory, so it does not start.
func TestPrewarm_needsTheDiskCache(t *testing.T) {
	engine, registry := prewarmEngine(t, "")
	register(t, registry, "ns", "a")
	engine.StartPrewarm("ns")
	if engine.prewarmer.Load() != nil {
		t.Fatal("prewarm started without a compilation cache directory")
	}
	engine.KickPrewarm() // a deploy on such a gateway is a no-op
}

// TestPrewarm_pruneEmptiesACacheOfReplacedVersions: past compileCacheSlack
// entries per current module the cache is emptied (and refilled by the sweep);
// an entry wazero is still writing is never removed.
func TestPrewarm_pruneEmptiesACacheOfReplacedVersions(t *testing.T) {
	dir := t.TempDir()
	engine, _ := prewarmEngine(t, dir)
	p := &prewarmer{engine: engine, logger: zap.NewNop()}
	sub := filepath.Join(dir, "wazero-test")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(sub, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("aa")
	write("bb")
	if p.pruneCompilationCache(1) {
		t.Fatal("pruned at exactly the slack (2 entries, 1 current module)")
	}
	write("cc")
	write("dd" + ".123" + compileCacheTempSuffix)
	if !p.pruneCompilationCache(1) {
		t.Fatal("3 entries for 1 current module were not pruned")
	}
	if left := cacheEntries(t, dir); len(left) != 0 {
		t.Errorf("entries left after the prune: %v", left)
	}
	if _, err := os.Stat(filepath.Join(sub, "dd.123"+compileCacheTempSuffix)); err != nil {
		t.Errorf("an entry still being written was removed: %v", err)
	}
}

// TestPrewarm_sweepRefillsAfterAPrune: a sweep that pruned compiles the
// current set again, so what the namespace runs is back on disk.
func TestPrewarm_sweepRefillsAfterAPrune(t *testing.T) {
	dir := t.TempDir()
	engine, registry := prewarmEngine(t, dir)
	register(t, registry, "ns", "a")
	engine.StartPrewarm("ns")
	waitForEntries(t, dir, 1)

	// Two replaced versions' entries make three for one current module.
	stale := filepath.Dir(cacheEntries(t, dir)[0])
	for _, name := range []string{"stale1", "stale2"} {
		if err := os.WriteFile(filepath.Join(stale, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	engine.KickPrewarm()
	deadline := time.Now().Add(5 * time.Second)
	for {
		left := cacheEntries(t, dir)
		if len(left) == 1 && !strings.HasPrefix(filepath.Base(left[0]), "stale") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the prune the cache holds %v, want only the current module", left)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
