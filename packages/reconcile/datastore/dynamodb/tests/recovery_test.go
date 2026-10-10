package dynamodbtests

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	adapter "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/dynamodb"
)

// runInterruptedController acknowledges an enqueue and claim, then exits without
// completing or cleaning them up. The parent must recover using a new client.
func runInterruptedController() int {
	endpoint = os.Getenv("FACTORY_DYNAMODB_ENDPOINT")
	store, err := adapter.New(newClient(), adapter.Config{Table: table, Namespace: os.Getenv("FACTORY_DYNAMODB_NAMESPACE")})
	if err != nil {
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := datastore.Key{Kind: "fixture", ID: "interrupted"}
	if err := store.Enqueue(ctx, key); err != nil {
		return 2
	}
	if _, err := store.Claim(ctx, []string{key.Kind}, time.Second); err != nil {
		return 3
	}
	return 77
}

func TestInterruptedControllerRecovery(t *testing.T) {
	_, _, config, clock := newFixture(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), executable)
	child.Env = append(os.Environ(), "FACTORY_DYNAMODB_CHILD=1", "FACTORY_DYNAMODB_ENDPOINT="+endpoint, "FACTORY_DYNAMODB_NAMESPACE="+config.Namespace)
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 77 {
		t.Fatalf("controller did not reach acknowledged claim: %v %s", err, output)
	}
	// The child used wall time. Place the reopened adapter beyond its lease.
	clock.milliseconds.Store(time.Now().Add(2 * time.Second).UnixMilli())
	store, err := adapter.New(newClient(), config)
	if err != nil {
		t.Fatal(err)
	}
	claim := claimKey(t, store)
	if claim.Fence != 2 || claim.Key.ID != "interrupted" {
		t.Fatalf("recovered claim: %+v", claim)
	}
	if err := store.Commit(t.Context(), claim, datastore.Completion{}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalServiceRestartPreservesAcknowledgedWrites(t *testing.T) {
	store, _, config, clock := newFixture(t)
	key := enqueueKey(t, store, "restart")
	previous := claimKey(t, store)
	// No tests run in parallel. Restart the same disk-backed container, retaining
	// its writable layer. This tests the emulator, not AWS durability guarantees.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if output, err := command(ctx, "restart", "--time", "10", containerName); err != nil {
		t.Fatalf("restart local service: %v %s", err, output)
	}
	if err := waitReady(ctx, newClient()); err != nil {
		t.Fatal(err)
	}
	reopened, err := adapter.New(newClient(), config)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := reopened.Get(t.Context(), key)
	if err != nil || !resource.Pending {
		t.Fatalf("acknowledged enqueue lost: %+v %v", resource, err)
	}
	clock.advance(2 * time.Minute)
	next := claimKey(t, reopened)
	if next.Fence <= previous.Fence {
		t.Fatal("restart reused an ownership fence")
	}
	if err := store.Commit(t.Context(), previous, datastore.Completion{}); !errors.Is(err, datastore.ErrLeaseLost) {
		t.Fatalf("old owner after restart: %v", err)
	}
}
