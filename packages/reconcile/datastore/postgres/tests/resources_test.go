package postgrestests

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/postgres"
)

func TestIndependentControllers(t *testing.T) {
	store, _, namespace := newFixture(t)
	key := enqueueKey(t, store, "one")
	next, err := postgres.New(openTestPool(t), namespace)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claims := make(chan datastore.Claim, 2)
	errs := make(chan error, 2)
	start := make(chan struct{})
	for _, store := range []datastore.Store{store, next} {
		wg.Go(func() {
			<-start
			ownedClaim, err := store.Claim(t.Context(), []string{"fixture"}, time.Minute)
			if err == nil {
				claims <- ownedClaim
			}
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		} else if !errors.Is(err, datastore.ErrNoWork) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("got %d owners", successes)
	}
	ownedClaim := <-claims
	if err := next.Enqueue(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(t.Context(), ownedClaim, datastore.Completion{}); err != nil {
		t.Fatal(err)
	}
	claimKey(t, next, time.Minute)
}

func TestLockedResourceDoesNotBlockOtherWork(t *testing.T) {
	store, db, namespace := newFixture(t)
	first := enqueueKey(t, store, "one")
	enqueueKey(t, store, "two")
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), "SELECT 1 FROM factory_reconcile_resources WHERE namespace=$1 AND kind=$2 AND id=$3 FOR UPDATE", namespace, first.Kind, first.ID); err != nil {
		t.Fatal(err)
	}
	ownedClaim := claimKey(t, store, time.Minute)
	if ownedClaim.Key.ID != "two" {
		t.Fatalf("claimed locked resource: %+v", ownedClaim)
	}
}

func TestCompletionAfterExpiryDuringLockWait(t *testing.T) {
	store, db, namespace := newFixture(t)
	enqueueKey(t, store, "one")
	ownedClaim := claimKey(t, store, time.Second)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), "SELECT 1 FROM factory_reconcile_resources WHERE namespace=$1 AND kind=$2 AND id=$3 FOR UPDATE", namespace, ownedClaim.Key.Kind, ownedClaim.Key.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- store.Commit(t.Context(), ownedClaim, datastore.Completion{}) }()
	// Observe the blocked backend before advancing past the lease expiry.
	waitForBlockedCommit(t, db)
	advanceStoreTime(t, db, 1100*time.Millisecond)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("unsuperseded completion rejected: %v", err)
	}
	resource, err := store.Get(t.Context(), ownedClaim.Key)
	if err != nil {
		t.Fatalf("%+v %v", resource, err)
	}
}

func TestRollbackPreservesClaimAndSchedule(t *testing.T) {
	store, db, namespace := newFixture(t)
	key := enqueueKey(t, store, "one")
	ownedClaim := claimKey(t, store, time.Minute)
	// The check constraint fails after SQL has attempted the diagnostic and schedule update.
	// PostgreSQL must roll the entire statement and transaction back.
	if _, err := db.ExecContext(t.Context(), "UPDATE factory_reconcile_resources SET failures=4294967295 WHERE namespace=$1", namespace); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(t.Context(), ownedClaim, datastore.Completion{Failure: "overflow", Again: true, After: time.Second}); err == nil {
		t.Fatal("expected constraint failure")
	}
	resource, err := store.Get(t.Context(), key)
	if err != nil || !resource.Pending || resource.LastError != "" {
		t.Fatalf("%+v %v", resource, err)
	}
	if err := store.Commit(t.Context(), ownedClaim, datastore.Completion{}); err != nil {
		t.Fatalf("failed transaction lost claim: %v", err)
	}
}

func TestCancelledWriteChangesNothing(t *testing.T) {
	store, _, _ := newFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	key := datastore.Key{Kind: "fixture", ID: "cancelled"}
	if err := store.Enqueue(ctx, key); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := store.Get(t.Context(), key); !errors.Is(err, datastore.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestNamespaceIsolation(t *testing.T) {
	first, _, _ := newFixture(t)
	second, _, _ := newFixture(t)
	key := enqueueKey(t, first, "one")
	if _, err := second.Get(t.Context(), key); !errors.Is(err, datastore.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := second.Claim(t.Context(), []string{key.Kind}, time.Minute); !errors.Is(err, datastore.ErrNoWork) {
		t.Fatal(err)
	}
}
