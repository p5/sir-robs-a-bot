package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

func runRecoveryContract(t *testing.T, factory func(*testing.T) Fixture) {
	key := datastore.Key{Kind: "recovery", ID: "one"}
	ttl := time.Second
	prepare := func(t *testing.T) (Fixture, datastore.Claim) {
		t.Helper()
		fixture := factory(t)
		if err := fixture.Store.Enqueue(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		claim, err := fixture.Store.Claim(t.Context(), []string{key.Kind}, ttl)
		if err != nil {
			t.Fatal(err)
		}
		return fixture, claim
	}
	claimNext := func(t *testing.T, store datastore.Store) datastore.Claim {
		t.Helper()
		claim, err := store.Claim(t.Context(), []string{key.Kind}, ttl)
		if err != nil {
			t.Fatal(err)
		}
		return claim
	}
	t.Run("renewal-retains-owner-and-never-shortens", func(t *testing.T) {
		fixture, original := prepare(t)
		if err := fixture.Store.Renew(t.Context(), original, 4*ttl); err != nil {
			t.Fatal(err)
		}
		if err := fixture.Store.Renew(t.Context(), original, ttl); err != nil {
			t.Fatal(err)
		}
		fixture.Elapse(2 * ttl)
		if _, err := fixture.Reopen().Claim(t.Context(), []string{key.Kind}, ttl); !errors.Is(err, datastore.ErrNoWork) {
			t.Fatalf("renewal shortened or lost: %v", err)
		}
		if err := fixture.Store.Commit(t.Context(), original, datastore.Completion{}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.Store.Renew(t.Context(), original, ttl); !errors.Is(err, datastore.ErrLeaseLost) {
			t.Fatalf("completed claim renewed: %v", err)
		}
	})
	t.Run("takeover-fences-renewal-and-release", func(t *testing.T) {
		fixture, previous := prepare(t)
		fixture.Elapse(2 * ttl)
		successor := claimNext(t, fixture.Reopen())
		if successor.Abandoned != 1 {
			t.Fatalf("crash not recorded: %+v", successor)
		}
		if err := fixture.Store.Renew(t.Context(), previous, ttl); !errors.Is(err, datastore.ErrLeaseLost) {
			t.Fatalf("old owner renewed: %v", err)
		}
		if err := fixture.Store.Release(t.Context(), previous); !errors.Is(err, datastore.ErrLeaseLost) {
			t.Fatalf("old owner released successor: %v", err)
		}
		if err := fixture.Store.Commit(t.Context(), successor, datastore.Completion{}); err != nil {
			t.Fatal(err)
		}
		item, err := fixture.Store.Get(t.Context(), key)
		if err != nil || item.Abandoned != 0 {
			t.Fatalf("success did not clear abandonment: %+v %v", item, err)
		}
	})
	t.Run("abandonment-survives-reopen-enqueue-and-failure", func(t *testing.T) {
		fixture, _ := prepare(t)
		for count := uint32(1); count <= 3; count++ {
			fixture.Elapse(2 * ttl)
			store := fixture.Reopen()
			claim := claimNext(t, store)
			if claim.Abandoned != count {
				t.Fatalf("recovery %d: %+v", count, claim)
			}
			if err := store.Enqueue(t.Context(), key); err != nil {
				t.Fatal(err)
			}
			if count == 3 {
				if err := store.Commit(t.Context(), claim, datastore.Completion{Failure: "poison key", Stop: true}); err != nil {
					t.Fatal(err)
				}
			}
		}
		claim := claimNext(t, fixture.Store)
		if claim.Abandoned != 3 || claim.Failures != 1 {
			t.Fatalf("lost budgets: %+v", claim)
		}
		if err := fixture.Store.Redrive(t.Context(), key); !errors.Is(err, datastore.ErrBusy) {
			t.Fatalf("redrove active work: %v", err)
		}
		if err := fixture.Store.Release(t.Context(), claim); err != nil {
			t.Fatal(err)
		}
		if err := fixture.Store.Redrive(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		next := claimNext(t, fixture.Reopen())
		if next.Abandoned != 0 || next.Failures != 0 || next.Fence <= claim.Fence {
			t.Fatalf("redrive reset ownership or lost budget reset: %+v", next)
		}
	})
	t.Run("release-preserves-budgets-and-pending-enqueue", func(t *testing.T) {
		fixture, claim := prepare(t)
		if err := fixture.Store.Enqueue(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		if err := fixture.Store.Release(t.Context(), claim); err != nil {
			t.Fatal(err)
		}
		if err := fixture.Store.Release(t.Context(), claim); !errors.Is(err, datastore.ErrLeaseLost) {
			t.Fatalf("duplicate release: %v", err)
		}
		next := claimNext(t, fixture.Reopen())
		if next.Abandoned != 0 || next.Failures != 0 || next.Sequence != 2 {
			t.Fatalf("release consumed budget or lost enqueue: %+v", next)
		}
	})
	t.Run("invalid-and-cancelled-lease-operations", func(t *testing.T) {
		fixture, claim := prepare(t)
		for _, duration := range []time.Duration{0, -time.Second} {
			if err := fixture.Store.Renew(t.Context(), claim, duration); err == nil {
				t.Fatal("invalid renewal accepted")
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		for name, operation := range map[string]func() error{
			"renew":   func() error { return fixture.Store.Renew(ctx, claim, ttl) },
			"release": func() error { return fixture.Store.Release(ctx, claim) },
			"redrive": func() error { return fixture.Store.Redrive(ctx, key) },
		} {
			if err := operation(); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled %s: %v", name, err)
			}
		}
		if err := fixture.Store.Commit(t.Context(), claim, datastore.Completion{}); err != nil {
			t.Fatal("rejected operation changed ownership", err)
		}
		if err := fixture.Store.Redrive(t.Context(), datastore.Key{Kind: key.Kind, ID: "missing"}); !errors.Is(err, datastore.ErrNotFound) {
			t.Fatalf("missing redrive: %v", err)
		}
	})
}
