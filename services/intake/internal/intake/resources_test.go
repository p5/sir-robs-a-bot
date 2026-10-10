package intake

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/resources/content"
	objects "github.com/p5/sir-robs-a-bot/packages/resources/content/memory"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore"
	state "github.com/p5/sir-robs-a-bot/packages/resources/datastore/memory"
)

func openResourceStore(t *testing.T, objects content.Store, state datastore.Store) *ResourceStore {
	t.Helper()
	store, err := NewResourceStore(objects, state)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

type dispatchQueue struct {
	keys []queue.Key
	err  error
}

func (target *dispatchQueue) Enqueue(_ context.Context, key queue.Key, _ ...queue.EnqueueOptions) error {
	// Record even failures to model an enqueue that committed but lost its reply.
	target.keys = append(target.keys, key)
	return target.err
}

func TestResourceAcceptancePreservesOriginalObservationAndReceiptPolicy(t *testing.T) {
	for _, mode := range []ReceiptMode{ReceiptAsync, ReceiptInline, ReceiptNone} {
		t.Run(string(mode), func(t *testing.T) {
			objects, state := objects.New(), state.New()
			store := openResourceStore(t, objects, state)
			submission := validSubmission()
			submission.ReceiptMode = mode
			// These JSON values must survive without a database's JSON coercion.
			submission.Metadata = jsontext.Value(`{"large":1e20000,"small":1e-20000,"nul":"\u0000"}`)
			accepted, err := store.Accept(t.Context(), submission)
			if err != nil || !accepted.Created {
				t.Fatalf("accept original request: %+v %v", accepted, err)
			}
			id, _ := submission.Source.RequestID()
			if accepted.RequestID != id {
				t.Fatal("resource integration changed the existing request identity")
			}
			store = openResourceStore(t, objects, state)
			changed := submission
			changed.Instruction = "a later observation"
			changed.ReceiptMode = ReceiptAsync
			duplicate, err := store.Accept(t.Context(), changed)
			if err != nil || duplicate.Created || duplicate.RequestID != id {
				t.Fatalf("accept duplicate after reopen: %+v %v", duplicate, err)
			}
			request, err := store.Get(t.Context(), id)
			if err != nil || request.Instruction != submission.Instruction || request.ReceiptMode != mode || string(request.Metadata) != string(submission.Metadata) {
				t.Fatalf("original snapshot changed: %+v %v", request, err)
			}
			owner, factory := &dispatchQueue{}, &dispatchQueue{}
			found, err := store.DeliverOne(t.Context(), owner)
			if err != nil || !found || len(owner.keys) != 1 || owner.keys[0] != (queue.Key{Kind: Kind, ID: id}) {
				t.Fatalf("owner delivery: %v %v %+v", found, err, owner.keys)
			}
			if _, err := store.Dispatch(t.Context(), queue.Key{Kind: Kind, ID: id}, factory, &testIndex{}); err != nil {
				t.Fatal(err)
			}
			if len(factory.keys) != 1 {
				t.Fatal("factory request was not dispatched")
			}
			_, _, _, err = store.receipt(t.Context(), id)
			if mode == ReceiptAsync && err != nil {
				t.Fatal("receipt intent was not materialized", err)
			}
			if mode != ReceiptAsync && !errors.Is(err, datastore.ErrNotFound) {
				t.Fatal("disabled receipt was materialized", err)
			}
			for {
				found, err := store.DeliverOne(t.Context(), owner)
				if err != nil {
					t.Fatal(err)
				}
				if !found {
					break
				}
			}

			if _, err := store.Accept(t.Context(), changed); err != nil {
				t.Fatal(err)
			}
			if found, err := store.DeliverOne(t.Context(), owner); err != nil || found {
				t.Fatalf("duplicate recreated delivered work: %v %v", found, err)
			}
		})
	}
}

type interruptedResourceCommit struct {
	datastore.Store
	commit bool
	err    error
}

func (store interruptedResourceCommit) Create(ctx context.Context, record datastore.Record) (datastore.Record, bool, error) {
	if store.commit {
		if _, _, err := store.Store.Create(ctx, record); err != nil {
			return datastore.Record{}, false, err
		}
	}
	return datastore.Record{}, false, store.err
}

func TestResourceAcceptanceRecoveryUsesFirstCommittedInput(t *testing.T) {
	for _, test := range []struct {
		name      string
		committed bool
	}{
		{name: "before commit", committed: false},
		{name: "lost commit reply", committed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			objects, state := objects.New(), state.New()
			lost := errors.New("resource commit interrupted")
			store := openResourceStore(t, objects, interruptedResourceCommit{Store: state, commit: test.committed, err: lost})
			submission := validSubmission()
			if _, err := store.Accept(t.Context(), submission); !errors.Is(err, lost) {
				t.Fatalf("accept returned success after interrupted commit: %v", err)
			}
			store = openResourceStore(t, objects, state)
			changed := submission
			changed.Instruction = "changed before retry"
			accepted, err := store.Accept(t.Context(), changed)
			wantCreated := !test.committed
			if err != nil || accepted.Created != wantCreated {
				t.Fatalf("retry acceptance: %+v %v", accepted, err)
			}
			request, err := store.Get(t.Context(), accepted.RequestID)
			want := changed.Instruction
			if test.committed {
				want = submission.Instruction
			}
			if err != nil || request.Instruction != want {
				t.Fatalf("uncommitted upload pinned input: %+v %v", request, err)
			}
			pending, err := state.Pending(t.Context(), 100)
			if err != nil || len(pending) != 1 {
				t.Fatalf("retry lost notification: %+v %v", pending, err)
			}
		})
	}
}

