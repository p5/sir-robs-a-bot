package intake

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"time"
)

// Acknowledger belongs to a provider adapter. It binds an authenticated connection
// to an idempotent receipt operation. It receives the immutable accepted snapshot.
type Acknowledger interface {
	Connection() Connection
	Acknowledge(context.Context, Request) error
}

// DeliveryDisposition tells intake whether a provider operation can be retried.
type DeliveryDisposition string

const (
	DeliveryRetry     DeliveryDisposition = ""
	DeliveryTerminal  DeliveryDisposition = "terminal"
	DeliveryUncertain DeliveryDisposition = "uncertain"
)

// RetryScope determines whether a delay protects one receipt or its connection.
type RetryScope string

const (
	RetryRequest    RetryScope = ""
	RetryConnection RetryScope = "connection"
)

// DeliveryError communicates provider retry policy without exposing API types.
// Connection-scoped delays persist across delivery passes and process restarts.
type DeliveryError struct {
	Err        error
	RetryAfter time.Duration
	RetryScope RetryScope
	// Uncertain means delivery may have succeeded. Both stopped dispositions need operator recovery.
	Disposition DeliveryDisposition
}

func (err *DeliveryError) Error() string { return "provider acknowledgement failed" }
func (err *DeliveryError) Unwrap() error { return err.Err }

// AcknowledgeOne locks one due obligation for the authenticated account. Holding
// the transaction across delivery excludes concurrent senders. A lost reply or
// commit can repeat delivery; the provider must make its receipt operation idempotent.
// Failed delivery commits its retry time without changing the accepted request.
func (store Store) AcknowledgeOne(ctx context.Context, connection Connection, deliver func(context.Context, Request) error) (bool, error) {
	if connection.Validate() != nil || deliver == nil {
		return false, errors.New("acknowledgement requires an account and sender")
	}
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// Serialize receipt delivery per connection and enforce its durable cooldown.
	var ready bool
	err = tx.QueryRowContext(ctx, `SELECT acknowledgement_not_before IS NULL OR acknowledgement_not_before <= clock_timestamp() FROM intake_connections WHERE provider=$1 AND account=$2 FOR NO KEY UPDATE SKIP LOCKED`, connection.Provider, connection.Account).Scan(&ready)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !ready {
		return false, nil
	}
	var id string
	var snapshot []byte
	err = tx.QueryRowContext(ctx, `SELECT a.request_id, r.snapshot
      FROM intake_acknowledgements a JOIN intake_requests r ON r.id=a.request_id
      WHERE r.provider=$1 AND r.account=$2 AND a.state='pending' AND a.next_attempt_at <= clock_timestamp()
      ORDER BY a.next_attempt_at,a.request_id FOR UPDATE OF a SKIP LOCKED LIMIT 1`, connection.Provider, connection.Account).Scan(&id, &snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var request Request
	if err := json.Unmarshal(snapshot, &request); err != nil || request.ID != id || request.Source.Connection != connection || request.Validate() != nil || request.ReceiptMode != ReceiptAsync {
		if _, err := tx.ExecContext(ctx, `UPDATE intake_acknowledgements SET state='quarantined', attempts=attempts+1, last_error='invalid stored request' WHERE request_id=$1`, id); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return true, errors.New("invalid stored acknowledgement request quarantined")
	}
	deliveryErr := deliver(ctx, request)
	if deliveryErr == nil {
		_, err = tx.ExecContext(ctx, `UPDATE intake_acknowledgements SET state='delivered', delivered_at=clock_timestamp(), attempts=attempts+1, last_error=NULL WHERE request_id=$1`, id)
	} else {
		delay := time.Minute
		diagnostic := "delivery failed"
		state := "pending"
		if failure, ok := errors.AsType[*DeliveryError](deliveryErr); ok {
			delay = max(delay, failure.RetryAfter)
			diagnostic = "provider delivery failed"
			if failure.RetryScope != RetryRequest && failure.RetryScope != RetryConnection {
				state = "quarantined"
				diagnostic = "invalid retry scope"
			}
			switch failure.Disposition {
			case DeliveryRetry:
			case DeliveryTerminal:
				state = "terminal"
			case DeliveryUncertain:
				state = "uncertain"
			default:
				state = "quarantined"
				diagnostic = "invalid delivery disposition"
			}
			if failure.RetryScope == RetryConnection {
				if _, err := tx.ExecContext(ctx, `UPDATE intake_connections SET acknowledgement_not_before=clock_timestamp()+($3 * interval '1 second') WHERE provider=$1 AND account=$2`, connection.Provider, connection.Account, delay.Seconds()); err != nil {
					return false, err
				}
			}
		}
		// Store only bounded, service-generated diagnostics, never response bodies.
		_, err = tx.ExecContext(ctx, `UPDATE intake_acknowledgements SET next_attempt_at=clock_timestamp()+($2 * interval '1 second'), attempts=attempts+1, last_error=$3, state=$4 WHERE request_id=$1`, id, delay.Seconds(), diagnostic, state)
	}
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, deliveryErr
}

// AcknowledgeDue isolates individual delivery failures while honoring API-wide
// rate limits. The caller supplies a deadline to bound each delivery pass.
func (store Store) AcknowledgeDue(ctx context.Context, sender Acknowledger) (int, error) {
	if sender == nil {
		return 0, errors.New("acknowledgement sender is required")
	}
	connection := sender.Connection()
	var failures []error
	delivered := 0
	for {
		found, err := store.AcknowledgeOne(ctx, connection, sender.Acknowledge)
		if err != nil {
			failures = append(failures, err)
			failure, rateLimited := errors.AsType[*DeliveryError](err)
			if rateLimited && failure.RetryScope == RetryConnection {
				// Put the API-wide failure first so callers honor its delay even
				// when an earlier source failed with a different HTTP status.
				return delivered, errors.Join(append([]error{err}, failures...)...)
			}
			if !found || ctx.Err() != nil {
				break
			}
		} else if found {
			delivered++
		}
		if !found {
			break
		}
	}
	return delivered, errors.Join(failures...)
}
