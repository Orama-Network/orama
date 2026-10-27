package serverless

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Compiling ahead of the first call (bug-963).
//
// Compiling a TinyGo module with wazero takes the better part of a second on a
// laptop and one to two seconds on a node; reading the compiled code back from
// the on-disk compilation cache takes tens of milliseconds. Without this, the
// first call of every function on every gateway paid the compile, and a
// namespace with more functions than the module cache holds paid it again for
// every function the LRU had evicted.
//
// A namespace gateway therefore keeps the compiled code of every current
// version of its namespace's functions on disk (Config.CompilationCacheDir; a
// call loads a module into memory, the warm-up does not): it lists them when
// it starts, again
// every prewarmInterval (so a deploy made through another gateway reaches it
// within that interval), and at once when a deploy lands on it. It compiles
// what it has not compiled yet, one module at a time — wazero recommends
// compiling from a single goroutine, and the warm-up must not take the cores
// the invocations need.

const (
	// prewarmInterval is how often a gateway looks for functions of its
	// namespace it has not compiled. The list is one indexed read of the
	// namespace's functions table; the interval bounds how long a deploy made
	// on another gateway can leave this one cold.
	prewarmInterval = 15 * time.Second

	// prewarmSweepTimeout bounds one sweep, so a stalled registry or IPFS read
	// cannot hold the warm-up forever. A sweep that runs out resumes from what
	// it had not compiled on the next tick.
	prewarmSweepTimeout = 10 * time.Minute

	// compileCacheSlack is how many on-disk entries per current module the
	// compilation cache may hold before it is emptied and refilled. Entries of
	// replaced versions accumulate with every deploy, and wazero never removes
	// one; at twice the current set the dead entries are at least half of it.
	compileCacheSlack = 2

	// compileCacheTempSuffix marks an entry wazero is still writing
	// (os.CreateTemp, then a rename). Pruning must leave it: removing it fails
	// the rename, and with it the compile.
	compileCacheTempSuffix = ".tmp"
)

// prewarmer is the background loop that keeps the served namespace compiled.
type prewarmer struct {
	engine    *Engine
	namespace string
	logger    *zap.Logger

	kick   chan struct{}
	cancel context.CancelFunc
	done   chan struct{}

	// attempted is every WASM CID a sweep has compiled or tried to: a CID
	// names immutable bytes, so one attempt is enough, and a module that
	// fails to compile is not retried every interval. Only the loop goroutine
	// touches it.
	attempted map[string]struct{}
}

// StartPrewarm starts compiling namespace's functions ahead of their first
// call. It does nothing when prewarming is disabled, when the engine has no
// on-disk compilation cache (the compiled code is kept there, not in memory),
// when namespace is empty (a gateway that serves no single namespace has no
// list to warm), or when it was already started.
func (e *Engine) StartPrewarm(namespace string) {
	namespace = strings.TrimSpace(namespace)
	if !e.config.EnablePrewarm || e.compilationCache == nil || namespace == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &prewarmer{
		engine:    e,
		namespace: namespace,
		logger:    e.logger.With(zap.String("namespace", namespace)),
		kick:      make(chan struct{}, 1),
		cancel:    cancel,
		done:      make(chan struct{}),
		attempted: make(map[string]struct{}),
	}
	if !e.prewarmer.CompareAndSwap(nil, p) {
		cancel()
		return
	}
	go p.run(ctx)
}

// KickPrewarm asks for a sweep now, e.g. right after a deploy landed on this
// gateway. It never blocks; a sweep already asked for absorbs the request.
func (e *Engine) KickPrewarm() {
	p := e.prewarmer.Load()
	if p == nil {
		return
	}
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

func (p *prewarmer) run(ctx context.Context) {
	defer close(p.done)
	ticker := time.NewTicker(prewarmInterval)
	defer ticker.Stop()
	for {
		p.sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-p.kick:
		}
	}
}

// stop ends the loop and waits for the compile in progress to return.
func (p *prewarmer) stop() {
	p.cancel()
	<-p.done
}

// sweep compiles every current function of the namespace not attempted yet.
func (p *prewarmer) sweep(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, prewarmSweepTimeout)
	defer cancel()

	fns, err := p.engine.registry.List(ctx, p.namespace)
	if err != nil {
		if parent.Err() == nil {
			p.logger.Warn("prewarm: listing the namespace's functions failed", zap.Error(err))
		}
		return
	}
	current := make(map[string]struct{}, len(fns))
	for _, fn := range fns {
		if fn.WASMCID != "" {
			current[fn.WASMCID] = struct{}{}
		}
	}
	for cid := range p.attempted {
		if _, ok := current[cid]; !ok {
			delete(p.attempted, cid)
		}
	}
	if p.pruneCompilationCache(len(current)) {
		// What was compiled to disk is gone: compile the current set again.
		clear(p.attempted)
	}

	compiled := 0
	start := time.Now()
	for cid := range current {
		if ctx.Err() != nil {
			return
		}
		if _, done := p.attempted[cid]; done {
			continue
		}
		wasm, err := p.engine.registry.GetWASMBytes(ctx, cid)
		if err == nil {
			err = p.engine.Precompile(ctx, cid, wasm)
		}
		if err != nil {
			if ctx.Err() != nil {
				// Cut off, not failed: the next sweep tries it again.
				return
			}
			p.logger.Warn("prewarm: compiling a function ahead of its first call failed; its first call compiles it",
				zap.String("wasm_cid", cid), zap.Error(err))
		} else {
			compiled++
		}
		p.attempted[cid] = struct{}{}
	}
	if compiled > 0 {
		p.logger.Info("prewarm: compiled functions ahead of their first call",
			zap.Int("compiled", compiled), zap.Int("current", len(current)),
			zap.Duration("took", time.Since(start)))
	}
}

// pruneCompilationCache empties the on-disk compilation cache when it holds
// more than compileCacheSlack entries per current module, and reports whether
// it did. wazero offers no way to map an entry back to its module, so the dead
// entries cannot be picked out; emptying the cache and compiling the current
// set again bounds the directory to a small multiple of what the namespace
// runs. An entry still being written is left alone (see
// compileCacheTempSuffix).
func (p *prewarmer) pruneCompilationCache(currentModules int) bool {
	dir := p.engine.config.CompilationCacheDir
	if dir == "" {
		return false
	}
	var entries []string
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && !strings.HasSuffix(d.Name(), compileCacheTempSuffix) {
			entries = append(entries, path)
		}
		return nil
	})
	if walkErr != nil {
		p.logger.Warn("prewarm: reading the compilation cache failed", zap.String("dir", dir), zap.Error(walkErr))
		return false
	}
	if len(entries) <= compileCacheSlack*currentModules {
		return false
	}
	removed := 0
	for _, path := range entries {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			p.logger.Warn("prewarm: removing a compilation cache entry failed", zap.String("path", path), zap.Error(err))
			continue
		}
		removed++
	}
	p.logger.Info("prewarm: emptied the compilation cache of replaced versions",
		zap.Int("entries", len(entries)), zap.Int("removed", removed), zap.Int("current_modules", currentModules))
	return true
}
