package postgrestests

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/postgres"
)

func runInterruptedController() int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := sql.Open("postgres", os.Getenv("FACTORY_POSTGRES_DSN"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	store, err := postgres.New(db, os.Getenv("FACTORY_POSTGRES_NAMESPACE"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if _, err := store.Claim(ctx, []string{"fixture"}, 200*time.Millisecond); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Terminate without pool cleanup or any controller-side state write.
	os.Exit(77)
	return 1
}

func TestControllerProcessRecovery(t *testing.T) {
	store, db, namespace := newFixture(t)
	enqueueKey(t, store, "one")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0])
	child.Env = append(os.Environ(), "FACTORY_POSTGRES_CHILD=1", "FACTORY_POSTGRES_DSN="+dsn, "FACTORY_POSTGRES_NAMESPACE="+namespace)
	out, err := child.CombinedOutput()
	exitErr, ok := errors.AsType[*exec.ExitError](err)
	if !ok || exitErr.ExitCode() != 77 {
		t.Fatalf("child interruption: %v %s", err, out)
	}
	advanceStoreTime(t, db, 300*time.Millisecond)
	next, err := postgres.New(openTestPool(t), namespace)
	if err != nil {
		t.Fatal(err)
	}
	ownedClaim := claimKey(t, next, time.Minute)
	if ownedClaim.Fence != 2 {
		t.Fatalf("claim did not survive process exit: %+v", ownedClaim)
	}
	if err := next.Commit(t.Context(), ownedClaim, datastore.Completion{}); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseCrashRecovery(t *testing.T) {
	store, db, namespace := newFixture(t)
	key := enqueueKey(t, store, "one")
	ownedClaim := claimKey(t, store, time.Minute)
	if err := store.Commit(t.Context(), ownedClaim, datastore.Completion{Failure: "", Again: true, After: time.Minute}); err != nil {
		t.Fatal(err)
	}
	// Kill the owned server without shutdown, then reuse its existing data volume.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if out, err := command(ctx, "kill", "--signal", "KILL", containerName); err != nil {
		t.Fatalf("crash: %v %s", err, out)
	}
	if out, err := command(ctx, "start", containerName); err != nil {
		t.Fatalf("restart: %v %s", err, out)
	}
	if err := waitReady(ctx, db); err != nil {
		t.Fatal(err)
	}
	next, err := postgres.New(openTestPool(t), namespace)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := next.Get(t.Context(), key)
	if err != nil || !resource.Pending {
		t.Fatalf("lost acknowledged state: %+v %v", resource, err)
	}
	if _, err := next.Claim(t.Context(), []string{"fixture"}, time.Minute); !errors.Is(err, datastore.ErrNoWork) {
		t.Fatalf("lost durable delay: %v", err)
	}
}
