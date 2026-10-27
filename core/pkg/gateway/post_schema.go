package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Orama-Network/orama/core/pkg/logging"
	"go.uber.org/zap"
)

// Start-up work that needs the schema's tables.
//
// All of it used to run while the gateway was being built, before the
// readiness loop had applied the schema. On a fresh cluster the tables did not
// exist yet, so each piece failed and logged a warning: the signing key was
// never published (every other gateway refused this one's tokens until a
// restart), the pubsub trigger dispatcher never subscribed, the push token_fp
// backfill gave up after a timeout, and the cron scheduler logged a failed
// ListDue every tick until the migrations landed. The plaintext-key migration
// was worse: it failed the start-up outright, a crash loop only a won race
// avoided.
//
// It runs once the schema is up, in two groups:
//
//   - gating steps (postSchemaSteps) run inside the readiness loop. A failure
//     keeps the gateway not ready — it refuses traffic, so nothing is minted
//     with an unpublished key — and is retried with the schema.
//   - after-ready steps (afterReadySteps) are housekeeping and background
//     services whose failure must not take every route down with it: one
//     writes to the core registry from a namespace gateway, one can walk many
//     rows. They run once the gateway is ready, each on its own, and are
//     retried with backoff until they succeed, each failure logged as an error.

// postSchemaAttemptTimeout bounds one pass over a group of steps. The context
// they would otherwise get lives as long as the gateway, so one hung query
// would stall readiness for good. The old background backfill had the same cap.
const postSchemaAttemptTimeout = 5 * time.Minute

// postSchemaStep is one piece of that work.
type postSchemaStep struct {
	name string
	run  func(context.Context) error
}

