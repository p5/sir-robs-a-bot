// Package persistence wires deployment storage to the intake workflow.
package persistence

import (
	"context"
	"errors"

	"github.com/p5/sir-robs-a-bot/packages/reconcile"
	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
)

// AWS owns runtime wiring. Resource records remain the application authority;
// queues schedule their handlers, and Index is only a read projection.
type AWS struct {
	*intake.ResourceStore
	Owner   queue.Store
	Factory queue.Store
	Index   intake.RequestIndex
}

func (store *AWS) Accept(ctx context.Context, submission intake.Submission) (intake.Acceptance, error) {
	if _, err := store.Activation(ctx, submission.Source.Connection); err != nil {
		return intake.Acceptance{}, err
	}
	return store.ResourceStore.Accept(ctx, submission)
}

func (store *AWS) Migrate(context.Context) error {
	return errors.New("AWS storage is provisioned externally; migrate applies only to PostgreSQL")
}

func (store *AWS) List(ctx context.Context, connection intake.Connection) ([]string, error) {
	return store.Index.List(ctx, connection)
}

func (store *AWS) DeliverOne(ctx context.Context, _ queue.Store) (bool, error) {
	return store.ResourceStore.DeliverOne(ctx, intake.OwnerQueue{Queue: store.Owner})
}

func (store *AWS) DispatchEngine() (*reconcile.Engine, error) {
	return store.ResourceStore.DispatchEngine(store.Owner, store.Factory, store.Index)
}

// Drain advances notifications and due handlers until neither can progress. The
// supplied context bounds the pass; scheduled delays stay in the queue.
func (store *AWS) Drain(ctx context.Context, sender intake.Acknowledger) (int, error) {
	dispatch, err := store.DispatchEngine()
	if err != nil {
		return 0, err
	}
	engines := []*reconcile.Engine{dispatch}
	if sender != nil {
		effects, err := store.ReceiptEngine(store.Owner, sender)
		if err != nil {
			return 0, err
		}
		engines = append(engines, effects)
	}
	count := 0
	for {
		progressed := false
		found, err := store.DeliverOne(ctx, nil)
		if err != nil {
			return count, err
		}
		progressed = found
		for _, engine := range engines {
			found, err := engine.ReconcileOne(ctx)
			if err != nil {
				return count, err
			}
			if found {
				progressed = true
				count++
			}
		}
		if !progressed {
			return count, nil
		}
	}
}

func (store *AWS) AcknowledgeDue(ctx context.Context, sender intake.Acknowledger) (int, error) {
	if sender == nil {
		return 0, errors.New("receipt sender required")
	}
	counter := &countingSender{Acknowledger: sender}
	_, err := store.Drain(ctx, counter)
	return counter.delivered, err
}

func (store *AWS) RedriveAcknowledgement(ctx context.Context, connection intake.Connection, id string) error {
	if err := store.ResourceStore.RedriveAcknowledgement(ctx, connection, id); err != nil {
		return err
	}
	key := queue.Key{Kind: intake.ReceiptKind, ID: id}
	if err := store.Owner.Enqueue(ctx, key); err != nil {
		return err
	}
	return store.Owner.Redrive(ctx, key)
}

// countingSender reports successful effects, rather than internal queue callbacks.
type countingSender struct {
	intake.Acknowledger
	delivered int
}

func (sender *countingSender) Acknowledge(ctx context.Context, request intake.Request) error {
	err := sender.Acknowledger.Acknowledge(ctx, request)
	if err == nil {
		sender.delivered++
	}
	return err
}
func (sender *countingSender) ObserveAcknowledgement(ctx context.Context, request intake.Request) (bool, error) {
	observer, ok := sender.Acknowledger.(intake.ReceiptObserver)
	if !ok {
		return false, nil
	}
	observed, err := observer.ObserveAcknowledgement(ctx, request)
	if observed && err == nil {
		sender.delivered++
	}
	return observed, err
}
