package reconcile

import (
	"math/rand/v2"
	"time"
)

func (engine *Engine) retryDelay(previousFailures uint32) time.Duration {
	ceiling := engine.config.RetryInitial
	for range previousFailures {
		// Check before doubling to avoid duration overflow near the cap.
		if ceiling >= engine.config.RetryMax || ceiling > engine.config.RetryMax/2 {
			ceiling = engine.config.RetryMax
			break
		}
		ceiling *= 2
	}

	// Equal jitter spreads retries over the upper half of the backoff window.
	// Round the lower bound upward so a positive ceiling never yields zero.
	floor := ceiling - ceiling/2
	return floor + rand.N(ceiling-floor+1)
}