// runPostSchemaSteps runs the steps in order and stops at the first failure,
// naming the step.
func runPostSchemaSteps(ctx context.Context, steps []postSchemaStep) error {
	for _, s := range steps {
		if err := s.run(ctx); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	return nil
}

// onceSucceeded runs start until it succeeds, then never again.
//
// A failed attempt is retried, and a start step spawns a goroutine that must
// not be spawned twice. Each step's attempts run on one goroutine, so the flag
// needs no lock.
func onceSucceeded(start func(context.Context) error) func(context.Context) error {
	done := false
	return func(ctx context.Context) error {
		if done {
			return nil
		}
		if err := start(ctx); err != nil {
			return err
		}
		done = true
		return nil
	}
}

// tokenFPBackfiller is the push device store's backfill of token_fp (migration
// 033's column) for rows registered before it existed.
type tokenFPBackfiller interface {
	BackfillTokenFP(ctx context.Context) (int, error)
}

// postSchemaSteps is the work that gates readiness, in order. Every step is
// idempotent: a failure repeats the whole sequence.
func (g *Gateway) postSchemaSteps(cfg *Config, deps *Dependencies) []postSchemaStep {
	steps := []postSchemaStep{{
		// Before anything is minted: a token signed with an unpublished key
		// is refused by every other gateway.
		name: "publish this gateway's signing key",
		run:  deps.AuthService.PublishSigningKey,
	}}

	// A namespace gateway's own database is not the key registry, and the
	// api_keys rows in it are leftovers — the pre-#163 ones being raw
	// credentials the tenant can read. They are removed here, on the only
	// gateways where the local database is provably not the registry.
	if usesSeparateAPIKeyRegistry(cfg) && g.client != nil {
		steps = append(steps, postSchemaStep{
			name: "remove plaintext API keys from this namespace's own database",
			run: func(ctx context.Context) error {
				return g.logCount(ctx, "Removed leftover plaintext API keys from this namespace's database",
					func(ctx context.Context) (int, error) { return purgeTenantPlaintextAPIKeys(ctx, g.client.Database()) })
			},
		})
	}
	steps = append(steps, postSchemaStep{
		name: "hash plaintext API keys",
		run: func(ctx context.Context) error {
			return g.logCount(ctx, "Hashed leftover plaintext API keys", deps.AuthService.MigratePlaintextAPIKeys)
		},
	})
	return steps
}

// afterReadySteps is the work that runs once the gateway is ready. Each step
// runs independently of the others (runAfterReady).
// life is the gateway's lifetime: the cron scheduler's loop runs under it, not
// under one attempt's timeout.
func (g *Gateway) afterReadySteps(life context.Context, cfg *Config, deps *Dependencies) []postSchemaStep {
	steps := []postSchemaStep{{
		name: "revoke API keys of deleted namespaces",
		run: func(ctx context.Context) error {
			return g.logCount(ctx, "Revoked orphaned API keys", deps.AuthService.RevokeOrphanedAPIKeys)
		},
	}}
	// The index gateways' unbound keys: stamp this one while it runs, and
	// retire the ones no gateway has stamped for longer than any token they
	// signed could still be valid. A namespace gateway's key is bound and is
	// not this gateway's to retire.
	if !servesNamedNamespace(cfg.ClientNamespace) {
		steps = append(steps, postSchemaStep{
			name: "retire unused index signing keys",
			run: onceSucceeded(func(ctx context.Context) error {
				if err := g.logCount(ctx, "Retired index signing keys no gateway has used for a day",
					deps.AuthService.RetireUnusedIndexKeys); err != nil {
					return err
				}
				deps.AuthService.StartSigningKeyHeartbeat(life)
				return nil
			}),
		})
	}
	if b, ok := deps.PushDeviceStore.(tokenFPBackfiller); ok {
		steps = append(steps, postSchemaStep{
			name: "backfill push token fingerprints",
			run: func(ctx context.Context) error {
				return g.logCount(ctx, "Backfilled push token fingerprints", b.BackfillTokenFP)
			},
		})
	}

	// The storage reference backfill reads this namespace's
	// ipfs_content_ownership and the registry's ipfs_cid_refs, so it cannot
	// start before the schema exists: started at construction it logged
	// "no such table" at ERROR on every new namespace gateway until the first
	// migration pass finished. Unpins stay refused until it completes
	// (HoldUntilCIDRefBackfill, called where the handlers are built).
	if g.storageHandlers != nil {
		steps = append(steps, postSchemaStep{
			name: "start the storage reference backfill",
			run: onceSucceeded(func(context.Context) error {
				g.storageHandlers.StartCIDRefBackfill(life, ownNamespace(cfg))
				return nil
			}),
		})
	}

	// Start steps, last and once each. The dispatcher subscribes to libp2p
	// pubsub for every literal trigger pattern in function_pubsub_triggers,
	// so WASM PubSubPublish calls reach trigger handlers (bugboard #282);
	// until it has, only HTTP-published events fire triggers.
	if g.pubsubDispatcher != nil {
		steps = append(steps, postSchemaStep{
			name: "start the pubsub trigger dispatcher",
			run:  onceSucceeded(g.pubsubDispatcher.Start),
		})
	}
	// It lists the namespace's functions table, so it starts once the schema
	// exists. StartPrewarm does nothing on a gateway that serves no single
	// namespace or keeps no compiled code on disk (bug-963).
	if g.serverlessEngine != nil && isNamespaceGateway(cfg) {
		steps = append(steps, postSchemaStep{
			name: "start compiling the namespace's functions ahead of their first call",
			run: onceSucceeded(func(context.Context) error {
				g.serverlessEngine.StartPrewarm(cfg.ClientNamespace)
				return nil
			}),
		})
	}
	if g.cronScheduler != nil {
		steps = append(steps, postSchemaStep{
			name: "start the cron scheduler",
			run: onceSucceeded(func(context.Context) error {
				g.cronScheduler.Start(life)
				return nil
			}),
		})
	}
	return steps
}

// errPostSchemaStep marks a failure of a readiness-gating step, so readiness
// reports it as ReasonPostSchema rather than as a schema failure.
var errPostSchemaStep = errors.New("start-up work that gates readiness failed")

// runGatingSteps runs the readiness-gating steps once. A failure is marked
// errPostSchemaStep; the gateway stays not ready and the readiness loop
// retries it with the schema.
func runGatingSteps(ctx context.Context, steps []postSchemaStep) error {
	if err := runAttempt(ctx, steps); err != nil {
		return fmt.Errorf("%w: %w", errPostSchemaStep, err)
	}
	return nil
}

// runAttempt runs steps once, bounded by postSchemaAttemptTimeout.
func runAttempt(ctx context.Context, steps []postSchemaStep) error {
	attemptCtx, cancel := context.WithTimeout(ctx, postSchemaAttemptTimeout)
	defer cancel()
	return runPostSchemaSteps(attemptCtx, steps)
}

// runAfterReady waits for the gateway to become ready, then runs each step on
// its own until it succeeds or ctx ends. The steps are independent: a
// housekeeping write that keeps failing must not keep the trigger dispatcher
// or the cron scheduler from starting.
func (g *Gateway) runAfterReady(ctx context.Context, steps []postSchemaStep) {
	if !g.ready.waitReady(ctx) {
		return
	}
	var wg sync.WaitGroup
	for _, step := range steps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.retryStep(ctx, step)
		}()
	}
	wg.Wait()
}

// retryStep runs one step until it succeeds or ctx ends, backing off between
// attempts like the schema loop does, and logs every failure as an error.
func (g *Gateway) retryStep(ctx context.Context, step postSchemaStep) {
	backoff := schemaRetryBaseBackoff
	for attempt := 1; ; attempt++ {
		err := runAttempt(ctx, []postSchemaStep{step})
		if err == nil {
			return
		}
		g.logger.ComponentError(logging.ComponentGeneral, "Post-start work failed; retrying",
			zap.Int("attempt", attempt), zap.Duration("retry_in", backoff), zap.Error(err))

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > schemaRetryMaxBackoff {
			backoff = schemaRetryMaxBackoff
		}
	}
}

// logCount runs a step that reports how many rows it changed, and logs the
// count when there were any.
func (g *Gateway) logCount(ctx context.Context, msg string, run func(context.Context) (int, error)) error {
	n, err := run(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		g.logger.ComponentInfo(logging.ComponentGeneral, msg, zap.Int("count", n))
	}
	return nil
}
