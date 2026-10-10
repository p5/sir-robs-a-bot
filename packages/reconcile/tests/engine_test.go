package reconciletests

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/storetest"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/internal/teststore"
)

func testConfig() reconcile.Config {
	return reconcile.Config{
		Concurrency:   2,
		LeaseDuration: time.Minute,
		CallTimeout:   30 * time.Second,
		PollInterval:  time.Millisecond,
		RetryInitial:  time.Second,
		RetryMax:      4 * time.Second,
		MaxFailures:   3,
	}
}

func newTestEngine(t *testing.T, store datastore.Store, reconciler reconcile.Reconciler) *reconcile.Engine {
	t.Helper()
	engine, err := reconcile.New(store, map[string]reconcile.Reconciler{"fixture": reconciler}, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func enqueueKey(t *testing.T, store datastore.Store, id string) datastore.Key {
	t.Helper()
	key := datastore.Key{Kind: "fixture", ID: id}
	if err := store.Enqueue(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	return key
}

func reconcileOne(t *testing.T, engine *reconcile.Engine, want bool) {
	t.Helper()
	got, err := engine.ReconcileOne(t.Context())
	if err != nil || got != want {
		t.Fatalf("worked=%v error=%v", got, err)
	}
}

func TestInspectionContract(t *testing.T) {
	storetest.RunInspection(t, func(t *testing.T) storetest.Fixture {
		store := teststore.New()
		return storetest.Fixture{Store: store, Elapse: store.Advance, Reopen: func() datastore.Store { return store }}
	})
}

func TestStoreContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) storetest.Fixture {
		store := teststore.New()
		return storetest.Fixture{Store: store, Elapse: store.Advance, Reopen: func() datastore.Store { return store }}
	})
}

