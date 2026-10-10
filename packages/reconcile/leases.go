package reconcile

import (
	"context"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

// reconcileWithLease joins the heartbeat before returning. Completion must not
// race with a renewal still in flight, and callbacks must stop before release.
func (engine *Engine) reconcileWithLease(ctx context.Context, reconciler Reconciler, claim datastore.Claim) (result Result, reconcileErr error, renewalErr error) {
	timeoutCtx, stopTimeout := context.WithTimeout(ctx, engine.config.CallTimeout)
	defer stopTimeout()
	callCtx, cancelCall := context.WithCancelCause(timeoutCtx)
	defer cancelCall(nil)
	leaseCtx, stopLease := context.WithCancel(callCtx)
	renewed := make(chan error, 1)
	go func() {
		err := engine.maintainLease(leaseCtx, claim)
		if err != nil {
			cancelCall(err)
		}
		renewed <- err
	}()
	result, reconcileErr = engine.reconcile(callCtx, reconciler, claim)
	stopLease()
	renewalErr = <-renewed
	return result, reconcileErr, renewalErr
}

// A renewal has at most one interval to finish. Any failure cancels the call;
// unknown outcomes cannot authorize continued processing. The datastore remains
// the authority for fencing even if a paused process misses this local deadline.
func (engine *Engine) maintainLease(ctx context.Context, claim datastore.Claim) error {
	interval := engine.config.LeaseDuration / 3
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			renewalCtx, cancel := context.WithTimeout(ctx, interval)
			started := time.Now()
			err := engine.store.Renew(renewalCtx, claim, engine.config.LeaseDuration)
			engine.observe(claim.Key.Kind, "renew", started, err)
			cancel()
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
}