func TestResourceConcurrentAcceptanceHasOneWinner(t *testing.T) {
	store := openResourceStore(t, objects.New(), state.New())
	var winners atomic.Int64
	var group sync.WaitGroup
	start := make(chan struct{})
	for range 16 {
		group.Go(func() {
			<-start
			accepted, err := store.Accept(t.Context(), validSubmission())
			if err != nil {
				t.Error(err)
			} else if accepted.Created {
				winners.Add(1)
			}
		})
	}
	close(start)
	group.Wait()
	if winners.Load() != 1 {
		t.Fatalf("concurrent acceptance had %d winners", winners.Load())
	}
}

func TestResourceDispatchRejectsCorruptInputWithoutAcknowledging(t *testing.T) {
	objects, state := objects.New(), state.New()
	store := openResourceStore(t, objects, state)
	submission := validSubmission()
	id, _ := submission.Source.RequestID()
	// Create valid resource storage with invalid domain content. Hash checking
	// alone cannot establish that the stored bytes describe the requested ID.
	if _, _, err := store.requests.Create(t.Context(), queue.Key{Kind: Kind, ID: id}, []byte(`{"ID":"different"}`), jsontext.Value(`{}`)); err != nil {
		t.Fatal(err)
	}
	factory := &dispatchQueue{}
	if _, err := store.Dispatch(t.Context(), queue.Key{Kind: Kind, ID: id}, factory, &testIndex{}); err == nil {
		t.Fatal("corrupt request dispatched")
	}
	if len(factory.keys) != 0 {
		t.Fatal("corrupt request reached factory queue")
	}

	if pending, err := state.Pending(t.Context(), 100); err != nil || len(pending) != 1 {
		t.Fatalf("corrupt request notification silently removed: %+v %v", pending, err)
	}
}

func TestResourceAcceptanceRejectsInvalidSubmissionWithoutWork(t *testing.T) {
	state := state.New()
	store := openResourceStore(t, objects.New(), state)
	submission := validSubmission()
	submission.ReceiptMode = "unknown"
	if _, err := store.Accept(t.Context(), submission); err == nil {
		t.Fatal("invalid submission accepted")
	}
	if pending, err := state.Pending(t.Context(), 100); err != nil || len(pending) != 0 {
		t.Fatalf("invalid submission created work: %+v %v", pending, err)
	}
}

func FuzzStoredResourceRequest(f *testing.F) {
	submission := validSubmission()
	id, _ := submission.Source.RequestID()
	snapshot, _ := json.Marshal(Request{ID: id, Submission: submission})
	f.Add(id, snapshot)
	f.Add("", []byte(`null`))
	f.Add(id, []byte(`{"ID":"different"}`))
	f.Fuzz(func(t *testing.T, id string, snapshot []byte) {
		if len(snapshot) > content.MaxBytes {
			return
		}
		request, err := decodeResourceRequest(id, snapshot)
		if err == nil && (request.ID != id || request.Validate() != nil) {
			t.Fatal("invalid stored input crossed the request boundary")
		}
	})
}