func TestBoundedRetries(t *testing.T) {
	store := teststore.New()
	key := enqueueKey(t, store, "one")
	engine := newTestEngine(t, store, reconcile.ReconcilerFunc(func(context.Context, datastore.Key) (reconcile.Result, error) {
		return reconcile.Result{}, errors.New("broken")
	}))
	reconcileOne(t, engine, true)
	reconcileOne(t, engine, false)
	store.Advance(time.Second)
	reconcileOne(t, engine, true)
	reconcileOne(t, engine, false)
	store.Advance(2 * time.Second)
	reconcileOne(t, engine, true)
	store.Advance(time.Hour)
	reconcileOne(t, engine, false)
	got, err := store.Get(t.Context(), key)
	if err != nil || got.Failures != 3 || got.Pending || got.LastError == "" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCancellationAbandonsClaim(t *testing.T) {
	store := teststore.New()
	enqueueKey(t, store, "one")
	ctx, cancel := context.WithCancel(t.Context())
	engine := newTestEngine(t, store, reconcile.ReconcilerFunc(func(ctx context.Context, key datastore.Key) (reconcile.Result, error) {
		cancel()
		return reconcile.Result{}, ctx.Err()
	}))
	if _, err := engine.ReconcileOne(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	store.Advance(time.Minute)
	claim, err := store.Claim(t.Context(), []string{"fixture"}, time.Minute)
	if err != nil {
		t.Fatalf("%+v %v", claim, err)
	}
}

func TestLeaseLossRejectsCompletion(t *testing.T) {
	store := teststore.New()
	key := enqueueKey(t, store, "one")
	engine := newTestEngine(t, store, reconcile.ReconcilerFunc(func(context.Context, datastore.Key) (reconcile.Result, error) {
		store.Advance(time.Minute)
		if _, err := store.Claim(t.Context(), []string{"fixture"}, time.Minute); err != nil {
			t.Fatal(err)
		}
		return reconcile.Result{}, nil
	}))
	if _, err := engine.ReconcileOne(t.Context()); !errors.Is(err, datastore.ErrLeaseLost) {
		t.Fatal(err)
	}
	got, err := store.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestConcurrentEnginesRetainWake(t *testing.T) {
	store := teststore.New()
	key := enqueueKey(t, store, "one")
	started := make(chan struct{})
	finish := make(chan struct{})
	var calls atomic.Int32
	reconciler := reconcile.ReconcilerFunc(func(ctx context.Context, key datastore.Key) (reconcile.Result, error) {
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-finish:
			case <-ctx.Done():
				return reconcile.Result{}, ctx.Err()
			}
		}
		return reconcile.Result{}, nil
	})
	firstEngine, secondEngine := newTestEngine(t, store, reconciler), newTestEngine(t, store, reconciler)
	done := make(chan error, 1)
	go func() {
		_, err := firstEngine.ReconcileOne(t.Context())
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("call did not start")
	}
	reconcileOne(t, secondEngine, false)
	for range 8 {
		if err := store.Enqueue(t.Context(), key); err != nil {
			t.Fatal(err)
		}
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	reconcileOne(t, secondEngine, true)
	reconcileOne(t, firstEngine, false)
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
}

func TestRunConcurrencyAndShutdown(t *testing.T) {
	store := teststore.New()
	for _, id := range []string{"one", "two", "three"} {
		enqueueKey(t, store, id)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{}, 3)
	var active atomic.Int32
	reconciler := reconcile.ReconcilerFunc(func(ctx context.Context, key datastore.Key) (reconcile.Result, error) {
		active.Add(1)
		defer active.Add(-1)
		entered <- struct{}{}
		<-ctx.Done()
		return reconcile.Result{}, ctx.Err()
	})
	engine := newTestEngine(t, store, reconciler)
	done := make(chan error, 1)
	go func() { done <- engine.Run(ctx) }()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	if active.Load() != 2 {
		t.Fatalf("active calls = %d, want 2", active.Load())
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	if active.Load() != 0 || len(entered) != 0 {
		t.Fatalf("shutdown left %d active calls and %d extra starts", active.Load(), len(entered))
	}
}

func TestInvalidConfiguration(t *testing.T) {
	reconciler := map[string]reconcile.Reconciler{"fixture": reconcile.ReconcilerFunc(func(context.Context, datastore.Key) (reconcile.Result, error) { return reconcile.Result{}, nil })}
	cases := []reconcile.Config{testConfig(), testConfig(), testConfig()}
	cases[0].Concurrency = 0
	cases[1].CallTimeout = 0
	cases[2].MaxFailures = 0
	for _, config := range cases {
		if _, err := reconcile.New(teststore.New(), reconciler, config); err == nil {
			t.Fatal("accepted invalid config")
		}
	}
}

func TestParallelConsumers(t *testing.T) {
	store := teststore.New()
	for _, id := range []string{"one", "two"} {
		enqueueKey(t, store, id)
	}
	var count atomic.Int32
	reconciler := reconcile.ReconcilerFunc(func(context.Context, datastore.Key) (reconcile.Result, error) {
		count.Add(1)
		return reconcile.Result{}, nil
	})
	engines := []*reconcile.Engine{newTestEngine(t, store, reconciler), newTestEngine(t, store, reconciler)}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, engine := range engines {
		wg.Go(func() {
			worked, err := engine.ReconcileOne(t.Context())
			if !worked && err == nil {
				err = errors.New("consumer found no work")
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count.Load() != 2 {
		t.Fatal(count.Load())
	}
}

func TestSuccessResetsFailures(t *testing.T) {
	store := teststore.New()
	key := enqueueKey(t, store, "one")
	calls := 0
	engine := newTestEngine(t, store, reconcile.ReconcilerFunc(func(context.Context, datastore.Key) (reconcile.Result, error) {
		calls++
		if calls == 1 {
			return reconcile.Result{}, errors.New("temporary")
		}
		return reconcile.Result{}, nil
	}))
	reconcileOne(t, engine, true)
	store.Advance(time.Second)
	reconcileOne(t, engine, true)
	resource, err := store.Get(t.Context(), key)
	if err != nil || resource.Failures != 0 || resource.LastError != "" {
		t.Fatalf("%+v %v", resource, err)
	}
}

func TestCallTimeoutPersistsFailure(t *testing.T) {
	store := teststore.New()
	key := enqueueKey(t, store, "one")
	config := testConfig()
	config.CallTimeout = time.Nanosecond
	engine, err := reconcile.New(store, map[string]reconcile.Reconciler{"fixture": reconcile.ReconcilerFunc(func(ctx context.Context, key datastore.Key) (reconcile.Result, error) {
		<-ctx.Done()
		return reconcile.Result{}, nil
	})}, config)
	if err != nil {
		t.Fatal(err)
	}
	reconcileOne(t, engine, true)
	resource, err := store.Get(t.Context(), key)
	if err != nil || resource.Failures != 1 || !resource.Pending {
		t.Fatalf("%+v %v", resource, err)
	}
}

type recordingStore struct {
	datastore.Store
	completion datastore.Completion
}

func (store *recordingStore) Commit(ctx context.Context, claim datastore.Claim, completion datastore.Completion) error {
	store.completion = completion
	return store.Store.Commit(ctx, claim, completion)
}

func TestRetryDelayCaps(t *testing.T) {
	model := teststore.New()
	store := &recordingStore{Store: model}
	enqueueKey(t, store, "one")
	config := testConfig()
	config.MaxFailures = 6
	engine, err := reconcile.New(store, map[string]reconcile.Reconciler{"fixture": reconcile.ReconcilerFunc(func(context.Context, datastore.Key) (reconcile.Result, error) {
		return reconcile.Result{}, errors.New("temporary")
	})}, config)
	if err != nil {
		t.Fatal(err)
	}
	for _, ceiling := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second, 4 * time.Second} {
		reconcileOne(t, engine, true)
		delay := store.completion.After
		if delay < ceiling/2 || delay > ceiling {
			t.Fatalf("retry delay %v outside [%v, %v]", delay, ceiling/2, ceiling)
		}
		model.Advance(delay - time.Nanosecond)
		reconcileOne(t, engine, false)
		model.Advance(time.Nanosecond)
	}
	reconcileOne(t, engine, true)
	model.Advance(time.Hour)
	reconcileOne(t, engine, false)
}

func TestInvalidResultCannotSettle(t *testing.T) {
	store := teststore.New()
	key := enqueueKey(t, store, "one")
	engine := newTestEngine(t, store, reconcile.ReconcilerFunc(func(context.Context, datastore.Key) (reconcile.Result, error) {
		return reconcile.Result{After: time.Second}, nil
	}))
	reconcileOne(t, engine, true)
	resource, err := store.Get(t.Context(), key)
	if err != nil || resource.Failures != 1 || !resource.Pending {
		t.Fatalf("%+v %v", resource, err)
	}
}

type uncertainStore struct {
	datastore.Store
	failure error
}

func (store uncertainStore) Commit(ctx context.Context, claim datastore.Claim, out datastore.Completion) error {
	if err := store.Store.Commit(ctx, claim, out); err != nil {
		return err
	}
	return store.failure
}

func TestAmbiguousCommitDoesNotReplay(t *testing.T) {
	store := teststore.New()
	enqueueKey(t, store, "one")
	failure := errors.New("reply lost")
	calls := 0
	reconciler := reconcile.ReconcilerFunc(func(context.Context, datastore.Key) (reconcile.Result, error) {
		calls++
		return reconcile.Result{}, nil
	})
	if _, err := newTestEngine(t, uncertainStore{Store: store, failure: failure}, reconciler).ReconcileOne(t.Context()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	reconcileOne(t, newTestEngine(t, store, reconciler), false)
	if calls != 1 {
		t.Fatalf("replayed completed call %d times", calls)
	}
}

func TestConsumerOwnsProgressAcrossEngineRestart(t *testing.T) {
	store := teststore.New()
	key := enqueueKey(t, store, "one")
	// This map stands in for the consumer's durable application repository.
	// The queue stores none of these values.
	progress := map[datastore.Key]string{}
	calls := 0
	callback := reconcile.ReconcilerFunc(func(ctx context.Context, got datastore.Key) (reconcile.Result, error) {
		if got != key {
			t.Fatalf("wrong resource: %v", got)
		}
		calls++
		if progress[got] == "" {
			progress[got] = "prepared"
			return reconcile.Result{Again: true, After: time.Second}, nil
		}
		if progress[got] != "prepared" {
			t.Fatal("lost application progress")
		}
		progress[got] = "complete"
		return reconcile.Result{}, nil
	})
	reconcileOne(t, newTestEngine(t, store, callback), true)
	restarted := newTestEngine(t, store, callback)
	reconcileOne(t, restarted, false)
	store.Advance(time.Second)
	reconcileOne(t, restarted, true)
	reconcileOne(t, restarted, false)
	if calls != 2 || progress[key] != "complete" {
		t.Fatalf("calls=%d progress=%v", calls, progress)
	}
}

// failFirstCompletion simulates interruption after application progress was saved
// but before the queue accepted completion.
type failFirstCompletion struct {
	datastore.Store
	failed bool
}

func (store *failFirstCompletion) Commit(ctx context.Context, claim datastore.Claim, completion datastore.Completion) error {
	if !store.failed {
		store.failed = true
		return errors.New("completion not delivered")
	}
	return store.Store.Commit(ctx, claim, completion)
}

func TestApplicationProgressSurvivesUndeliveredCompletion(t *testing.T) {
	model := teststore.New()
	key := enqueueKey(t, model, "progress-before-completion")
	queue := &failFirstCompletion{Store: model}
	applicationComplete := false
	calls, effects := 0, 0
	callback := reconcile.ReconcilerFunc(func(ctx context.Context, got datastore.Key) (reconcile.Result, error) {
		if got != key {
			t.Fatalf("wrong key: %v", got)
		}
		calls++
		// A consumer reloads its persisted progress on every call. This fixture
		// demonstrates ordering and repetition, not application storage durability.
		if !applicationComplete {
			effects++
			applicationComplete = true
		}
		return reconcile.Result{}, nil
	})
	if _, err := newTestEngine(t, queue, callback).ReconcileOne(t.Context()); err == nil {
		t.Fatal("undelivered completion reported success")
	}
	model.Advance(testConfig().LeaseDuration)
	reconcileOne(t, newTestEngine(t, queue, callback), true)
	reconcileOne(t, newTestEngine(t, queue, callback), false)
	if calls != 2 || effects != 1 {
		t.Fatalf("calls=%d effects=%d", calls, effects)
	}
}
