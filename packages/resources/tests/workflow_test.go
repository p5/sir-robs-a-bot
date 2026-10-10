package resourcestests

import (
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile"
	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	queuedynamodb "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/dynamodb"
	queuememory "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/memory"
	"github.com/p5/sir-robs-a-bot/packages/resources"
	"github.com/p5/sir-robs-a-bot/packages/resources/content"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore/storetest"
	example "github.com/p5/sir-robs-a-bot/packages/resources/examples/request-run"
)

func TestDynamoDBContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) storetest.Fixture {
		namespace := rand.Text()
		return storetest.Fixture{Store: openState(t, namespace), Reopen: func() datastore.Store { return openState(t, namespace) }}
	})
}

func TestS3ConditionalUploadAndIntegrity(t *testing.T) {
	objects, fixture := openObjects(t, "owner")
	repository, _ := content.New(objects)
	ref, err := repository.Put(t.Context(), []byte("original"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Put(t.Context(), []byte("original")); err != nil {
		t.Fatal("conditional retry:", err)
	}
	fixture.mu.Lock()
	fixture.objects["/snapshots/owner/"+ref.Key()] = []byte("changed")
	fixture.mu.Unlock()
	if _, err := repository.Get(t.Context(), ref); !errors.Is(err, content.ErrCorrupt) {
		t.Fatalf("corrupt read: %v", err)
	}
	if _, err := repository.Put(t.Context(), []byte("original")); !errors.Is(err, content.ErrCorrupt) {
		t.Fatalf("corrupt collision: %v", err)
	}
}

type lostQueueCommit struct {
	queue.Store
	fail bool
}

func (store *lostQueueCommit) Commit(ctx context.Context, claim queue.Claim, completion queue.Completion) error {
	if store.fail {
		store.fail = false
		return errors.New("injected queue completion failure")
	}
	return store.Store.Commit(ctx, claim, completion)
}

func TestSeparateOwnersRecoverProgressBeforeQueueCompletion(t *testing.T) {
	objects, _ := openObjects(t, "request-owner")
	runObjects, _ := openObjects(t, "run-owner")
	requests, err := resources.New(objects, openState(t, "requests-"+rand.Text()))
	if err != nil {
		t.Fatal(err)
	}
	runs, err := resources.New(runObjects, openState(t, "runs-"+rand.Text()))
	if err != nil {
		t.Fatal(err)
	}
	app := example.Application{Requests: requests, Runs: runs}
	if _, _, err := app.Submit(t.Context(), "one", "inspect this request"); err != nil {
		t.Fatal(err)
	}
	// A fresh application wrapper uses the same durable resources after restart.
	restarted := example.Application{Requests: requests, Runs: runs}
	requestKey := queue.Key{Kind: example.RequestKind, ID: "one"}
	if _, err := restarted.RequestReconciler(t.Context(), requestKey); err != nil {
		t.Fatal(err)
	}
	runKey := queue.Key{Kind: example.RunKind, ID: "one"}
	before, _, err := runs.Get(t.Context(), runKey)
	if err != nil {
		t.Fatal(err)
	}
	// Fail queue completion in the claim that actually persists the run result.
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	target := &lostQueueCommit{Store: queuememory.New(queuememory.Config{Clock: func() time.Time { return time.Unix(0, clock.Load()) }}), fail: true}
	engine, err := reconcile.New(target, map[string]reconcile.Reconciler{example.RunKind: reconcile.ReconcilerFunc(restarted.RunReconciler)}, reconcile.Config{
		Concurrency: 1, LeaseDuration: time.Minute, CallTimeout: time.Second, PollInterval: time.Second, RetryInitial: time.Second, RetryMax: time.Minute, MaxFailures: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.DeliverOne(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ReconcileOne(t.Context()); err == nil {
		t.Fatal("completion failure was not observed")
	}
	saved, _, err := runs.Get(t.Context(), runKey)
	if err != nil || saved.Version != before.Version+1 {
		t.Fatalf("callback did not persist progress before failed completion: %+v %v", saved, err)
	}
	// Recover the expired claim through the real engine, without a timing sleep.
	clock.Add(int64(2 * time.Minute))
	found, err := engine.ReconcileOne(t.Context())
	if err != nil || !found {
		t.Fatalf("expired claim recovery: %v %v", found, err)
	}
	after, _, err := runs.Get(t.Context(), runKey)
	if err != nil || after.Version != saved.Version {
		t.Fatal("queue completion failure changed durable progress")
	}

}

func TestExecutablePrototype(t *testing.T) {
	binary := os.Getenv("RESOURCE_PROTOTYPE_BINARY")
	if binary == "" {
		t.Fatal("RESOURCE_PROTOTYPE_BINARY must name the built executable; run the project verifier")
	}
	command := exec.CommandContext(t.Context(), binary, "demo", "inspect this request")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("prototype: %v %s", err, output)
	}
	// Lifecycle logs use stderr, so select the final JSON record from combined output.
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var record datastore.Record
	if json.Unmarshal([]byte(lines[len(lines)-1]), &record) != nil || !strings.Contains(string(record.State), `"complete"`) {
		t.Fatalf("prototype did not converge: %s", output)
	}
}

func TestExecutableRejectsEmptyInstruction(t *testing.T) {
	binary := os.Getenv("RESOURCE_PROTOTYPE_BINARY")
	if binary == "" {
		t.Fatal("RESOURCE_PROTOTYPE_BINARY is required")
	}
	output, err := exec.CommandContext(t.Context(), binary, "demo", "").CombinedOutput()
	if err == nil || !strings.Contains(string(output), "instruction is required") {
		t.Fatalf("invalid instruction: %v %s", err, output)
	}
}

func TestDynamoDBRequestRunPipeline(t *testing.T) {
	objects, _ := openObjects(t, "requests")
	runObjects, _ := openObjects(t, "runs")
	requests, _ := resources.New(objects, openState(t, "pipeline-requests-"+rand.Text()))
	runs, _ := resources.New(runObjects, openState(t, "pipeline-runs-"+rand.Text()))
	app := example.Application{Requests: requests, Runs: runs}
	first, created, err := app.Submit(t.Context(), "one", "original instruction")
	if err != nil || !created {
		t.Fatalf("submit: %+v %v %v", first, created, err)
	}
	target, err := queuedynamodb.New(newClient(), queuedynamodb.Config{Table: table, Namespace: "pipeline-queue-" + rand.Text()})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := reconcile.New(target, map[string]reconcile.Reconciler{
		example.RequestKind: reconcile.ReconcilerFunc(app.RequestReconciler),
		example.RunKind:     reconcile.ReconcilerFunc(app.RunReconciler),
	}, reconcile.Config{Concurrency: 1, LeaseDuration: time.Minute, CallTimeout: 10 * time.Second,
		PollInterval: time.Second, RetryInitial: time.Second, RetryMax: time.Minute, MaxFailures: 3})
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		for _, owner := range []*resources.Repository{requests, runs} {
			for {
				found, err := owner.DeliverOne(t.Context(), target)
				if err != nil {
					t.Fatal(err)
				}
				if !found {
					break
				}
			}
		}
		found, err := engine.ReconcileOne(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			record, _, err := runs.Get(t.Context(), queue.Key{Kind: example.RunKind, ID: "one"})
			if err != nil {
				t.Fatal(err)
			}
			var state example.State
			if json.Unmarshal(record.State, &state) != nil || state.Phase != "complete" || state.InstructionSHA256 == "" {
				t.Fatalf("run did not complete: %+v", record)
			}
			duplicate, created, err := app.Submit(t.Context(), "one", "changed instruction")
			if err != nil || created || duplicate.Content != first.Content {
				t.Fatalf("completed request changed: %+v %v %v", duplicate, created, err)
			}
			return
		}
	}
	t.Fatal("durable request/run pipeline did not converge")
}
