package memory

import (
	"errors"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/storetest"
)

type testClock struct {
	lock sync.Mutex
	now  time.Time
}

func fixture(t *testing.T) storetest.Fixture {
	t.Helper()
	clock := &testClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	store := New(Config{Clock: func() time.Time {
		clock.lock.Lock()
		defer clock.lock.Unlock()
		return clock.now
	}})
	return storetest.Fixture{
		Store: store,
		Elapse: func(duration time.Duration) {
			clock.lock.Lock()
			defer clock.lock.Unlock()
			clock.now = clock.now.Add(duration)
		},
		// Only the same instance retains records. This is not restart recovery.
		Reopen: func() datastore.Store { return store },
	}
}

func TestStoreContract(t *testing.T) {
	storetest.Run(t, fixture)
}

func TestInspectionContract(t *testing.T) {
	storetest.RunInspection(t, fixture)
}

func TestOperationSequenceCorpus(t *testing.T) {
	for index, commands := range storetest.SequenceCorpus() {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			storetest.RunSequence(t, fixture(t), commands)
		})
	}
}

func TestIndependentInstancesStartEmpty(t *testing.T) {
	first, second := New(Config{}), New(Config{})
	key := datastore.Key{Kind: "fixture", ID: "one"}
	if err := first.Enqueue(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Get(t.Context(), key); !errors.Is(err, datastore.ErrNotFound) {
		t.Fatalf("independent instance retained records: %v", err)
	}
}

func TestDefaultClockExpiresClaims(t *testing.T) {
	store := New(Config{})
	key := datastore.Key{Kind: "fixture", ID: "one"}
	if err := store.Enqueue(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	first, err := store.Claim(t.Context(), []string{key.Kind}, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		next, err := store.Claim(t.Context(), []string{key.Kind}, time.Minute)
		if err == nil {
			if next.Fence <= first.Fence || next.Abandoned != 1 {
				t.Fatalf("default clock recovery: %+v", next)
			}
			return
		}
		if !errors.Is(err, datastore.ErrNoWork) || time.Now().After(deadline) {
			t.Fatalf("default clock did not expire claim: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCounterOverflowDoesNotMutateRecords(t *testing.T) {
	for _, operation := range []string{"enqueue", "redrive", "fence", "abandoned", "failure"} {
		t.Run(operation, func(t *testing.T) {
			store := fixture(t).Store.(*Store)
			key := datastore.Key{Kind: "fixture", ID: "one"}
			if err := store.Enqueue(t.Context(), key); err != nil {
				t.Fatal(err)
			}
			entry := store.entries[key]
			var mutate func() error
			switch operation {
			case "enqueue", "redrive":
				entry.sequence = math.MaxUint64
				if operation == "enqueue" {
					mutate = func() error { return store.Enqueue(t.Context(), key) }
				} else {
					mutate = func() error { return store.Redrive(t.Context(), key) }
				}
			case "fence", "abandoned":
				if operation == "fence" {
					entry.fence = math.MaxUint64
				} else {
					entry.active, entry.fence = true, 1
					entry.leaseUntil = entry.dueAt.Add(-time.Second)
					entry.resource.Abandoned = math.MaxUint32
				}
				mutate = func() error {
					_, err := store.Claim(t.Context(), []string{key.Kind}, time.Minute)
					return err
				}
			case "failure":
				claim, err := store.Claim(t.Context(), []string{key.Kind}, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				entry.resource.Failures = math.MaxUint32
				mutate = func() error {
					return store.Commit(t.Context(), claim, datastore.Completion{Failure: "overflow", Stop: true})
				}
			}
			before := *entry
			if err := mutate(); err == nil || *entry != before {
				t.Fatalf("overflow changed record: before=%+v after=%+v error=%v", before, *entry, err)
			}
		})
	}
}

func TestInvalidClockRejectsWrites(t *testing.T) {
	store := New(Config{Clock: func() time.Time { return time.Time{} }})
	key := datastore.Key{Kind: "fixture", ID: "one"}
	if err := store.Enqueue(t.Context(), key); err == nil {
		t.Fatal("zero scheduling clock accepted")
	}
	if _, err := store.Get(t.Context(), key); !errors.Is(err, datastore.ErrNotFound) {
		t.Fatal("failed enqueue created a record")
	}
}
