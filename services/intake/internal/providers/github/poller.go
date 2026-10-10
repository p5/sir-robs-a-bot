package github

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
)

// Poller is stateless between sweeps. PostgreSQL deduplicates overlapping
// controllers, partial sweeps, and restarts. Notification read flags are ignored.
type Poller struct {
	Client *Client
	Policy Policy
	Intake intake.Acceptor
}

type Sweep struct {
	Threads       int
	Accepted      int
	Existing      int
	FailedThreads int
	FailedSources int
	NextPoll      time.Duration `json:"-"`
}

func (poller Poller) Scan(ctx context.Context) (Sweep, error) {
	var sweep Sweep
	if err := poller.Policy.Validate(); err != nil {
		return sweep, err
	}
	if poller.Client == nil || poller.Intake == nil {
		return sweep, fmt.Errorf("notification client and request sink are required")
	}
	seen := make(map[string]bool)
	var failures []error
	var retryAfter time.Duration
	interval, err := poller.Client.Notifications(ctx, poller.Policy.Since.Add(-time.Second), func(notification Notification) error {
		// Notifications from distinct threads can reference the same conversation.
		if seen[notification.Subject.URL] {
			return nil
		}
		seen[notification.Subject.URL] = true
		sweep.Threads++
		sourceFailed := false
		sourceFailure := func(err error) error {
			if err == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if failure, ok := errors.AsType[*APIError](err); ok && failure.RateLimited {
				return err
			}
			sweep.FailedSources++
			sourceFailed = true
			failures = append(failures, err)
			return nil
		}
		conversationErr := poller.Client.Conversation(ctx, notification, func(sourceType string, source Source) error {
			request, accepted, err := poller.Policy.Candidate(notification.Repository, sourceType, source)
			if err != nil || !accepted {
				return sourceFailure(err)
			}
			submitterID, err := poller.Client.Submitter(ctx, sourceType, source)
			if err != nil {
				return sourceFailure(err)
			}
			if submitterID == poller.Policy.Account.ID || !poller.Policy.AllowedUsers[submitterID] {
				return nil
			}
			request.SubmittedBy = strconv.FormatInt(submitterID, 10)
			result, err := poller.Intake.Accept(ctx, request)
			if err != nil {
				return err
			}
			if result.Created {
				sweep.Accepted++
			} else {
				sweep.Existing++
			}
			return nil
		})
		if sourceFailed || conversationErr != nil {
			sweep.FailedThreads++
		}
		if conversationErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if failure, ok := errors.AsType[*APIError](conversationErr); ok && failure.RateLimited {
				failures = append([]error{conversationErr}, failures...)
			} else {
				failures = append(failures, conversationErr)
			}
			if failure, ok := errors.AsType[*APIError](conversationErr); ok {
				retryAfter = max(retryAfter, failure.RetryAfter)
				if failure.RateLimited {
					return conversationErr
				}
			}
		}
		return nil
	})
	sweep.NextPoll = max(interval, retryAfter)
	return sweep, errors.Join(append(failures, err)...)
}
