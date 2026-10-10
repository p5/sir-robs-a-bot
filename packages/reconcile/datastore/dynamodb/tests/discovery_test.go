package dynamodbtests

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	adapter "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/dynamodb"
)

func TestDiscoverySkipsRetainedCompletedRecords(t *testing.T) {
	store, _, _, _ := newFixture(t)
	// Retained resources have no scheduling entry. They must not add pages
	// to discovery, even after crossing the former partition-query page size.
	for index := range 260 {
		enqueueKey(t, store, fmt.Sprintf("a-%03d", index))
		claim := claimKey(t, store)
		if err := store.Commit(t.Context(), claim, datastore.Completion{Stop: true}); err != nil {
			t.Fatal(err)
		}
	}
	key := enqueueKey(t, store, "z-due")
	claim := claimKey(t, store)
	if claim.Key != key {
		t.Fatalf("due resource behind filtered pages was lost: %+v", claim)
	}
}

func TestDiscoveryKeepsHighestPriorityBeyondCandidateWindow(t *testing.T) {
	store, _, _, _ := newFixture(t)
	for index := range 260 {
		enqueueKey(t, store, fmt.Sprintf("a-%03d", index))
	}
	key := datastore.Key{Kind: "fixture", ID: "z-highest"}
	if err := store.Enqueue(t.Context(), key, datastore.EnqueueOptions{Priority: 100}); err != nil {
		t.Fatal(err)
	}
	claim := claimKey(t, store)
	if claim.Key != key || claim.Priority != 100 {
		t.Fatalf("bounded selection lost priority: %+v", claim)
	}
}

func TestNamespaceAndKindComponentsDoNotCollide(t *testing.T) {
	_, _, config, _ := newFixture(t)
	firstConfig := config
	firstConfig.Namespace += ":a"
	secondConfig := config
	first, err := adapter.New(newClient(), firstConfig)
	if err != nil {
		t.Fatal(err)
	}
	second, err := adapter.New(newClient(), secondConfig)
	if err != nil {
		t.Fatal(err)
	}
	key := datastore.Key{Kind: "b", ID: "same"}
	other := datastore.Key{Kind: "a:b", ID: "same"}
	if err := first.Enqueue(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if err := second.Enqueue(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		store datastore.Store
		key   datastore.Key
		want  string
	}{{first, key, "first"}, {second, other, "second"}} {
		claim, err := fixture.store.Claim(t.Context(), []string{fixture.key.Kind}, time.Second)
		if err != nil || claim.Key != fixture.key {
			t.Fatalf("namespace collision: %+v %v", claim, err)
		}
	}
}

func TestClockMovementDoesNotAuthorizeSupersededOwner(t *testing.T) {
	store, _, config, clock := newFixture(t)
	enqueueKey(t, store, "clock")
	previous := claimKey(t, store)
	clock.advance(2 * time.Minute)
	successor := claimKey(t, store)
	clock.advance(-24 * time.Hour)
	other, err := adapter.New(newClient(), config)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Commit(t.Context(), previous, datastore.Completion{}); !errors.Is(err, datastore.ErrLeaseLost) {
		t.Fatalf("clock rollback restored ownership: %v", err)
	}
	if err := other.Commit(t.Context(), successor, datastore.Completion{}); err != nil {
		t.Fatalf("clock rollback revoked current ownership: %v", err)
	}
}
