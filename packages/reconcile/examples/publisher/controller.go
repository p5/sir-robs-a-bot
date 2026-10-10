package publisher

import (
	"context"
	"errors"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

// Controller implements workflow policy through the public reconciler interface.
// Delay stands in for an asynchronous provider's completion time.
type Controller struct {
	Application Application
	Delay       time.Duration
}

func (controller Controller) Reconcile(ctx context.Context, key datastore.Key) (reconcile.Result, error) {
	if key.Kind != Kind {
		return reconcile.Result{}, reconcile.Permanent(errors.New("unsupported resource kind"))
	}
	doc, err := controller.Application.Get(ctx, key.ID)
	if err != nil {
		return reconcile.Result{}, err
	}
	if doc.ObservedRevision == doc.Revision {
		return reconcile.Result{}, nil
	}
	if doc.Body == "" {
		return reconcile.Result{}, reconcile.Permanent(errors.New("document body is empty"))
	}
	ready, err := controller.Application.ensurePublication(ctx, doc, controller.Delay)
	if err != nil {
		return reconcile.Result{}, err
	}
	if !ready {
		return reconcile.Result{Again: true, After: controller.Delay, ProtectDelay: true}, nil
	}
	observed, err := controller.Application.observe(ctx, doc)
	if err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{Again: !observed}, nil
}
