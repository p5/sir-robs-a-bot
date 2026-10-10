package main

import (
	"context"
	"errors"
	"time"

	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/persistence"
)

// runRedrive resets scheduling only. Resource progress still decides whether an
// effect is eligible; completed receipts cannot become new deliveries here.
func runRedrive(ctx context.Context, store backend, kind, id string) error {
	target, ok := store.(*persistence.AWS)
	if !ok {
		return errors.New("redrive-work requires AWS resource storage")
	}
	operation, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	return target.Owner.Redrive(operation, queue.Key{Kind: kind, ID: id})
}
