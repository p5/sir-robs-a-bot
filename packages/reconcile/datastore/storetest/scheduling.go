package storetest

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

func runSchedulingContract(t *testing.T, factory func(*testing.T) Fixture) {
	t.Run("priority-escalates-and-survives-follow-up", func(t *testing.T) {
		fixture := factory(t)
		low := datastore.Key{Kind: "priority", ID: "a"}
		high := datastore.Key{Kind: "priority", ID: "z"}
		for _, key := range []datastore.Key{low, high} {
			if err := fixture.Store.Enqueue(t.Context(), key); err != nil {
				t.Fatal(err)
			}
		}
		if err := fixture.Store.Enqueue(t.Context(), high, datastore.EnqueueOptions{Priority: math.MaxUint32}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.Store.Enqueue(t.Context(), high, datastore.EnqueueOptions{Priority: 1}); err != nil {
			t.Fatal(err)
		}
		claim, err := fixture.Reopen().Claim(t.Context(), []string{low.Kind}, time.Minute)
		if err != nil || claim.Key != high || claim.Priority != math.MaxUint32 {
			t.Fatalf("priority claim: %+v %v", claim, err)
		}
		if err := fixture.Store.Commit(t.Context(), claim, datastore.Completion{Again: true}); err != nil {
			t.Fatal(err)
		}
		claim, err = fixture.Store.Claim(t.Context(), []string{low.Kind}, time.Minute)
		if err != nil || claim.Key != high || claim.Priority != math.MaxUint32 {
			t.Fatalf("priority follow-up: %+v %v", claim, err)
		}
		if err := fixture.Store.Commit(t.Context(), claim, datastore.Completion{}); err != nil {
			t.Fatal(err)
		}
		item, err := fixture.Store.Get(t.Context(), high)
		if err != nil || item.Priority != 0 {
			t.Fatalf("finished priority: %+v %v", item, err)
		}
		claim, err = fixture.Store.Claim(t.Context(), []string{low.Kind}, time.Minute)
		if err != nil || claim.Key != low {
			t.Fatalf("remaining low priority: %+v %v", claim, err)
		}
	})
	t.Run("protected-delay-survives-enqueue-before-and-after-completion", func(t *testing.T) {
		fixture := factory(t)
		key := datastore.Key{Kind: "protected", ID: "one"}
		if err := fixture.Store.Enqueue(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		claim, err := fixture.Store.Claim(t.Context(), []string{key.Kind}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.Store.Enqueue(t.Context(), key, datastore.EnqueueOptions{Priority: 10}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.Store.Commit(t.Context(), claim, datastore.Completion{Failure: "rate limited", Again: true, After: time.Second, ProtectDelay: true}); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if err := fixture.Store.Enqueue(t.Context(), key, datastore.EnqueueOptions{Priority: 100}); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.Reopen().Claim(t.Context(), []string{key.Kind}, time.Minute); !errors.Is(err, datastore.ErrNoWork) {
				t.Fatalf("enqueue bypassed protected delay: %v", err)
			}
		}
		fixture.Elapse(2 * time.Second)
		next, err := fixture.Store.Claim(t.Context(), []string{key.Kind}, time.Minute)
		if err != nil || next.Failures != 1 || next.Priority != 100 || next.Sequence != 5 {
			t.Fatalf("protected recovery: %+v %v", next, err)
		}
	})
	t.Run("redrive-clears-protected-delay", func(t *testing.T) {
		fixture := factory(t)
		key := datastore.Key{Kind: "protected", ID: "one"}
		if err := fixture.Store.Enqueue(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		claim, err := fixture.Store.Claim(t.Context(), []string{key.Kind}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.Store.Commit(t.Context(), claim, datastore.Completion{Again: true, After: time.Hour, ProtectDelay: true}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.Store.Redrive(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.Store.Claim(t.Context(), []string{key.Kind}, time.Minute); err != nil {
			t.Fatal("redrive retained scheduling floor", err)
		}
	})
	t.Run("rejects-ambiguous-enqueue-options", func(t *testing.T) {
		fixture := factory(t)
		key := datastore.Key{Kind: "priority", ID: "one"}
		if err := fixture.Store.Enqueue(t.Context(), key, datastore.EnqueueOptions{}, datastore.EnqueueOptions{}); err == nil {
			t.Fatal("ambiguous enqueue accepted")
		}
		if _, err := fixture.Store.Get(t.Context(), key); !errors.Is(err, datastore.ErrNotFound) {
			t.Fatalf("invalid enqueue mutated storage: %v", err)
		}
	})
}
