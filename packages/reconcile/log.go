package reconcile

import (
	"context"
	"log/slog"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

// claimLogger adds ownership fields for queue lifecycle events.
func (engine *Engine) claimLogger(claim datastore.Claim) *slog.Logger {
	return engine.logger.With(
		"kind", claim.Key.Kind,
		"resource_id", claim.Key.ID,
		"fence", claim.Fence,
		"abandoned", claim.Abandoned,
		"failures", claim.Failures,
	)
}

func logCompletion(ctx context.Context, logger *slog.Logger, completion datastore.Completion, duration time.Duration) {
	switch {
	case completion.Stop:
		logger.WarnContext(ctx, "reconciliation suspension requested", "event", "suspension_requested", "duration", duration, "error", completion.Failure)
	case completion.Failure != "":
		logger.WarnContext(ctx, "reconciliation retry requested", "event", "retry_requested", "duration", duration, "after", completion.After, "error", completion.Failure)
	default:
		logger.InfoContext(ctx, "reconciliation completed", "event", "completed", "duration", duration, "again", completion.Again, "after", completion.After)
	}
}
