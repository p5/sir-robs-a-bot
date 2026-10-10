package storetest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

// RunInspection tests the optional Inspector contract. Adapters that implement
// inspection must call this in addition to Run, with the same isolated fixtures.
func RunInspection(t *testing.T, factory func(*testing.T) Fixture) {
	t.Helper()
	open := func(t *testing.T) (Fixture, datastore.Inspector) {
		t.Helper()
		fixture := factory(t)
		inspector, ok := fixture.Store.(datastore.Inspector)
		if !ok {
			t.Fatal("adapter does not implement inspection")
		}
		return fixture, inspector
	}
	t.Run("ordered-pages-and-kind-isolation", func(t *testing.T) {
		fixture, inspector := open(t)
		ids := []string{"z", "A", "é", "a", "a/b", "a?x=1", "😀"}
		for _, id := range ids {
			if err := fixture.Store.Enqueue(t.Context(), datastore.Key{Kind: "inspect", ID: id}); err != nil {
				t.Fatal(err)
			}
		}
		if err := fixture.Store.Enqueue(t.Context(), datastore.Key{Kind: "other", ID: "hidden"}); err != nil {
			t.Fatal(err)
		}
		request := datastore.ListRequest{Kind: "inspect", Limit: 2}
		var got []string
		for pages := 0; ; pages++ {
			if pages > len(ids) {
				t.Fatal("inspection did not terminate")
			}
			page, err := inspector.List(t.Context(), request)
			if err != nil || len(page.Entries) > request.Limit {
				t.Fatalf("invalid page: %+v %v", page, err)
			}
			for _, entry := range page.Entries {
				if entry.Item.Key.Kind != request.Kind || !entry.Item.Pending || !entry.LeaseUntil.IsZero() || entry.DueAt.IsZero() {
					t.Fatalf("invalid entry: %+v", entry)
				}
				got = append(got, entry.Item.Key.ID)
			}
			if page.Next == "" {
				break
			}
			if page.Next <= request.After {
				t.Fatal("cursor did not advance")
			}
			request.After = page.Next
			inspector = fixture.Reopen().(datastore.Inspector)
		}
		slices.Sort(ids)
		if !slices.Equal(got, ids) {
			t.Fatalf("listed %q, want %q", got, ids)
		}
		page, err := inspector.List(t.Context(), datastore.ListRequest{Kind: "missing", Limit: 1})
		if err != nil || len(page.Entries) != 0 || page.Next != "" {
			t.Fatalf("missing kind: %+v %v", page, err)
		}
	})
	t.Run("observes-lifecycle-without-changing-ownership", func(t *testing.T) {
		fixture, inspector := open(t)
		key := datastore.Key{Kind: "inspect", ID: "one"}
		if err := fixture.Store.Enqueue(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		read := func() datastore.Entry {
			t.Helper()
			page, err := inspector.List(t.Context(), datastore.ListRequest{Kind: key.Kind, Limit: 1})
			if err != nil || len(page.Entries) != 1 {
				t.Fatalf("inspection: %+v %v", page, err)
			}
			return page.Entries[0]
		}
		claim, err := fixture.Store.Claim(t.Context(), []string{key.Kind}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		active := read()
		if !active.Item.Pending || active.LeaseUntil.IsZero() || !active.LeaseUntil.After(active.DueAt) {
			t.Fatalf("active entry: %+v", active)
		}
		if err := fixture.Store.Commit(t.Context(), claim, datastore.Completion{Failure: "needs operator", Stop: true}); err != nil {
			t.Fatal("inspection changed ownership", err)
		}
		suspended := read()
		if suspended.Item.Pending || suspended.Item.Failures != 1 || suspended.Item.LastError != "needs operator" || !suspended.LeaseUntil.IsZero() {
			t.Fatalf("suspended entry: %+v", suspended)
		}
		if err := fixture.Store.Redrive(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		claim, err = fixture.Store.Claim(t.Context(), []string{key.Kind}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.Store.Commit(t.Context(), claim, datastore.Completion{Again: true, After: time.Hour}); err != nil {
			t.Fatal(err)
		}
		delayed := read()
		if !delayed.Item.Pending || !delayed.LeaseUntil.IsZero() || !delayed.DueAt.After(active.LeaseUntil) || delayed.Item.LastError != "" {
			t.Fatalf("delayed entry: %+v", delayed)
		}
	})
	t.Run("cursor-need-not-exist", func(t *testing.T) {
		fixture, inspector := open(t)
		for _, id := range []string{"a", "c"} {
			if err := fixture.Store.Enqueue(t.Context(), datastore.Key{Kind: "inspect", ID: id}); err != nil {
				t.Fatal(err)
			}
		}
		page, err := inspector.List(t.Context(), datastore.ListRequest{Kind: "inspect", After: "b", Limit: 2})
		if err != nil || len(page.Entries) != 1 || page.Entries[0].Item.Key.ID != "c" {
			t.Fatalf("nonexistent cursor: %+v %v", page, err)
		}
	})
	t.Run("concurrent-inserts-do-not-rewind-traversal", func(t *testing.T) {
		fixture, inspector := open(t)
		enqueue := func(id string) {
			t.Helper()
			if err := fixture.Store.Enqueue(t.Context(), datastore.Key{Kind: "inspect", ID: id}); err != nil {
				t.Fatal(err)
			}
		}
		enqueue("a")
		enqueue("c")
		first, err := inspector.List(t.Context(), datastore.ListRequest{Kind: "inspect", Limit: 1})
		if err != nil || first.Next != "a" {
			t.Fatalf("first page: %+v %v", first, err)
		}
		enqueue("0")
		enqueue("b")
		next, err := inspector.List(t.Context(), datastore.ListRequest{Kind: "inspect", After: first.Next, Limit: 10})
		if err != nil || len(next.Entries) != 2 || next.Entries[0].Item.Key.ID != "b" || next.Entries[1].Item.Key.ID != "c" {
			t.Fatalf("continuation after inserts: %+v %v", next, err)
		}
	})
	t.Run("namespace-isolation", func(t *testing.T) {
		fixture, _ := open(t)
		_, other := open(t)
		if err := fixture.Store.Enqueue(t.Context(), datastore.Key{Kind: "inspect", ID: "private"}); err != nil {
			t.Fatal(err)
		}
		page, err := other.List(t.Context(), datastore.ListRequest{Kind: "inspect", Limit: 10})
		if err != nil || len(page.Entries) != 0 || page.Next != "" {
			t.Fatalf("namespace leak: %+v %v", page, err)
		}
	})
	t.Run("invalid-input-and-cancellation", func(t *testing.T) {
		_, inspector := open(t)
		for _, request := range []datastore.ListRequest{
			{Kind: "inspect", Limit: 0}, {Kind: "inspect", Limit: -1},
			{Kind: "inspect", Limit: 101}, {Kind: "", Limit: 1},
			{Kind: "inspect", After: "\x00", Limit: 1},
			{Kind: "inspect", After: "\xff", Limit: 1},
		} {
			if _, err := inspector.List(t.Context(), request); err == nil {
				t.Fatalf("invalid request accepted: %+v", request)
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := inspector.List(ctx, datastore.ListRequest{Kind: "inspect", Limit: 1}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled inspection: %v", err)
		}
	})
}
