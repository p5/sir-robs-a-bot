package resourceunit

import (
	"context"
	"errors"
	"testing"

	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/resources"
	"github.com/p5/sir-robs-a-bot/packages/resources/content"
	contentmemory "github.com/p5/sir-robs-a-bot/packages/resources/content/memory"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore"
	statememory "github.com/p5/sir-robs-a-bot/packages/resources/datastore/memory"
)

var interrupted = errors.New("injected interruption")

type failContent struct{ content.Store }

func (failContent) Put(context.Context, string, []byte) error { return interrupted }

type interruptedState struct {
	datastore.Store
	failBefore bool
	failAfter  bool
}

func (store *interruptedState) Create(ctx context.Context, record datastore.Record) (datastore.Record, bool, error) {
	if store.failBefore {
		return datastore.Record{}, false, interrupted
	}
	saved, created, err := store.Store.Create(ctx, record)
	if err == nil && store.failAfter {
		return datastore.Record{}, false, interrupted
	}
	return saved, created, err
}

type enqueueFunc func(context.Context, queue.Key) error

func (enqueue enqueueFunc) Enqueue(ctx context.Context, key queue.Key, _ ...queue.EnqueueOptions) error {
	return enqueue(ctx, key)
}

func newRepository(t *testing.T, objects content.Store, state datastore.Store) *resources.Repository {
	t.Helper()
	repository, err := resources.New(objects, state)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func TestUploadFailureCreatesNoResourceOrWork(t *testing.T) {
	state := statememory.New()
	repository := newRepository(t, failContent{contentmemory.New()}, state)
	key := queue.Key{Kind: "request", ID: "one"}
	if _, _, err := repository.Create(t.Context(), key, []byte("input"), []byte(`{}`)); !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	if _, err := state.Get(t.Context(), key); !errors.Is(err, datastore.ErrNotFound) {
		t.Fatal("upload failure accepted request")
	}
	pending, _ := state.Pending(t.Context(), 100)
	if len(pending) != 0 {
		t.Fatal("upload failure created notification")
	}
}

func TestUploadBeforeCommitCanRecoverWithDifferentObservation(t *testing.T) {
	objects := contentmemory.New()
	state := &interruptedState{Store: statememory.New(), failBefore: true}
	repository := newRepository(t, objects, state)
	key := queue.Key{Kind: "request", ID: "one"}
	if _, _, err := repository.Create(t.Context(), key, []byte("unaccepted"), []byte(`{}`)); !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	state.failBefore = false
	accepted, created, err := repository.Create(t.Context(), key, []byte("accepted"), []byte(`{}`))
	if err != nil || !created {
		t.Fatalf("recovery: %+v %v %v", accepted, created, err)
	}
	_, data, err := repository.Get(t.Context(), key)
	if err != nil || string(data) != "accepted" {
		t.Fatalf("unaccepted upload pinned input: %q %v", data, err)
	}
}

func TestUnknownCommitPreservesOriginalAndDelivery(t *testing.T) {
	state := &interruptedState{Store: statememory.New(), failAfter: true}
	repository := newRepository(t, contentmemory.New(), state)
	key := queue.Key{Kind: "request", ID: "one"}
	if _, _, err := repository.Create(t.Context(), key, []byte("original"), []byte(`{}`)); !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	state.failAfter = false
	_, created, err := repository.Create(t.Context(), key, []byte("changed"), []byte(`{}`))
	if err != nil || created {
		t.Fatalf("retry created another request: %v %v", created, err)
	}
	_, data, err := repository.Get(t.Context(), key)
	if err != nil || string(data) != "original" {
		t.Fatalf("lost accepted input: %q %v", data, err)
	}
	pending, _ := state.Pending(t.Context(), 100)
	if len(pending) != 1 {
		t.Fatal("unknown commit lost notification")
	}
}

func TestLostEnqueueReplyAndConcurrentUpdatePreserveWork(t *testing.T) {
	state := statememory.New()
	repository := newRepository(t, contentmemory.New(), state)
	key := queue.Key{Kind: "request", ID: "one"}
	if _, _, err := repository.Create(t.Context(), key, []byte("input"), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	calls := 0
	target := enqueueFunc(func(ctx context.Context, received queue.Key) error {
		calls++
		if received != key {
			t.Fatal("wrong resource key")
		}
		if calls == 1 {
			return interrupted
		}
		if calls == 2 {
			if _, err := repository.Update(ctx, key, 1, []byte(`{"phase":"changed"}`)); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	})
	if _, err := repository.DeliverOne(t.Context(), target); !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	for range 2 {
		found, err := repository.DeliverOne(t.Context(), target)
		if err != nil || !found {
			t.Fatalf("recovery: %v %v", found, err)
		}
	}
	found, err := repository.DeliverOne(t.Context(), target)
	if err != nil || found || calls != 3 {
		t.Fatalf("delivery state: found=%v calls=%d err=%v", found, calls, err)
	}
}
