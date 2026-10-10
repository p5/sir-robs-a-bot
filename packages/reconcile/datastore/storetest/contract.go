// Package storetest runs the shared coordination-store contract against adapters.
// Passing this suite does not prove power-loss durability or service availability.
package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

// Fixture provides isolated storage and an adapter-clock test harness.
// Elapse must advance store time or wait until the requested duration has passed
// there. Reopen must use a new client to the same state for a durable adapter.
// Each factory invocation must have a fresh namespace and register cleanup.
type Fixture struct {
	Store  datastore.Store
	Elapse func(time.Duration)
	Reopen func() datastore.Store
}

// Run exercises atomicity, claim fencing, enqueue retention, and scheduling.
func Run(t *testing.T, factory func(*testing.T) Fixture) {
	t.Helper()
	runRecoveryContract(t, factory)
	runSchedulingContract(t, factory)
	key := datastore.Key{Kind: "fixture", ID: "one"}
	ttl := time.Second
	claimKey := func(t *testing.T, store datastore.Store) datastore.Claim {
		t.Helper()
		claim, err := store.Claim(t.Context(), []string{key.Kind}, ttl)
		if err != nil {
			t.Fatal(err)
		}
		return claim
	}
	assertNoDueWork := func(t *testing.T, store datastore.Store) {
		t.Helper()
		_, err := store.Claim(t.Context(), []string{key.Kind}, ttl)
		if !errors.Is(err, datastore.ErrNoWork) {
			t.Fatalf("want no work, got %v", err)
		}
	}
	enqueueKey := func(t *testing.T, store datastore.Store) {
		t.Helper()
		if err := store.Enqueue(t.Context(), key); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("concurrent-enqueue-coalesces", func(t *testing.T) {
		fixture := factory(t)
		var workers sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			workers.Go(func() { errs <- fixture.Store.Enqueue(t.Context(), key) })
		}
		workers.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		claim := claimKey(t, fixture.Store)
		if claim.Sequence != 8 || claim.Fence != 1 {
			t.Fatalf("lost signals: %+v", claim)
		}
		assertNoDueWork(t, fixture.Store)
	})
	t.Run("claim-exclusion-and-filter", func(t *testing.T) {
		fixture := factory(t)
		enqueueKey(t, fixture.Store)
		if _, err := fixture.Store.Claim(t.Context(), []string{"other"}, ttl); !errors.Is(err, datastore.ErrNoWork) {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wg.Go(func() {
				_, err := fixture.Store.Claim(t.Context(), []string{key.Kind}, ttl)
				errs <- err
			})
		}
		wg.Wait()
		close(errs)
		wins := 0
		for err := range errs {
			if err == nil {
				wins++
			} else if !errors.Is(err, datastore.ErrNoWork) {
				t.Fatal(err)
			}
		}
		if wins != 1 {
			t.Fatalf("%d simultaneous owners", wins)
		}
	})
	t.Run("completion-after-expiry-before-recovery", func(t *testing.T) {
		fixture := factory(t)
		enqueueKey(t, fixture.Store)
		claim := claimKey(t, fixture.Store)
		fixture.Elapse(2 * ttl)
		if err := fixture.Store.Commit(t.Context(), claim, datastore.Completion{}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.Store.Commit(t.Context(), claim, datastore.Completion{}); !errors.Is(err, datastore.ErrLeaseLost) {
			t.Fatalf("duplicate completion: %v", err)
		}
		resource, err := fixture.Store.Get(t.Context(), key)
		if err != nil {
			t.Fatalf("%+v %v", resource, err)
		}
		assertNoDueWork(t, fixture.Store)
	})
	t.Run("completion-competes-with-recovery", func(t *testing.T) {
		fixture := factory(t)
		enqueueKey(t, fixture.Store)
		previous := claimKey(t, fixture.Store)
		fixture.Elapse(2 * ttl)
		start := make(chan struct{})
		completed := make(chan error, 1)
		go func() {
			<-start
			completed <- fixture.Store.Commit(t.Context(), previous, datastore.Completion{})
		}()
		close(start)
		successor, claimErr := fixture.Store.Claim(t.Context(), []string{key.Kind}, ttl)
		commitErr := <-completed
		switch {
		case claimErr == nil:
			if !errors.Is(commitErr, datastore.ErrLeaseLost) {
				t.Fatalf("recovery won but previous completion returned %v", commitErr)
			}
			if err := fixture.Store.Commit(t.Context(), successor, datastore.Completion{}); err != nil {
				t.Fatal(err)
			}
		case errors.Is(claimErr, datastore.ErrNoWork):
			if commitErr != nil {
				t.Fatalf("completion won but returned %v", commitErr)
			}
		default:
			t.Fatal(claimErr)
		}
		assertNoDueWork(t, fixture.Store)
	})
	t.Run("expiry-and-stale-owner", func(t *testing.T) {
		fixture := factory(t)
		enqueueKey(t, fixture.Store)
		previousClaim := claimKey(t, fixture.Store)
		fixture.Elapse(2 * ttl)
		nextClaim := claimKey(t, fixture.Store)
		if nextClaim.Fence <= previousClaim.Fence {
			t.Fatal("fence did not advance")
		}
		if err := fixture.Store.Commit(t.Context(), previousClaim, datastore.Completion{}); !errors.Is(err, datastore.ErrLeaseLost) {
			t.Fatal(err)
		}
		if err := fixture.Store.Commit(t.Context(), nextClaim, datastore.Completion{}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.Store.Commit(t.Context(), nextClaim, datastore.Completion{}); !errors.Is(err, datastore.ErrLeaseLost) {
			t.Fatal(err)
		}
		resource, err := fixture.Store.Get(t.Context(), key)
		if err != nil {
			t.Fatalf("%+v %v", resource, err)
		}
		assertNoDueWork(t, fixture.Store)
	})
	t.Run("enqueue-during-call", func(t *testing.T) {
		fixture := factory(t)
		enqueueKey(t, fixture.Store)
		claim := claimKey(t, fixture.Store)
		if err := fixture.Store.Enqueue(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		assertNoDueWork(t, fixture.Store)
		if err := fixture.Store.Commit(t.Context(), claim, datastore.Completion{Stop: true}); err != nil {
			t.Fatal(err)
		}
		claimKey(t, fixture.Store)
	})

	t.Run("durable-delay-and-reopen", func(t *testing.T) {
		fixture := factory(t)
		enqueueKey(t, fixture.Store)
		claim := claimKey(t, fixture.Store)
		if err := fixture.Store.Commit(t.Context(), claim, datastore.Completion{Again: true, After: 3 * ttl}); err != nil {
			t.Fatal(err)
		}
		store := fixture.Reopen()
		assertNoDueWork(t, store)
		fixture.Elapse(4 * ttl)
		claimKey(t, store)
	})
	t.Run("enqueue-brings-delay-forward", func(t *testing.T) {
		fixture := factory(t)
		enqueueKey(t, fixture.Store)
		claim := claimKey(t, fixture.Store)
		if err := fixture.Store.Commit(t.Context(), claim, datastore.Completion{Again: true, After: 3 * ttl}); err != nil {
			t.Fatal(err)
		}
		assertNoDueWork(t, fixture.Store)
		if err := fixture.Store.Enqueue(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		claimKey(t, fixture.Store)
	})
	t.Run("failures-survive-reopen", func(t *testing.T) {
		fixture := factory(t)
		enqueueKey(t, fixture.Store)
		claim := claimKey(t, fixture.Store)
		if err := fixture.Store.Commit(t.Context(), claim, datastore.Completion{Failure: "broken", Stop: true}); err != nil {
			t.Fatal(err)
		}
		store := fixture.Reopen()
		assertNoDueWork(t, store)
		resource, err := store.Get(t.Context(), key)
		if err != nil || resource.Failures != 1 || resource.LastError != "broken" {
			t.Fatalf("%+v %v", resource, err)
		}
		if err := store.Enqueue(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		claim = claimKey(t, store)
		if claim.Failures != 1 {
			t.Fatal("enqueue erased failure diagnostics")
		}
	})

	t.Run("cancelled-operations", func(t *testing.T) {
		fixture := factory(t)
		enqueueKey(t, fixture.Store)
		claim := claimKey(t, fixture.Store)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		operations := map[string]func() error{
			"get":     func() error { _, err := fixture.Store.Get(ctx, key); return err },
			"enqueue": func() error { return fixture.Store.Enqueue(ctx, key) },
			"claim":   func() error { _, err := fixture.Store.Claim(ctx, []string{key.Kind}, ttl); return err },
			"commit": func() error {
				return fixture.Store.Commit(ctx, claim, datastore.Completion{})
			},
		}
		for name, operation := range operations {
			t.Run(name, func(t *testing.T) {
				if err := operation(); !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled %s returned %v", name, err)
				}
			})
		}
		if err := fixture.Store.Commit(t.Context(), claim, datastore.Completion{}); err != nil {
			t.Fatalf("cancelled operation changed ownership: %v", err)
		}
		resource, err := fixture.Store.Get(t.Context(), key)
		if err != nil {
			t.Fatalf("read after cancellation: %v", err)
		}
		if resource.Pending {
			t.Fatalf("cancelled operation changed resource: %+v", resource)
		}
	})

	t.Run("resource-reference-identity", func(t *testing.T) {
		fixture := factory(t)
		keys := []datastore.Key{
			{Kind: "github-path", ID: "https://github.com/p5/example/blob/main/docs/a%2Fb.md"},
			{Kind: "github-path", ID: "https://github.com/p5/example/blob/main/docs/a/b.md"},
			{Kind: "github-review", ID: "https://github.com/p5/example/blob/main/docs/a%2Fb.md"},
			{Kind: "request", ID: "urn:factory:request:example"},
		}
		for _, key := range keys {
			if err := fixture.Store.Enqueue(t.Context(), key); err != nil {
				t.Fatalf("create %v: %v", key, err)
			}
		}
		for _, key := range keys {
			resource, err := fixture.Store.Get(t.Context(), key)
			if err != nil {
				t.Fatalf("get %v: %v", key, err)
			}
			if resource.Key != key {
				t.Fatalf("store changed identity %v: %+v", key, resource)
			}
			if err := fixture.Store.Enqueue(t.Context(), key); err != nil {
				t.Fatalf("duplicate identity %v did not coalesce: %v", key, err)
			}
		}
	})

	t.Run("invalid-resource-identity", func(t *testing.T) {
		fixture := factory(t)
		for _, key := range []datastore.Key{
			{Kind: " ", ID: "one"},
			{Kind: "fixture", ID: ""},
			{Kind: "fixture", ID: "one\x00two"},
			{Kind: "fixture", ID: "\xff"},
		} {
			if err := fixture.Store.Enqueue(t.Context(), key); err == nil {
				t.Fatalf("enqueue accepted invalid identity %q", key)
			}
			if _, err := fixture.Store.Get(t.Context(), key); err == nil || errors.Is(err, datastore.ErrNotFound) {
				t.Fatalf("get did not validate identity %q: %v", key, err)
			}
		}
	})

	t.Run("missing-resource", func(t *testing.T) {
		fixture := factory(t)
		if _, err := fixture.Store.Get(t.Context(), key); !errors.Is(err, datastore.ErrNotFound) {
			t.Fatal(err)
		}
	})
}
