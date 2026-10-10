package reconciletests

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/internal/teststore"
)

func TestPermanentFailureRequiresWake(t *testing.T) {
	store := teststore.New()
	key := enqueueKey(t, store, "one")
	cause := errors.New("invalid request")
	failure := fmt.Errorf("validate: %w", reconcile.Permanent(cause))
	if !errors.Is(failure, cause) || reconcile.Permanent(nil) != nil {
		t.Fatal("permanent marker changed error identity")
	}
	engine := newTestEngine(t, store, reconcile.ReconcilerFunc(func(context.Context, datastore.Key) (reconcile.Result, error) {
		return reconcile.Result{}, failure
	}))
	reconcileOne(t, engine, true)
	store.Advance(24 * time.Hour)
	reconcileOne(t, engine, false)
	resource, err := store.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if resource.Pending || resource.Failures != 1 || !strings.Contains(resource.LastError, cause.Error()) {
		t.Fatalf("permanent error did not suspend first attempt: %+v", resource)
	}
	if err := store.Enqueue(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	reconcileOne(t, engine, true)
}

// cancelAfterCommit stops the polling host after an acknowledged checkpoint.
type cancelAfterCommit struct {
	datastore.Store
	cancel context.CancelFunc
}

func (store cancelAfterCommit) Commit(ctx context.Context, claim datastore.Claim, completion datastore.Completion) error {
	err := store.Store.Commit(ctx, claim, completion)
	if err == nil {
		store.cancel()
	}
	return err
}

func TestRunContinuesAfterLeaseLoss(t *testing.T) {
	model := teststore.New()
	key := enqueueKey(t, model, "one")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store := cancelAfterCommit{Store: model, cancel: cancel}
	calls := 0
	config := testConfig()
	config.Concurrency = 1
	engine, err := reconcile.New(store, map[string]reconcile.Reconciler{
		"fixture": reconcile.ReconcilerFunc(func(context.Context, datastore.Key) (reconcile.Result, error) {
			calls++
			if calls == 1 {
				model.Advance(config.LeaseDuration)
				if _, err := model.Claim(ctx, []string{"fixture"}, config.LeaseDuration); err != nil {
					t.Fatal(err)
				}
				model.Advance(config.LeaseDuration)
				return reconcile.Result{}, nil
			}
			return reconcile.Result{}, nil
		}),
	}, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("polling stopped before recovery: %v", err)
	}
	resource, err := model.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("lease loss prevented recovery: calls=%d resource=%+v", calls, resource)
	}
}

func TestCommitTelemetryDoesNotReportUncertainSuccess(t *testing.T) {
	for _, test := range []struct {
		name        string
		commitError error
		wantEvent   string
	}{
		{name: "acknowledged", wantEvent: "completed"},
		{name: "unknown outcome", commitError: errors.New("reply lost"), wantEvent: "commit_failed"},
		{name: "ownership lost", commitError: datastore.ErrLeaseLost, wantEvent: "lease_lost"},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := teststore.New()
			enqueueKey(t, model, "one")
			var logs bytes.Buffer
			config := testConfig()
			config.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			store := uncertainStore{Store: model, failure: test.commitError}
			engine, err := reconcile.New(store, map[string]reconcile.Reconciler{
				"fixture": reconcile.ReconcilerFunc(func(context.Context, datastore.Key) (reconcile.Result, error) {
					return reconcile.Result{}, nil
				}),
			}, config)
			if err != nil {
				t.Fatal(err)
			}
			_, err = engine.ReconcileOne(t.Context())
			if !errors.Is(err, test.commitError) {
				t.Fatalf("commit error = %v, want %v", err, test.commitError)
			}
			output := logs.String()
			if !strings.Contains(output, "event=claimed") || !strings.Contains(output, "event="+test.wantEvent) {
				t.Fatalf("missing lifecycle events: %s", output)
			}
			if strings.Contains(output, "private observation") || strings.Contains(output, "desired\"") {
				t.Fatalf("telemetry included resource bytes: %s", output)
			}
			if test.commitError != nil && strings.Contains(output, "event=completed") {
				t.Fatalf("uncertain completion reported success: %s", output)
			}
		})
	}
}

func TestConstructorRejectsNilReconcilerFunction(t *testing.T) {
	var callback reconcile.ReconcilerFunc
	_, err := reconcile.New(teststore.New(), map[string]reconcile.Reconciler{"fixture": callback}, testConfig())
	if err == nil {
		t.Fatal("constructor accepted a nil reconciler function")
	}
}
