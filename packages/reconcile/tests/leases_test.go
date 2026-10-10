package reconciletests

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/internal/teststore"
)

type renewalStore struct {
	datastore.Store
	renewed chan struct{}
	failure error
	commits atomic.Int32
}

func (store *renewalStore) Renew(ctx context.Context, claim datastore.Claim, duration time.Duration) error {
	if store.failure != nil {
		return store.failure
	}
	if err := store.Store.Renew(ctx, claim, duration); err != nil {
		return err
	}
	store.renewed <- struct{}{}
	return nil
}
func (store *renewalStore) Commit(ctx context.Context, claim datastore.Claim, completion datastore.Completion) error {
	store.commits.Add(1)
	return store.Store.Commit(ctx, claim, completion)
}

func TestEngineRenewsDuringLongCall(t *testing.T) {
	model := teststore.New()
	enqueueKey(t, model, "long-call")
	store := &renewalStore{Store: model, renewed: make(chan struct{}, 8)}
	config := testConfig()
	config.LeaseDuration = 30 * time.Millisecond
	config.CallTimeout = time.Second
	callback := reconcile.ReconcilerFunc(func(ctx context.Context, key datastore.Key) (reconcile.Result, error) {
		// Observe several heartbeats rather than assuming that a sleeping goroutine
		// received enough CPU time to renew its lease.
		for range 4 {
			select {
			case <-store.renewed:
			case <-ctx.Done():
				return reconcile.Result{}, ctx.Err()
			}
		}
		return reconcile.Result{}, nil
	})
	engine, err := reconcile.New(store, map[string]reconcile.Reconciler{"fixture": callback}, config)
	if err != nil {
		t.Fatal(err)
	}
	reconcileOne(t, engine, true)
	if store.commits.Load() != 1 {
		t.Fatalf("completion count=%d", store.commits.Load())
	}
}

func TestRenewalFailureCancelsCallWithoutCompletion(t *testing.T) {
	for _, failure := range []error{datastore.ErrLeaseLost, errors.New("renewal response lost")} {
		t.Run(failure.Error(), func(t *testing.T) {
			model := teststore.New()
			key := enqueueKey(t, model, "lost-ownership")
			store := &renewalStore{Store: model, failure: failure}
			config := testConfig()
			config.LeaseDuration = 30 * time.Millisecond
			config.CallTimeout = time.Second
			stopped := false
			engine, err := reconcile.New(store, map[string]reconcile.Reconciler{"fixture": reconcile.ReconcilerFunc(func(ctx context.Context, key datastore.Key) (reconcile.Result, error) {
				<-ctx.Done()
				stopped = true
				return reconcile.Result{}, nil
			})}, config)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := engine.ReconcileOne(t.Context()); !errors.Is(err, failure) {
				t.Fatalf("renewal error hidden: %v", err)
			}
			if !stopped || store.commits.Load() != 0 {
				t.Fatalf("stopped=%v completions=%d", stopped, store.commits.Load())
			}
			item, err := model.Get(t.Context(), key)
			if err != nil || !item.Pending || item.Failures != 0 {
				t.Fatalf("renewal failure became completion: %+v %v", item, err)
			}
		})
	}
}

func TestCrashBudgetSuspendsBeforeCallingReconciler(t *testing.T) {
	model := teststore.New()
	key := enqueueKey(t, model, "poison")
	config := testConfig()
	config.MaxAbandoned = 2
	for range 2 {
		if _, err := model.Claim(t.Context(), []string{key.Kind}, config.LeaseDuration); err != nil {
			t.Fatal(err)
		}
		model.Advance(config.LeaseDuration)
	}
	calls := 0
	engine, err := reconcile.New(model, map[string]reconcile.Reconciler{"fixture": reconcile.ReconcilerFunc(func(context.Context, datastore.Key) (reconcile.Result, error) {
		calls++
		return reconcile.Result{}, nil
	})}, config)
	if err != nil {
		t.Fatal(err)
	}
	reconcileOne(t, engine, true)
	reconcileOne(t, engine, false)
	item, err := model.Get(t.Context(), key)
	if err != nil || item.Pending || item.Abandoned != 2 || calls != 0 {
		t.Fatalf("budget failed: %+v calls=%d %v", item, calls, err)
	}
	if err := model.Enqueue(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	reconcileOne(t, engine, true)
	if calls != 0 {
		t.Fatal("ordinary enqueue erased crash budget")
	}
	if err := model.Redrive(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	reconcileOne(t, engine, true)
	if calls != 1 {
		t.Fatalf("redrive did not restore processing: %d", calls)
	}
}

func TestOrderlyShutdownReleasesWithoutAbandonment(t *testing.T) {
	model := teststore.New()
	key := enqueueKey(t, model, "shutdown")
	ctx, cancel := context.WithCancel(t.Context())
	callback := reconcile.ReconcilerFunc(func(ctx context.Context, key datastore.Key) (reconcile.Result, error) {
		cancel()
		return reconcile.Result{}, ctx.Err()
	})
	if _, err := newTestEngine(t, model, callback).ReconcileOne(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	claim, err := model.Claim(t.Context(), []string{key.Kind}, time.Minute)
	if err != nil || claim.Abandoned != 0 || claim.Failures != 0 {
		t.Fatalf("orderly shutdown charged a crash: %+v %v", claim, err)
	}
}
