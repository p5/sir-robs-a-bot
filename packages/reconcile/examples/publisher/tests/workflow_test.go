package publishertests

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/examples/publisher"
	"github.com/prometheus/client_golang/prometheus"
)

func engineConfig() reconcile.Config {
	return reconcile.Config{Concurrency: 2, LeaseDuration: 2 * time.Second,
		CallTimeout: time.Second, PollInterval: 10 * time.Millisecond,
		RetryInitial: 500 * time.Millisecond, RetryMax: 500 * time.Millisecond,
		MaxFailures: 3, MaxAbandoned: 3, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func newEngine(t *testing.T, store datastore.Store, callback reconcile.Reconciler, metrics *reconcile.Metrics) *reconcile.Engine {
	t.Helper()
	config := engineConfig()
	config.Metrics = metrics
	engine, err := reconcile.New(store, map[string]reconcile.Reconciler{publisher.Kind: callback}, config)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func step(t *testing.T, engine *reconcile.Engine) {
	t.Helper()
	worked, err := engine.ReconcileOne(t.Context())
	if err != nil || !worked {
		t.Fatalf("dispatch worked=%t: %v", worked, err)
	}
}

func deliver(t *testing.T, app publisher.Application, store datastore.Store) {
	t.Helper()
	for {
		delivered, err := app.DeliverOne(t.Context(), store)
		if err != nil {
			t.Fatal(err)
		}
		if !delivered {
			return
		}
	}
}

func submit(t *testing.T, app publisher.Application, id, body string, priority uint32) {
	t.Helper()
	if _, err := app.Submit(t.Context(), id, body, priority); err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, check func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for !check() {
		select {
		case <-ctx.Done():
			t.Fatal("condition did not become true:", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func assertPublished(t *testing.T, app publisher.Application, id string, revision int64, body string) {
	t.Helper()
	doc, err := app.Get(t.Context(), id)
	if err != nil || doc.Revision != revision || doc.ObservedRevision != revision || doc.Body != body {
		t.Fatalf("unexpected document: %+v %v", doc, err)
	}
	var count int
	var output string
	err = app.DB.QueryRowContext(t.Context(), `SELECT count(*), min(body)
        FROM prototype_publications WHERE namespace=$1 AND id=$2 AND revision=$3`,
		app.Namespace, id, revision).Scan(&count, &output)
	if err != nil || count != 1 || output != body {
		t.Fatalf("publications count=%d body=%q: %v", count, output, err)
	}
}

type lostEnqueueReply struct{ datastore.Store }

func (store lostEnqueueReply) Enqueue(ctx context.Context, key datastore.Key, options ...datastore.EnqueueOptions) error {
	if err := store.Store.Enqueue(ctx, key, options...); err != nil {
		return err
	}
	return errors.New("enqueue accepted but reply lost")
}

func TestConsumerDeliveryAndScheduling(t *testing.T) {
	for _, backend := range []string{"postgres", "dynamodb"} {
		t.Run(backend, func(t *testing.T) {
			app, store := open(t, backend, "delivery-"+rand.Text())
			submit(t, app, "ordinary", "first", 1)
			submit(t, app, "urgent", "urgent", 4000000000)
			delivered, err := app.DeliverOne(t.Context(), lostEnqueueReply{store})
			if !delivered || err == nil {
				t.Fatal("lost acknowledgement was not reported")
			}
			var obligations int
			if err := app.DB.QueryRowContext(t.Context(), "SELECT count(*) FROM prototype_outbox WHERE namespace=$1", app.Namespace).Scan(&obligations); err != nil || obligations != 2 {
				t.Fatalf("outbox obligation lost: %d %v", obligations, err)
			}
			deliver(t, app, store)
			metrics := reconcile.NewMetrics()
			registry := prometheus.NewPedanticRegistry()
			registry.MustRegister(metrics)
			controller := publisher.Controller{Application: app, Delay: time.Second}
			var calls []string
			callback := reconcile.ReconcilerFunc(func(ctx context.Context, key datastore.Key) (reconcile.Result, error) {
				calls = append(calls, key.ID)
				return controller.Reconcile(ctx, key)
			})
			engine := newEngine(t, store, callback, metrics)
			step(t, engine)
			if calls[0] != "urgent" {
				t.Fatalf("priority ignored: %v", calls)
			}
			// A newer desired revision arrives after application progress but
			// before follow-up. Delivery must preserve the protected wait.
			submit(t, app, "urgent", "replacement", 4000000001)
			deliver(t, app, store)
			step(t, engine)
			if calls[1] != "ordinary" {
				t.Fatalf("enqueue bypassed protected wait: %v", calls)
			}
			if worked, err := engine.ReconcileOne(t.Context()); err != nil || worked {
				t.Fatalf("protected publications dispatched early: %t %v", worked, err)
			}
			eventually(t, func() bool {
				if _, err := engine.ReconcileOne(t.Context()); err != nil {
					t.Fatal(err)
				}
				doc, err := app.Get(t.Context(), "urgent")
				if err != nil {
					t.Fatal(err)
				}
				other, err := app.Get(t.Context(), "ordinary")
				if err != nil {
					t.Fatal(err)
				}
				return doc.ObservedRevision == 2 && other.ObservedRevision == 1
			})
			assertPublished(t, app, "urgent", 2, "replacement")
			assertPublished(t, app, "ordinary", 1, "first")
			families, err := registry.Gather()
			if err != nil || len(families) == 0 {
				t.Fatalf("host metrics missing: %v", err)
			}
			inspector, ok := store.(datastore.Inspector)
			if !ok {
				t.Fatal("adapter lacks inspection")
			}
			page, err := inspector.List(t.Context(), datastore.ListRequest{Kind: publisher.Kind, Limit: 10})
			if err != nil || len(page.Entries) != 2 {
				t.Fatalf("inspection: %+v %v", page, err)
			}
			for _, entry := range page.Entries {
				if entry.Item.Pending || !entry.LeaseUntil.IsZero() {
					t.Fatalf("completed work still active: %+v", entry)
				}
			}
		})
	}
}

func TestConsumerRetrySuspensionAndRedrive(t *testing.T) {
	for _, backend := range []string{"postgres", "dynamodb"} {
		t.Run(backend, func(t *testing.T) {
			app, store := open(t, backend, "retry-"+rand.Text())
			submit(t, app, "one", "valid", 1)
			deliver(t, app, store)
			controller := publisher.Controller{Application: app, Delay: time.Millisecond}
			var fail atomic.Bool
			fail.Store(true)
			callback := reconcile.ReconcilerFunc(func(ctx context.Context, key datastore.Key) (reconcile.Result, error) {
				if fail.Swap(false) {
					return reconcile.Result{}, errors.New("provider unavailable")
				}
				return controller.Reconcile(ctx, key)
			})
			engine := newEngine(t, store, callback, nil)
			step(t, engine)
			key := datastore.Key{Kind: publisher.Kind, ID: "one"}
			// Even a high-priority new event cannot bypass the retry floor.
			if err := store.Enqueue(t.Context(), key, datastore.EnqueueOptions{Priority: 99}); err != nil {
				t.Fatal(err)
			}
			if worked, err := engine.ReconcileOne(t.Context()); worked || err != nil {
				t.Fatalf("retry floor bypassed: %t %v", worked, err)
			}
			eventually(t, func() bool {
				if _, err := engine.ReconcileOne(t.Context()); err != nil {
					t.Fatal(err)
				}
				item, err := store.Get(t.Context(), key)
				if err != nil {
					t.Fatal(err)
				}
				return !item.Pending
			})
			assertPublished(t, app, "one", 1, "valid")
			submit(t, app, "one", "", 0)
			deliver(t, app, store)
			step(t, engine)
			item, err := store.Get(t.Context(), key)
			if err != nil || item.Pending || item.Failures != 1 || item.LastError == "" {
				t.Fatalf("permanent error not suspended: %+v %v", item, err)
			}
			submit(t, app, "one", "fixed", 0)
			deliver(t, app, store)
			if err := store.Redrive(t.Context(), key); err != nil {
				t.Fatal(err)
			}
			eventually(t, func() bool {
				if _, err := engine.ReconcileOne(t.Context()); err != nil {
					t.Fatal(err)
				}
				item, err := store.Get(t.Context(), key)
				if err != nil {
					t.Fatal(err)
				}
				return !item.Pending
			})
			assertPublished(t, app, "one", 3, "fixed")
			item, err = store.Get(t.Context(), key)
			if err != nil || item.Failures != 0 || item.LastError != "" {
				t.Fatalf("redrive/success retained failure: %+v %v", item, err)
			}
		})
	}
}

func TestConsumerConcurrentOutboxRelays(t *testing.T) {
	for _, backend := range []string{"postgres", "dynamodb"} {
		t.Run(backend, func(t *testing.T) {
			namespace := "relays-" + rand.Text()
			app, store := open(t, backend, namespace)
			otherApp, otherStore := open(t, backend, namespace)
			for index := range 16 {
				submit(t, app, fmt.Sprint(index), "body", 0)
			}
			var workers sync.WaitGroup
			errors := make(chan error, 2)
			for _, relay := range []struct {
				app   publisher.Application
				store datastore.Store
			}{{app, store}, {otherApp, otherStore}} {
				workers.Go(func() {
					for {
						delivered, err := relay.app.DeliverOne(t.Context(), relay.store)
						if err != nil {
							errors <- err
							return
						}
						if !delivered {
							return
						}
					}
				})
			}
			workers.Wait()
			close(errors)
			for err := range errors {
				t.Fatal(err)
			}
			for index := range 16 {
				if _, err := store.Get(t.Context(), datastore.Key{Kind: publisher.Kind, ID: fmt.Sprint(index)}); err != nil {
					t.Fatal(err)
				}
			}
			var count int
			if err := app.DB.QueryRowContext(t.Context(), "SELECT count(*) FROM prototype_outbox WHERE namespace=$1", namespace).Scan(&count); err != nil || count != 0 {
				t.Fatalf("undelivered obligations: %d %v", count, err)
			}
		})
	}
}

func TestConsumerInputChangesBeforeQueueCompletion(t *testing.T) {
	for _, backend := range []string{"postgres", "dynamodb"} {
		t.Run(backend, func(t *testing.T) {
			app, store := open(t, backend, "change-"+rand.Text())
			submit(t, app, "one", "old", 1)
			deliver(t, app, store)
			controller := publisher.Controller{Application: app, Delay: 100 * time.Millisecond}
			var changed bool
			callback := reconcile.ReconcilerFunc(func(ctx context.Context, key datastore.Key) (reconcile.Result, error) {
				result, err := controller.Reconcile(ctx, key)
				if err != nil || changed {
					return result, err
				}
				changed = true
				if _, err := app.Submit(ctx, key.ID, "new", 99); err != nil {
					return reconcile.Result{}, err
				}
				if _, err := app.DeliverOne(ctx, store); err != nil {
					return reconcile.Result{}, err
				}
				return result, nil
			})
			engine := newEngine(t, store, callback, nil)
			step(t, engine)
			item, err := store.Get(t.Context(), datastore.Key{Kind: publisher.Kind, ID: "one"})
			if err != nil || !item.Pending || item.Priority != 99 {
				t.Fatalf("active-call update lost: %+v %v", item, err)
			}
			eventually(t, func() bool {
				if _, err := engine.ReconcileOne(t.Context()); err != nil {
					t.Fatal(err)
				}
				doc, err := app.Get(t.Context(), "one")
				if err != nil {
					t.Fatal(err)
				}
				return doc.ObservedRevision == 2
			})
			assertPublished(t, app, "one", 2, "new")
		})
	}
}

type lostCompletionReply struct {
	datastore.Store
	lost bool
}

func (store *lostCompletionReply) Commit(ctx context.Context, claim datastore.Claim, completion datastore.Completion) error {
	if err := store.Store.Commit(ctx, claim, completion); err != nil {
		return err
	}
	if !store.lost {
		store.lost = true
		return errors.New("completion accepted but reply lost")
	}
	return nil
}

func TestConsumerLostCompletionReply(t *testing.T) {
	for _, backend := range []string{"postgres", "dynamodb"} {
		t.Run(backend, func(t *testing.T) {
			namespace := "completion-" + rand.Text()
			app, store := open(t, backend, namespace)
			submit(t, app, "one", "output", 1)
			deliver(t, app, store)
			controller := publisher.Controller{Application: app, Delay: 100 * time.Millisecond}
			engine := newEngine(t, &lostCompletionReply{Store: store}, controller, nil)
			if worked, err := engine.ReconcileOne(t.Context()); !worked || err == nil {
				t.Fatal("lost completion reply was not surfaced")
			}
			// A host restarts after Run/ReconcileOne reports a store failure.
			app, store = open(t, backend, namespace)
			engine = newEngine(t, store, publisher.Controller{Application: app, Delay: 100 * time.Millisecond}, nil)
			eventually(t, func() bool {
				if _, err := engine.ReconcileOne(t.Context()); err != nil {
					t.Fatal(err)
				}
				item, err := store.Get(t.Context(), datastore.Key{Kind: publisher.Kind, ID: "one"})
				if err != nil {
					t.Fatal(err)
				}
				return !item.Pending
			})
			assertPublished(t, app, "one", 1, "output")
		})
	}
}

func TestConsumerRejectsInvalidIdentityBeforeWriting(t *testing.T) {
	app, _ := open(t, "postgres", "invalid-"+rand.Text())
	if _, err := app.Submit(t.Context(), "", "body", 0); err == nil {
		t.Fatal("empty identity accepted")
	}
	var count int
	if err := app.DB.QueryRowContext(t.Context(), "SELECT count(*) FROM prototype_documents WHERE namespace=$1", app.Namespace).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid input changed application state: %d %v", count, err)
	}
	submit(t, app, "literal' sql --", "literal content", 0)
	doc, err := app.Get(t.Context(), "literal' sql --")
	if err != nil || doc.Body != "literal content" {
		t.Fatalf("opaque identity was not preserved: %+v %v", doc, err)
	}
}

func TestConsumerStaleObservationCannotAcknowledgeNewRevision(t *testing.T) {
	for _, backend := range []string{"postgres", "dynamodb"} {
		t.Run(backend, func(t *testing.T) {
			app, store := open(t, backend, "stale-"+rand.Text())
			submit(t, app, "one", "old", 1)
			deliver(t, app, store)
			controller := publisher.Controller{Application: app, Delay: time.Hour}
			// Persist the old immutable output and make it ready before racing
			// its observation with a new desired revision.
			if _, err := controller.Reconcile(t.Context(), datastore.Key{Kind: publisher.Kind, ID: "one"}); err != nil {
				t.Fatal(err)
			}
			if _, err := app.DB.ExecContext(t.Context(), `UPDATE prototype_publications
                SET ready_at = clock_timestamp() - interval '1 second' WHERE namespace=$1`, app.Namespace); err != nil {
				t.Fatal(err)
			}
			tx, err := app.DB.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var revision int64
			if err := tx.QueryRowContext(t.Context(), `SELECT revision FROM prototype_documents
                WHERE namespace=$1 AND id='one' FOR UPDATE`, app.Namespace).Scan(&revision); err != nil {
				t.Fatal(err)
			}
			engine := newEngine(t, store, controller, nil)
			finished := make(chan error, 1)
			go func() {
				_, err := engine.ReconcileOne(t.Context())
				finished <- err
			}()
			// The callback has read revision 1 and reached its conditional
			// observation write. PostgreSQL blocks that write on our row lock.
			eventually(t, func() bool {
				var blocked bool
				err := app.DB.QueryRowContext(t.Context(), `SELECT EXISTS (
                    SELECT 1 FROM pg_stat_activity WHERE datname=current_database()
                    AND wait_event_type='Lock' AND query LIKE '%UPDATE prototype_documents%'
                    AND pid <> pg_backend_pid())`).Scan(&blocked)
				if err != nil {
					t.Fatal(err)
				}
				return blocked
			})
			// Commit the desired update and outbox in the application's owning
			// transaction, before the old observation can acquire the row lock.
			if _, err := tx.ExecContext(t.Context(), `UPDATE prototype_documents
                SET revision=2, body='new' WHERE namespace=$1 AND id='one'`, app.Namespace); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(t.Context(), `INSERT INTO prototype_outbox (namespace,id,revision,priority)
                VALUES ($1,'one',2,99)`, app.Namespace); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("old observation did not finish")
			}
			doc, err := app.Get(t.Context(), "one")
			if err != nil || doc.Revision != 2 || doc.ObservedRevision != 0 {
				t.Fatalf("stale callback acknowledged new desired state: %+v %v", doc, err)
			}
			deliver(t, app, store)
			engine = newEngine(t, store, publisher.Controller{Application: app, Delay: time.Millisecond}, nil)
			eventually(t, func() bool {
				if _, err := engine.ReconcileOne(t.Context()); err != nil {
					t.Fatal(err)
				}
				doc, err := app.Get(t.Context(), "one")
				if err != nil {
					t.Fatal(err)
				}
				return doc.ObservedRevision == 2
			})
			assertPublished(t, app, "one", 2, "new")
		})
	}
}
