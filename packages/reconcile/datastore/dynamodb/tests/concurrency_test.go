package dynamodbtests

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	adapter "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/dynamodb"
)

// interceptHTTP reaches the real local service, but can lose its successful
// write response or insert a competing write immediately before a conditional mutation.
type interceptHTTP struct {
	writes      atomic.Int32
	drop        atomic.Bool
	malformed   atomic.Bool
	beforeWrite func()
}

func (client *interceptHTTP) Do(request *http.Request) (*http.Response, error) {
	isWrite := request.Header.Get("X-Amz-Target") == "DynamoDB_20120810.TransactWriteItems"
	if isWrite {
		client.writes.Add(1)
		if client.beforeWrite != nil {
			before := client.beforeWrite
			client.beforeWrite = nil
			before()
		}
	}
	response, err := http.DefaultClient.Do(request)
	if err == nil && isWrite && response.StatusCode == http.StatusOK && client.drop.Swap(false) {
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return nil, errors.New("injected lost write response")
	}
	if err == nil && isWrite && response.StatusCode == http.StatusOK && client.malformed.Swap(false) {
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		response.Body = io.NopCloser(strings.NewReader(`{"Attributes":{"":{"":{"":0A0`))
	}
	return response, err
}

func interceptedStore(t *testing.T, config adapter.Config, transport *interceptHTTP) *adapter.Store {
	t.Helper()
	options := newClient().Options()
	options.HTTPClient = transport
	// Restore SDK-default retry behavior. The adapter must override it for its
	// conditional mutations, even when the supplied client normally retries.
	options.Retryer = nil
	store, err := adapter.New(sdk.New(options), config)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestLostWriteResponseDoesNotRetryMutation(t *testing.T) {
	for _, operation := range []string{"enqueue", "claim", "commit"} {
		t.Run(operation, func(t *testing.T) {
			store, _, config, _ := newFixture(t)
			key := enqueueKey(t, store, "lost-response")
			var owned datastore.Claim
			if operation == "commit" {
				owned = claimKey(t, store)
			}
			transport := new(interceptHTTP)
			transport.drop.Store(true)
			faulty := interceptedStore(t, config, transport)
			var err error
			switch operation {
			case "enqueue":
				err = faulty.Enqueue(t.Context(), key)
			case "claim":
				_, err = faulty.Claim(t.Context(), []string{key.Kind}, time.Second)
			case "commit":
				err = faulty.Commit(t.Context(), owned, datastore.Completion{})
			}
			if err == nil || errors.Is(err, datastore.ErrLeaseLost) || errors.Is(err, datastore.ErrNoWork) {
				t.Fatalf("unknown outcome was hidden: %v", err)
			}
			if transport.writes.Load() != 1 {
				t.Fatalf("uncertain write retried %d times", transport.writes.Load())
			}
			resource, err := store.Get(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "enqueue":
				if claimKey(t, store).Sequence != 2 {
					t.Fatal("wake was lost or duplicated")
				}
			case "claim":
				if _, err := store.Claim(t.Context(), []string{key.Kind}, time.Second); !errors.Is(err, datastore.ErrNoWork) {
					t.Fatalf("uncertain claim was not retained: %v", err)
				}
			case "commit":
				if resource.Pending {
					t.Fatalf("completion did not reach storage: %+v", resource)
				}
				if err := store.Commit(t.Context(), owned, datastore.Completion{}); !errors.Is(err, datastore.ErrLeaseLost) {
					t.Fatalf("uncertain completion was applied twice: %v", err)
				}
			}
		})
	}
}

func TestCompletionRetriesAroundConcurrentEnqueue(t *testing.T) {
	store, _, config, _ := newFixture(t)
	key := enqueueKey(t, store, "concurrent-input")
	owned := claimKey(t, store)
	transport := &interceptHTTP{beforeWrite: func() {
		if err := store.Enqueue(t.Context(), key); err != nil {
			t.Fatal(err)
		}
	}}
	faulty := interceptedStore(t, config, transport)
	if err := faulty.Commit(t.Context(), owned, datastore.Completion{Stop: true}); err != nil {
		t.Fatal(err)
	}
	if transport.writes.Load() != 2 {
		t.Fatalf("want CAS conflict and retry, got %d", transport.writes.Load())
	}
	if next := claimKey(t, store); next.Sequence != 2 {
		t.Fatalf("enqueue lost: %+v", next)
	}
}

func TestProtectedCompletionRetainsConcurrentPriorityEscalation(t *testing.T) {
	store, _, config, clock := newFixture(t)
	key := enqueueKey(t, store, "protected-race")
	owned := claimKey(t, store)
	transport := &interceptHTTP{beforeWrite: func() {
		if err := store.Enqueue(t.Context(), key, datastore.EnqueueOptions{Priority: 100}); err != nil {
			t.Fatal(err)
		}
	}}
	faulty := interceptedStore(t, config, transport)
	if err := faulty.Commit(t.Context(), owned, datastore.Completion{Failure: "rate limited", Again: true, After: time.Minute, ProtectDelay: true}); err != nil {
		t.Fatal(err)
	}
	if transport.writes.Load() != 2 {
		t.Fatal("completion did not exercise a revision conflict")
	}
	if _, err := store.Claim(t.Context(), []string{key.Kind}, time.Minute); !errors.Is(err, datastore.ErrNoWork) {
		t.Fatalf("concurrent enqueue bypassed floor: %v", err)
	}
	clock.advance(2 * time.Minute)
	next := claimKey(t, store)
	if next.Priority != 100 || next.Sequence != 2 || next.Failures != 1 {
		t.Fatalf("completion lost concurrent state: %+v", next)
	}
}

func TestLostProtectedCompletionResponseRetainsFloor(t *testing.T) {
	store, _, config, clock := newFixture(t)
	key := enqueueKey(t, store, "protected-response-loss")
	owned := claimKey(t, store)
	transport := new(interceptHTTP)
	transport.drop.Store(true)
	faulty := interceptedStore(t, config, transport)
	if err := faulty.Commit(t.Context(), owned, datastore.Completion{Again: true, After: time.Minute, ProtectDelay: true}); err == nil {
		t.Fatal("lost response reported success")
	}
	if transport.writes.Load() != 1 {
		t.Fatal("unknown completion was retried")
	}
	if err := store.Enqueue(t.Context(), key, datastore.EnqueueOptions{Priority: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(t.Context(), []string{key.Kind}, time.Minute); !errors.Is(err, datastore.ErrNoWork) {
		t.Fatalf("lost response erased floor: %v", err)
	}
	clock.advance(2 * time.Minute)
	if next := claimKey(t, store); next.Priority != 100 || next.Sequence != 2 {
		t.Fatalf("retained schedule: %+v", next)
	}
}

func TestSuccessorWinsBeforeCompletionWrite(t *testing.T) {
	store, _, config, clock := newFixture(t)
	enqueueKey(t, store, "successor")
	previous := claimKey(t, store)
	clock.advance(2 * time.Minute)
	var successor datastore.Claim
	transport := &interceptHTTP{beforeWrite: func() { successor = claimKey(t, store) }}
	faulty := interceptedStore(t, config, transport)
	if err := faulty.Commit(t.Context(), previous, datastore.Completion{}); !errors.Is(err, datastore.ErrLeaseLost) {
		t.Fatalf("superseded owner committed: %v", err)
	}
	if err := store.Commit(t.Context(), successor, datastore.Completion{}); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedResponseAfterSuccessfulWriteReturnsUnknownOutcome(t *testing.T) {
	store, _, config, _ := newFixture(t)
	key := enqueueKey(t, store, "decoder-panic")
	transport := new(interceptHTTP)
	transport.malformed.Store(true)
	faulty := interceptedStore(t, config, transport)
	// Inject a malformed attribute response after the mutation reached storage.
	if err := faulty.Enqueue(t.Context(), key); err == nil {
		t.Fatalf("decoder failure hid the unknown write outcome: %v", err)
	}
	if transport.writes.Load() != 1 {
		t.Fatalf("decoder failure retried the mutation: %d writes", transport.writes.Load())
	}
	resource, err := store.Get(t.Context(), key)
	if err != nil || !resource.Pending {
		t.Fatalf("write did not persist before decoder failure: %+v %v", resource, err)
	}
}

func TestLostCreateResponseRetainsEnqueuedKey(t *testing.T) {
	store, _, config, _ := newFixture(t)
	key := datastore.Key{Kind: "fixture", ID: "new-unknown-outcome"}
	transport := new(interceptHTTP)
	transport.drop.Store(true)
	faulty := interceptedStore(t, config, transport)
	if err := faulty.Enqueue(t.Context(), key); err == nil {
		t.Fatal("lost create response reported success")
	}
	// Conditional creation and its ready entry commit in one transaction.
	if transport.writes.Load() != 1 {
		t.Fatalf("unexpected mutations: %d", transport.writes.Load())
	}
	if claim := claimKey(t, store); claim.Key != key || claim.Sequence != 1 {
		t.Fatalf("lost or duplicated enqueue: %+v", claim)
	}
}

func TestLeaseMutationUnknownOutcomesAreNotRetried(t *testing.T) {
	for _, operation := range []string{"renew", "release", "redrive"} {
		t.Run(operation, func(t *testing.T) {
			store, _, config, clock := newFixture(t)
			key := enqueueKey(t, store, "lease-response-loss")
			claim := claimKey(t, store)
			if operation == "redrive" {
				if err := store.Commit(t.Context(), claim, datastore.Completion{Failure: "broken", Stop: true}); err != nil {
					t.Fatal(err)
				}
			}
			transport := new(interceptHTTP)
			transport.drop.Store(true)
			faulty := interceptedStore(t, config, transport)
			var err error
			switch operation {
			case "renew":
				err = faulty.Renew(t.Context(), claim, 3*time.Minute)
			case "release":
				err = faulty.Release(t.Context(), claim)
			case "redrive":
				err = faulty.Redrive(t.Context(), key)
			}
			if err == nil || errors.Is(err, datastore.ErrLeaseLost) {
				t.Fatalf("unknown outcome hidden: %v", err)
			}
			if transport.writes.Load() != 1 {
				t.Fatalf("uncertain mutation repeated: %d", transport.writes.Load())
			}
			switch operation {
			case "renew":
				clock.advance(2 * time.Minute)
				if _, err := store.Claim(t.Context(), []string{key.Kind}, time.Minute); !errors.Is(err, datastore.ErrNoWork) {
					t.Fatalf("acknowledged renewal lost: %v", err)
				}
			case "release":
				next := claimKey(t, store)
				if next.Abandoned != 0 {
					t.Fatalf("release counted as crash: %+v", next)
				}
			case "redrive":
				next := claimKey(t, store)
				if next.Failures != 0 || next.Abandoned != 0 {
					t.Fatalf("redrive lost: %+v", next)
				}
			}
		})
	}
}

func TestConcurrentEnqueueSurvivesLeaseTransitions(t *testing.T) {
	for _, operation := range []string{"renew", "release"} {
		t.Run(operation, func(t *testing.T) {
			store, _, config, _ := newFixture(t)
			key := enqueueKey(t, store, "enqueue-during-lease-change")
			claim := claimKey(t, store)
			transport := &interceptHTTP{beforeWrite: func() {
				if err := store.Enqueue(t.Context(), key); err != nil {
					t.Fatal(err)
				}
			}}
			faulty := interceptedStore(t, config, transport)
			var err error
			if operation == "renew" {
				err = faulty.Renew(t.Context(), claim, time.Minute)
			} else {
				err = faulty.Release(t.Context(), claim)
			}
			if err != nil {
				t.Fatal(err)
			}
			if operation == "renew" {
				if err := store.Commit(t.Context(), claim, datastore.Completion{}); err != nil {
					t.Fatal(err)
				}
			}
			next := claimKey(t, store)
			if next.Sequence != 2 || next.Abandoned != 0 {
				t.Fatalf("lease transition lost enqueue: %+v", next)
			}
		})
	}
}

func TestSuccessorFencesInFlightLeaseMutation(t *testing.T) {
	for _, operation := range []string{"renew", "release"} {
		t.Run(operation, func(t *testing.T) {
			store, _, config, clock := newFixture(t)
			enqueueKey(t, store, "successor-during-lease-write")
			previous := claimKey(t, store)
			clock.advance(2 * time.Minute)
			var successor datastore.Claim
			transport := &interceptHTTP{beforeWrite: func() { successor = claimKey(t, store) }}
			faulty := interceptedStore(t, config, transport)
			var err error
			if operation == "renew" {
				err = faulty.Renew(t.Context(), previous, time.Minute)
			} else {
				err = faulty.Release(t.Context(), previous)
			}
			if !errors.Is(err, datastore.ErrLeaseLost) {
				t.Fatalf("stale %s accepted: %v", operation, err)
			}
			if err := store.Commit(t.Context(), successor, datastore.Completion{}); err != nil {
				t.Fatal("old mutation damaged successor", err)
			}
		})
	}
}
