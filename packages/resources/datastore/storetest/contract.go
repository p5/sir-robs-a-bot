// Package storetest defines the resource adapter's operation contract.
package storetest

import (
	"encoding/json/jsontext"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/resources/content"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore"
)

type Fixture struct {
	Store  datastore.Store
	Reopen func() datastore.Store
}

func Run(t *testing.T, open func(*testing.T) Fixture) {
	t.Helper()
	t.Run("first committed observation wins", func(t *testing.T) {
		fixture := open(t)
		record := initial()
		first, created, err := fixture.Store.Create(t.Context(), record)
		if err != nil || !created {
			t.Fatalf("create: %+v %v %v", first, created, err)
		}
		record.State = []byte(`{"changed":true}`)
		record.Content.SHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		duplicate, created, err := fixture.Reopen().Create(t.Context(), record)
		if err != nil || created || string(duplicate.State) != string(first.State) || duplicate.Content != first.Content {
			t.Fatalf("duplicate replaced winner: %+v %v %v", duplicate, created, err)
		}
	})
	t.Run("state and delivery survive reopen", func(t *testing.T) {
		fixture := open(t)
		record := initial()
		if _, _, err := fixture.Store.Create(t.Context(), record); err != nil {
			t.Fatal(err)
		}
		deliveries, err := fixture.Reopen().Pending(t.Context(), 100)
		if err != nil || len(deliveries) != 1 {
			t.Fatalf("pending: %+v %v", deliveries, err)
		}
		updated, err := fixture.Store.Update(t.Context(), record.Key, 1, []byte(`{"phase":"done"}`))
		if err != nil || updated.Version != 2 || updated.Content != record.Content {
			t.Fatalf("update: %+v %v", updated, err)
		}
		if err := fixture.Reopen().Delivered(t.Context(), deliveries[0]); err != nil {
			t.Fatal(err)
		}
		pending, err := fixture.Store.Pending(t.Context(), 100)
		if err != nil || len(pending) != 1 || pending[0].Version != 2 {
			t.Fatalf("stale ack lost newer work: %+v %v", pending, err)
		}
		if err := fixture.Store.Delivered(t.Context(), pending[0]); err != nil {
			t.Fatal(err)
		}
		pending, err = fixture.Reopen().Pending(t.Context(), 100)
		if err != nil || len(pending) != 0 {
			t.Fatalf("delivery not removed: %+v %v", pending, err)
		}
		if _, _, err := fixture.Store.Create(t.Context(), record); err != nil {
			t.Fatal(err)
		}
		pending, err = fixture.Store.Pending(t.Context(), 100)
		if err != nil || len(pending) != 0 {
			t.Fatal("duplicate recreated delivery")
		}
	})
	t.Run("concurrent update has one winner", func(t *testing.T) {
		fixture := open(t)
		record := initial()
		if _, _, err := fixture.Store.Create(t.Context(), record); err != nil {
			t.Fatal(err)
		}
		var wins atomic.Int32
		var group sync.WaitGroup
		for range 8 {
			group.Go(func() {
				_, err := fixture.Reopen().Update(t.Context(), record.Key, 1, []byte(`{"phase":"done"}`))
				if err == nil {
					wins.Add(1)
				} else if !errors.Is(err, datastore.ErrConflict) {
					t.Errorf("update: %v", err)
				}
			})
		}
		group.Wait()
		if wins.Load() != 1 {
			t.Fatalf("update winners: %d", wins.Load())
		}
	})
	t.Run("invalid input creates no obligation", func(t *testing.T) {
		fixture := open(t)
		record := initial()
		record.State = jsontext.Value(`broken`)
		if _, _, err := fixture.Store.Create(t.Context(), record); err == nil {
			t.Fatal("accepted invalid state")
		}
		pending, err := fixture.Store.Pending(t.Context(), 100)
		if err != nil || len(pending) != 0 {
			t.Fatalf("invalid input created work: %+v %v", pending, err)
		}
		if _, err := fixture.Store.Pending(t.Context(), 101); err == nil {
			t.Fatal("accepted unbounded page")
		}
	})
}

func initial() datastore.Record {
	return datastore.Record{Key: queue.Key{Kind: "request", ID: "example"}, Version: 1,
		Content: content.Reference{SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 1}, State: []byte(`{"phase":"accepted"}`)}
}
