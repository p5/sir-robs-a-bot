package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

// Result requests an optional follow-up after application progress is persisted.
// A returned error takes precedence and discards the Result.
type Result struct {
	Again        bool
	After        time.Duration
	ProtectDelay bool
}

// Reconciler inspects application state and makes repeatable progress.
// It must honor cancellation and persist its own progress before returning.
// Queue ownership does not protect application writes or prevent duplicate effects.
type Reconciler interface {
	Reconcile(context.Context, datastore.Key) (Result, error)
}

// ReconcilerFunc adapts a function to the Reconciler interface.
type ReconcilerFunc func(context.Context, datastore.Key) (Result, error)

func (reconcile ReconcilerFunc) Reconcile(ctx context.Context, key datastore.Key) (Result, error) {
	return reconcile(ctx, key)
}

// Config sets per-process dispatch limits and durable retry policy.
// The engine renews leases while a bounded call is active. Cancellation remains
// cooperative; reconcilers must return before ownership can be released safely.
type Config struct {
	Concurrency   int
	LeaseDuration time.Duration
	CallTimeout   time.Duration
	PollInterval  time.Duration
	RetryInitial  time.Duration
	RetryMax      time.Duration
	// MaxFailures includes the initial failed call, not just subsequent retries.
	MaxFailures uint32
	// MaxAbandoned bounds claims recovered after controller loss since the last
	// success or explicit redrive. Zero uses MaxFailures.
	MaxAbandoned uint32
	// Logger receives lifecycle events. Nil uses slog.Default().
	Logger *slog.Logger
	// Metrics optionally collects process-local observations. Nil disables collection.
	Metrics *Metrics
}

func (config Config) validate() error {
	switch {
	case config.Concurrency < 1:
		return errors.New("concurrency must be positive")
	case config.LeaseDuration < 3*time.Nanosecond:
		return errors.New("lease duration must allow a positive renewal interval")
	case config.CallTimeout <= 0:
		return errors.New("call timeout must be positive")
	case config.PollInterval <= 0:
		return errors.New("poll interval must be positive")
	case config.RetryInitial <= 0:
		return errors.New("initial retry delay must be positive")
	case config.RetryMax < config.RetryInitial:
		return errors.New("maximum retry delay must not be shorter than the initial delay")
	case config.MaxFailures < 1:
		return errors.New("failure limit must be positive")
	default:
		return nil
	}
}

// Engine dispatches work from a supplied store. It has no authoritative queue
// or scheduling state. Independent processes can share the same store.
type Engine struct {
	store       datastore.Store
	reconcilers map[string]Reconciler
	kinds       []string
	config      Config
	logger      *slog.Logger
}

// New validates configuration and copies the reconciler registry.
// Callers can change their registry afterward without affecting the engine.
func New(store datastore.Store, reconcilers map[string]Reconciler, config Config) (*Engine, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	if len(reconcilers) == 0 {
		return nil, errors.New("at least one reconciler is required")
	}
	if err := config.validate(); err != nil {
		return nil, fmt.Errorf("invalid engine configuration: %w", err)
	}
	for kind, reconciler := range reconcilers {
		if err := datastore.ValidateKind(kind); err != nil {
			return nil, err
		}
		callback, isFunction := reconciler.(ReconcilerFunc)
		if reconciler == nil || (isFunction && callback == nil) {
			return nil, fmt.Errorf("reconciler for kind %q is required", kind)
		}
	}

	if config.MaxAbandoned == 0 {
		config.MaxAbandoned = config.MaxFailures
	}

	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Engine{
		store:       store,
		reconcilers: maps.Clone(reconcilers),
		kinds:       slices.Sorted(maps.Keys(reconcilers)),
		config:      config,
		logger:      logger,
	}, nil
}

