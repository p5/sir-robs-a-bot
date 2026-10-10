package intake

import (
	"context"
	"encoding/json/v2"
	"errors"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile"
	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/resources"
)

// RequestIndex is a reconstructible read projection, not acceptance authority.
type RequestIndex interface {
	Put(context.Context, Request) error
	List(context.Context, Connection) ([]string, error)
}

// Dispatch independently advances the receipt handoff, read projection, and
// factory handoff. Failure in one path never prevents attempting another.
func (store *ResourceStore) Dispatch(ctx context.Context, key queue.Key, factory resources.Enqueuer, index RequestIndex) (reconcile.Result, error) {
	if key.Kind != Kind || factory == nil || index == nil {
		return reconcile.Result{}, errors.New("request dispatch requires kind, queue and index")
	}
	record, raw, err := store.requests.Get(ctx, key)
	if err != nil {
		return reconcile.Result{}, err
	}
	request, err := decodeResourceRequest(key.ID, raw)
	if err != nil {
		return reconcile.Result{}, err
	}
	var progress RequestProgress
	if json.Unmarshal(record.State, &progress) != nil || progress.Schema != 1 || (request.ReceiptMode != ReceiptAsync && !progress.ReceiptCreated) {
		return reconcile.Result{}, errors.New("invalid request progress")
	}
	changed := false
	var failures []error
	if !progress.ReceiptCreated {
		receipt := ReceiptProgress{Schema: 1, State: "pending", NextAttemptAt: store.now()}
		if err := store.createReceipt(ctx, request, receipt); err != nil {
			failures = append(failures, err)
		} else {
			progress.ReceiptCreated = true
			changed = true
		}
	}
	if !progress.Indexed {
		if err := index.Put(ctx, request); err != nil {
			failures = append(failures, err)
		} else {
			progress.Indexed = true
			changed = true
		}
	}
	if !progress.Dispatched {
		if err := factory.Enqueue(ctx, key); err != nil {
			failures = append(failures, err)
		} else {
			progress.Dispatched = true
			changed = true
		}
	}
	if changed {
		failures = append(failures, store.save(ctx, record, progress))
	}
	return reconcile.Result{}, errors.Join(failures...)
}

// OwnerQueue maps stored connection keys to their scoped effect handler. The
// resource notification still has one destination: the intake coordination queue.
type OwnerQueue struct {
	Queue resources.Enqueuer
}

func (target OwnerQueue) Enqueue(ctx context.Context, key queue.Key, options ...queue.EnqueueOptions) error {
	if target.Queue == nil {
		return errors.New("intake owner queue required")
	}
	switch key.Kind {
	case Kind, ReceiptKind:
	case ConnectionKind:
		// Routing depends only on the stable connection resource key. The
		// connection handler owns content validation, so one bad snapshot cannot
		// block notification delivery for unrelated requests.
		key.Kind = connectionEffectKind(key)
	default:
		return errors.New("unexpected resource kind in intake namespace")
	}
	return target.Queue.Enqueue(ctx, key, options...)
}

func workerConfig() reconcile.Config {
	return reconcile.Config{Concurrency: 4, LeaseDuration: time.Minute, CallTimeout: 45 * time.Second, PollInterval: time.Second, RetryInitial: time.Second, RetryMax: time.Minute, MaxFailures: 20}
}

func (store *ResourceStore) DispatchEngine(owner queue.Store, factory resources.Enqueuer, index RequestIndex) (*reconcile.Engine, error) {
	return reconcile.New(owner, map[string]reconcile.Reconciler{
		Kind: reconcile.ReconcilerFunc(func(ctx context.Context, key queue.Key) (reconcile.Result, error) {
			return store.Dispatch(ctx, key, factory, index)
		}),
		ReceiptKind: reconcile.ReconcilerFunc(store.AttachReceipt),
	}, workerConfig())
}

func (store *ResourceStore) ReceiptEngine(owner queue.Store, sender Acknowledger) (*reconcile.Engine, error) {
	if sender == nil || sender.Connection().Validate() != nil {
		return nil, errors.New("authenticated receipt sender required")
	}
	return reconcile.New(owner, map[string]reconcile.Reconciler{
		EffectKind(sender.Connection()): reconcile.ReconcilerFunc(func(ctx context.Context, key queue.Key) (reconcile.Result, error) {
			if key.ID != ConnectionKey(sender.Connection()).ID {
				return reconcile.Result{}, errors.New("receipt worker connection mismatch")
			}
			return store.ReconcileConnection(ctx, sender)
		}),
	}, workerConfig())
}
