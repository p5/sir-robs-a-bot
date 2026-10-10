package publishertests

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/examples/publisher"
)

func runChild(mode string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := sql.Open("postgres", os.Getenv("PUBLISHER_TEST_DSN"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	app := publisher.Application{DB: db, Namespace: os.Getenv("PUBLISHER_TEST_NAMESPACE")}
	config := publisher.QueueConfig{
		Backend: os.Getenv("PUBLISHER_TEST_BACKEND"), Namespace: app.Namespace,
		Table: table, Endpoint: os.Getenv("PUBLISHER_TEST_ENDPOINT"),
		Region: "us-east-1", Credentials: localCredentials(),
	}
	store, err := config.Open(db)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	delay := 100 * time.Millisecond
	if mode == "crash" {
		delay = 2 * time.Second
	}
	controller := publisher.Controller{Application: app, Delay: delay}
	var callback reconcile.Reconciler = controller
	if mode == "crash" || mode == "crash-observed" {
		callback = reconcile.ReconcilerFunc(func(ctx context.Context, key datastore.Key) (reconcile.Result, error) {
			result, err := controller.Reconcile(ctx, key)
			if err != nil {
				return result, err
			}
			if mode == "crash-observed" {
				doc, err := app.Get(ctx, key.ID)
				if err != nil {
					return reconcile.Result{}, err
				}
				if doc.ObservedRevision != doc.Revision {
					return result, nil
				}
			}
			fmt.Println("application progress persisted")
			// Exit before the engine can acknowledge queue completion. Deferred
			// cleanup never runs, as with abrupt controller process loss.
			os.Exit(77)
			return result, nil
		})
	}
	engine, err := reconcile.New(store, map[string]reconcile.Reconciler{publisher.Kind: callback}, engineConfig())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if mode == "crash" {
		worked, err := engine.ReconcileOne(ctx)
		fmt.Fprintf(os.Stderr, "crash child returned unexpectedly: %t %v\n", worked, err)
		return 1
	}
	fmt.Println("ready")
	if err := engine.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func childCommand(ctx context.Context, mode, backend, namespace string) *exec.Cmd {
	command := exec.CommandContext(ctx, os.Args[0])
	command.Env = append(os.Environ(), "PUBLISHER_TEST_CHILD="+mode,
		"PUBLISHER_TEST_DSN="+dsn, "PUBLISHER_TEST_ENDPOINT="+endpoint,
		"PUBLISHER_TEST_BACKEND="+backend, "PUBLISHER_TEST_NAMESPACE="+namespace)
	return command
}

func startWorker(t *testing.T, ctx context.Context, backend, namespace string) {
	t.Helper()
	command := childCommand(ctx, "worker", backend, namespace)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Signal(syscall.SIGTERM)
		if err := command.Wait(); err != nil {
			t.Errorf("worker shutdown: %v", err)
		}
	})
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatal("worker did not report readiness:", scanner.Err())
	}
}

func TestConsumerProcessRecovery(t *testing.T) {
	for _, backend := range []string{"postgres", "dynamodb"} {
		for _, mode := range []string{"crash", "crash-observed"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				namespace := "process-" + rand.Text()
				app, store := open(t, backend, namespace)
				submit(t, app, "interrupted", "durable output", 100)
				deliver(t, app, store)
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				t.Cleanup(cancel)
				output, err := childCommand(ctx, mode, backend, namespace).CombinedOutput()
				exit, ok := errors.AsType[*exec.ExitError](err)
				if !ok || exit.ExitCode() != 77 {
					t.Fatalf("controller did not interrupt at the intended boundary: %v %s", err, output)
				}
				doc, err := app.Get(t.Context(), "interrupted")
				expectedObserved := int64(0)
				if mode == "crash-observed" {
					expectedObserved = 1
				}
				if err != nil || doc.ObservedRevision != expectedObserved {
					t.Fatalf("crash missed the pending-effect boundary: %+v %v", doc, err)
				}
				var count int
				if err := app.DB.QueryRowContext(t.Context(), "SELECT count(*) FROM prototype_publications WHERE namespace=$1", namespace).Scan(&count); err != nil || count != 1 {
					t.Fatalf("progress was not durable before exit: %d %v", count, err)
				}
				for index := range 16 {
					submit(t, app, fmt.Sprint(index), "other output", 1)
				}
				deliver(t, app, store)
				// Independent OS processes use independent pools and adapter clients.
				startWorker(t, ctx, backend, namespace)
				startWorker(t, ctx, backend, namespace)
				eventually(t, func() bool {
					var remaining int
					if err := app.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM prototype_documents
                    WHERE namespace=$1 AND observed_revision <> revision`, namespace).Scan(&remaining); err != nil {
						t.Fatal(err)
					}
					if remaining != 0 {
						return false
					}
					page, err := store.(datastore.Inspector).List(t.Context(), datastore.ListRequest{Kind: publisher.Kind, Limit: 100})
					if err != nil {
						t.Fatal(err)
					}
					if len(page.Entries) != 17 {
						t.Fatalf("lost queue records: %d", len(page.Entries))
					}
					for _, entry := range page.Entries {
						if entry.Item.Pending || !entry.LeaseUntil.IsZero() {
							return false
						}
					}
					return true
				})
				assertPublished(t, app, "interrupted", 1, "durable output")
				if err := app.DB.QueryRowContext(t.Context(), "SELECT count(*) FROM prototype_publications WHERE namespace=$1", namespace).Scan(&count); err != nil || count != 17 {
					t.Fatalf("duplicate or missing effects after recovery: %d %v", count, err)
				}
			})
		}
	}
}
