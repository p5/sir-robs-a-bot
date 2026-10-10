package intake

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AcknowledgementStatus describes a durable receipt obligation. A missing record
// means the request selected inline or no acknowledgement at acceptance.
type AcknowledgementStatus struct {
	State               string
	Attempts            int64
	NextAttemptAt       time.Time
	DeliveredAt         *time.Time
	LastError           *string
	ConnectionNotBefore *time.Time
}

// Acknowledgement reads receipt status within the authenticated connection.
func (store Store) Acknowledgement(ctx context.Context, connection Connection, id string) (AcknowledgementStatus, error) {
	if connection.Validate() != nil {
		return AcknowledgementStatus{}, errors.New("invalid receipt connection")
	}
	var status AcknowledgementStatus
	err := store.DB.QueryRowContext(ctx, `SELECT a.state,a.attempts,a.next_attempt_at,a.delivered_at,a.last_error,c.acknowledgement_not_before
 FROM intake_acknowledgements a JOIN intake_requests r ON r.id=a.request_id
 JOIN intake_connections c ON c.provider=r.provider AND c.account=r.account
 WHERE r.id=$1 AND r.provider=$2 AND r.account=$3`, id, connection.Provider, connection.Account).Scan(&status.State, &status.Attempts, &status.NextAttemptAt, &status.DeliveredAt, &status.LastError, &status.ConnectionNotBefore)
	if errors.Is(err, sql.ErrNoRows) {
		return AcknowledgementStatus{}, errors.New("no receipt obligation for this connection and request")
	}
	return status, err
}

// RedriveAcknowledgement retries a stopped receipt after operator recovery.
// It preserves connection cooldowns and refuses completed or disabled receipts.
// Before redriving an uncertain delivery, establish whether its effect occurred.
func (store Store) RedriveAcknowledgement(ctx context.Context, connection Connection, id string) error {
	if connection.Validate() != nil {
		return errors.New("invalid receipt connection")
	}
	request, err := store.Get(ctx, id)
	if err != nil {
		return err
	}
	if request.Source.Connection != connection {
		return errors.New("receipt connection mismatch")
	}
	result, err := store.DB.ExecContext(ctx, `UPDATE intake_acknowledgements SET state='pending', next_attempt_at=clock_timestamp(), last_error=NULL WHERE request_id=$1 AND state IN ('terminal','uncertain','quarantined')`, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("receipt is not eligible for redrive")
	}
	return nil
}