// ReconcileOne handles at most one claimed resource. False means no work was
// due. Reconciler errors are persisted and scheduled; store errors are returned.
// Cancellation releases ownership after the call stops without publishing success.
// Hosts can call this method directly instead of running background workers.
func (engine *Engine) ReconcileOne(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	claimStarted := time.Now()
	claim, err := engine.store.Claim(ctx, engine.kinds, engine.config.LeaseDuration)
	engine.observe("", "claim", claimStarted, err)
	if errors.Is(err, datastore.ErrNoWork) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim due work: %w", err)
	}

	reconciler, registered := engine.reconcilers[claim.Key.Kind]
	if !registered {
		return true, fmt.Errorf("store claimed unregistered kind %q", claim.Key.Kind)
	}

	engine.config.Metrics.changeActive(claim.Key.Kind, 1)
	defer engine.config.Metrics.changeActive(claim.Key.Kind, -1)
	dispatchStarted := time.Now()
	outcome := "interrupted"
	defer func() { engine.config.Metrics.record(claim.Key.Kind, "dispatch", outcome, time.Since(dispatchStarted)) }()
	logger := engine.claimLogger(claim)
	started := time.Now()
	logger.InfoContext(ctx, "reconciliation claimed", "event", "claimed")
	var result Result
	var reconcileErr, renewalErr error
	if claim.Abandoned >= engine.config.MaxAbandoned {
		reconcileErr = Permanent(errors.New("abandoned claim budget exhausted"))
	} else {
		result, reconcileErr, renewalErr = engine.reconcileWithLease(ctx, reconciler, claim)
	}
	if err := ctx.Err(); err != nil {
		// The call has stopped. Release with a bounded cleanup context so orderly
		// shutdown does not consume the abandoned-claim budget. If release is
		// uncertain, recovery still occurs through expiry and fencing.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), engine.config.LeaseDuration/3)
		releaseStarted := time.Now()
		releaseErr := engine.store.Release(cleanup, claim)
		engine.observe(claim.Key.Kind, "release", releaseStarted, releaseErr)
		cancel()
		if releaseErr != nil && !errors.Is(releaseErr, datastore.ErrLeaseLost) {
			logger.ErrorContext(ctx, "interrupted claim release failed", "event", "release_failed", "error", releaseErr)
			return true, errors.Join(err, releaseErr)
		}
		logger.InfoContext(ctx, "reconciliation interrupted", "event", "interrupted", "duration", time.Since(started), "error", err)
		return true, err
	}

	if renewalErr != nil {
		outcome = "renewal_error"
		logger.WarnContext(ctx, "claim renewal failed", "event", "renewal_failed", "error", renewalErr)
		return true, fmt.Errorf("renew reconciliation claim: %w", renewalErr)
	}
	completion := engine.completionFor(claim, result, reconcileErr)
	commitStarted := time.Now()
	commitErr := engine.store.Commit(ctx, claim, completion)
	engine.observe(claim.Key.Kind, "commit", commitStarted, commitErr)
	if err := commitErr; err != nil {
		outcome = "commit_error"
		if errors.Is(err, datastore.ErrLeaseLost) {
			logger.WarnContext(ctx, "reconciliation lost ownership", "event", "lease_lost", "duration", time.Since(started))
		} else {
			logger.ErrorContext(ctx, "reconciliation commit failed", "event", "commit_failed", "duration", time.Since(started), "error", err)
		}
		return true, fmt.Errorf("commit reconciliation of %s/%s: %w", claim.Key.Kind, claim.Key.ID, err)
	}
	outcome = "completed"
	if completion.Stop {
		outcome = "suspended"
	} else if completion.Failure != "" {
		outcome = "retried"
	}
	logCompletion(ctx, logger, completion, time.Since(started))
	return true, nil
}

func (engine *Engine) reconcile(ctx context.Context, reconciler Reconciler, claim datastore.Claim) (Result, error) {
	started := time.Now()
	result, err := reconciler.Reconcile(ctx, claim.Key)
	engine.observe(claim.Key.Kind, "callback", started, err)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		// A reconciler can return success after its deadline. Do not publish it.
		return Result{}, err
	}
	if result.ProtectDelay && (!result.Again || result.After <= 0) {
		return Result{}, errors.New("protected delay requires a positive follow-up delay")
	}
	if result.After < 0 {
		return Result{}, errors.New("reconciliation delay must not be negative")
	}
	if !result.Again && result.After != 0 {
		return Result{}, errors.New("reconciliation delay requires a follow-up request")
	}
	return result, nil
}

func (engine *Engine) completionFor(claim datastore.Claim, result Result, reconcileErr error) datastore.Completion {
	if reconcileErr == nil {
		return datastore.Completion{
			Again:        result.Again,
			After:        result.After,
			ProtectDelay: result.ProtectDelay,
		}
	}

	_, permanent := errors.AsType[*permanentError](reconcileErr)
	completion := datastore.Completion{
		Failure: formatFailure(reconcileErr),
		Stop:    permanent || claim.Failures >= engine.config.MaxFailures-1,
	}
	if !completion.Stop {
		completion.Again = true
		completion.After = engine.retryDelay(claim.Failures)
		completion.ProtectDelay = true
	}
	return completion
}

// Run polls durable due work with Config.Concurrency workers. A store error
// cancels the workers and is returned to the host. Lease loss discards that
// completion and allows workers to continue. Shutdown waits for active
// reconcilers to return. It does not terminate external work.
// The concurrency limit applies to this Run invocation, not the whole cluster.
func (engine *Engine) Run(ctx context.Context) error {
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()

	firstError := make(chan error, 1)
	var workers sync.WaitGroup
	for range engine.config.Concurrency {
		workers.Go(func() {
			if err := engine.runWorker(workerCtx); err != nil {
				select {
				case firstError <- err:
				default:
				}
				cancelWorkers()
			}
		})
	}
	workers.Wait()

	select {
	case err := <-firstError:
		return err
	default:
		return workerCtx.Err()
	}
}

func (engine *Engine) runWorker(ctx context.Context) error {
	for ctx.Err() == nil {
		worked, err := engine.ReconcileOne(ctx)
		if err != nil && !errors.Is(err, datastore.ErrLeaseLost) {
			return err
		}
		if worked {
			continue
		}

		timer := time.NewTimer(engine.config.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}
