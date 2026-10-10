package intake

import (
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"math"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile"
	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/resources/content"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore"
)

type ReceiptInput struct {
	RequestID  string
	Connection Connection
}

type ReceiptProgress struct {
	Schema        uint8
	State         string
	Attempts      int64
	NextAttemptAt time.Time
	DeliveredAt   *time.Time
	LastError     *string
	LastAttempt   string
}

func (progress ReceiptProgress) validate() error {
	if progress.Schema != 1 || progress.Attempts < 0 || progress.NextAttemptAt.IsZero() || len(progress.LastAttempt) > 64 {
		return errors.New("invalid receipt progress")
	}
	if progress.LastError != nil && len(*progress.LastError) > 128 {
		return errors.New("invalid receipt diagnostic")
	}
	switch progress.State {
	case "pending", "delivered", "terminal", "uncertain", "quarantined":
	default:
		return errors.New("invalid receipt state")
	}
	if progress.State == "delivered" && (progress.DeliveredAt == nil || progress.DeliveredAt.IsZero()) {
		return errors.New("missing delivery time")
	}
	return nil
}

func (store *ResourceStore) createReceipt(ctx context.Context, request Request, progress ReceiptProgress) error {
	if request.Validate() != nil || request.ReceiptMode != ReceiptAsync || progress.validate() != nil {
		return errors.New("invalid receipt creation")
	}
	raw, err := json.Marshal(ReceiptInput{RequestID: request.ID, Connection: request.Source.Connection})
	if err != nil {
		return err
	}
	state, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	_, _, err = store.requests.Create(ctx, queue.Key{Kind: ReceiptKind, ID: request.ID}, raw, state)
	return err
}

func (store *ResourceStore) receipt(ctx context.Context, id string) (datastore.Record, ReceiptInput, ReceiptProgress, error) {
	record, raw, err := store.requests.Get(ctx, queue.Key{Kind: ReceiptKind, ID: id})
	if err != nil {
		return datastore.Record{}, ReceiptInput{}, ReceiptProgress{}, err
	}
	var input ReceiptInput
	var progress ReceiptProgress
	if json.Unmarshal(raw, &input) != nil || input.RequestID != id || input.Connection.Validate() != nil || json.Unmarshal(record.State, &progress) != nil || progress.validate() != nil {
		return datastore.Record{}, ReceiptInput{}, ReceiptProgress{}, errors.New("invalid stored receipt")
	}
	return record, input, progress, nil
}

func delayed(until, now time.Time) reconcile.Result {
	return reconcile.Result{Again: true, After: max(time.Microsecond, until.Sub(now)), ProtectDelay: true}
}

// AttachReceipt hands a due receipt to its connection. It never calls a provider.
// CAS selects one attempt; another receipt waits in the existing queue.
func (store *ResourceStore) AttachReceipt(ctx context.Context, key queue.Key) (reconcile.Result, error) {
	if key.Kind != ReceiptKind {
		return reconcile.Result{}, errors.New("unexpected receipt kind")
	}
	receiptRecord, input, receipt, err := store.receipt(ctx, key.ID)
	if err != nil {
		return reconcile.Result{}, err
	}
	if receipt.State != "pending" {
		return reconcile.Result{}, nil
	}
	if receipt.Attempts == math.MaxInt64 {
		return reconcile.Result{}, errors.New("receipt attempt counter exhausted")
	}
	now := store.now()
	if receipt.NextAttemptAt.After(now) {
		return delayed(receipt.NextAttemptAt, now), nil
	}
	record, connection, err := store.connection(ctx, input.Connection)
	if err != nil {
		return reconcile.Result{}, err
	}
	if connection.Attempt != nil {
		return delayed(now.Add(time.Minute), now), nil
	}
	if connection.NotBefore != nil && connection.NotBefore.After(now) {
		return delayed(*connection.NotBefore, now), nil
	}
	connection.Attempt = &ReceiptAttempt{ID: key.ID, Token: rand.Text(), Phase: "ready", ReceiptVersion: receiptRecord.Version, Attempts: receipt.Attempts + 1}
	if err := store.save(ctx, record, connection); err != nil {
		return reconcile.Result{}, err
	}
	// Keep the receipt scheduled until the connection publishes its outcome.
	return delayed(now.Add(time.Minute), now), nil
}

// ReceiptObserver may prove a previous effect exists using read-only provider
// calls. A negative observation never authorizes another POST after interruption.
type ReceiptObserver interface {
	ObserveAcknowledgement(context.Context, Request) (bool, error)
}

func receiptOutcome(attempt ReceiptAttempt, now time.Time, deliveryErr error) (ReceiptProgress, *time.Time) {
	result := ReceiptProgress{Schema: 1, State: "delivered", Attempts: attempt.Attempts, NextAttemptAt: now, DeliveredAt: new(now), LastAttempt: attempt.Token}
	if deliveryErr == nil {
		return result, nil
	}
	result.State = "pending"
	result.DeliveredAt = nil
	result.LastError = new("provider delivery failed")
	delay := time.Minute
	var cooldown *time.Time
	if failure, ok := errors.AsType[*DeliveryError](deliveryErr); ok {
		delay = max(delay, failure.RetryAfter)
		switch failure.Disposition {
		case DeliveryRetry:
		case DeliveryTerminal:
			result.State = "terminal"
		case DeliveryUncertain:
			result.State = "uncertain"
		default:
			result.State = "quarantined"
			result.LastError = new("invalid delivery disposition")
		}
		switch failure.RetryScope {
		case RetryRequest:
		case RetryConnection:
			cooldown = new(now.Add(delay))
		default:
			result.State = "quarantined"
			result.LastError = new("invalid retry scope")
		}
	}
	result.NextAttemptAt = now.Add(delay)
	return result, cooldown
}

// ReconcileConnection performs at most one external call. Sending is persisted
// before the call. Reentering that phase observes or stops; it never repeats POST.
func (store *ResourceStore) ReconcileConnection(ctx context.Context, sender Acknowledger) (reconcile.Result, error) {
	if sender == nil {
		return reconcile.Result{}, errors.New("receipt sender required")
	}
	record, connection, err := store.connection(ctx, sender.Connection())
	if err != nil {
		return reconcile.Result{}, err
	}
	attempt := connection.Attempt
	if attempt == nil {
		return reconcile.Result{}, nil
	}
	if attempt.Phase == "outcome" {
		return store.publishOutcome(ctx, record, connection)
	}
	if attempt.Phase == "ready" {
		// Attachment reads two resources. An earlier worker can deliver the
		// receipt between those reads and free this connection for a stale
		// attachment. Retire that attachment before any external effect.
		var receipt ReceiptProgress
		receiptRecord, err := store.readState(ctx, queue.Key{Kind: ReceiptKind, ID: attempt.ID}, &receipt)
		if err != nil {
			return reconcile.Result{}, err
		}
		if err := receipt.validate(); err != nil {
			return reconcile.Result{}, err
		}
		if receiptRecord.Version != attempt.ReceiptVersion || receipt.State != "pending" {
			connection.Attempt = nil
			return reconcile.Result{}, store.save(ctx, record, connection)
		}
	}
	if connection.NotBefore != nil && connection.NotBefore.After(store.now()) {
		return delayed(*connection.NotBefore, store.now()), nil
	}
	request, loadErr := store.Get(ctx, attempt.ID)
	if loadErr == nil && (request.Source.Connection != sender.Connection() || request.ReceiptMode != ReceiptAsync) {
		loadErr = ErrInvalidRequest
	}
	if loadErr != nil {
		if !errors.Is(loadErr, datastore.ErrNotFound) && !errors.Is(loadErr, content.ErrNotFound) && !errors.Is(loadErr, content.ErrCorrupt) && !errors.Is(loadErr, ErrInvalidRequest) {
			return reconcile.Result{}, loadErr
		}
		attempt.Outcome = &ReceiptProgress{Schema: 1, State: "quarantined", Attempts: attempt.Attempts, NextAttemptAt: store.now(), LastAttempt: attempt.Token, LastError: new("missing or invalid request")}
	} else if attempt.Phase == "sending" {
		observed := false
		var observationErr error
		if observer, ok := sender.(ReceiptObserver); ok {
			observed, observationErr = observer.ObserveAcknowledgement(ctx, request)
		}
		if failure, ok := errors.AsType[*DeliveryError](observationErr); ok && failure.RetryScope == RetryConnection {
			until := store.now().Add(max(time.Minute, failure.RetryAfter))
			if connection.NotBefore == nil || until.After(*connection.NotBefore) {
				connection.NotBefore = &until
			}
		}

		if ctx.Err() != nil {
			return reconcile.Result{}, ctx.Err()
		}
		if observed && observationErr == nil {
			outcome, _ := receiptOutcome(*attempt, store.now(), nil)
			attempt.Outcome = &outcome
		} else {
			attempt.Outcome = &ReceiptProgress{Schema: 1, State: "uncertain", Attempts: attempt.Attempts, NextAttemptAt: store.now(), LastAttempt: attempt.Token, LastError: new("interrupted provider delivery")}
		}
	} else {
		attempt.Phase = "sending"
		if err := store.save(ctx, record, connection); err != nil {
			return reconcile.Result{}, err
		}
		// Reread the committed generation. A lost write reply above must not call out.
		record, connection, err = store.connection(ctx, sender.Connection())
		if err != nil {
			return reconcile.Result{}, err
		}
		if connection.Attempt == nil || connection.Attempt.Token != attempt.Token || connection.Attempt.Phase != "sending" {
			return reconcile.Result{}, datastore.ErrConflict
		}
		attempt = connection.Attempt
		if ctx.Err() != nil {
			return reconcile.Result{}, ctx.Err()
		}
		deliveryErr := sender.Acknowledge(ctx, request)
		if ctx.Err() != nil {
			return reconcile.Result{}, ctx.Err()
		}
		outcome, cooldown := receiptOutcome(*attempt, store.now(), deliveryErr)
		attempt.Outcome = &outcome
		if cooldown != nil && (connection.NotBefore == nil || cooldown.After(*connection.NotBefore)) {
			connection.NotBefore = cooldown
		}
	}
	attempt.Phase = "outcome"
	if err := store.save(ctx, record, connection); err != nil {
		return reconcile.Result{}, err
	}
	if attempt.Outcome.State != "delivered" {
		slog.WarnContext(ctx, "receipt delivery needs recovery", "receipt_id", attempt.ID, "state", attempt.Outcome.State, "attempts", attempt.Attempts)
	}
	record, connection, err = store.connection(ctx, sender.Connection())
	if err != nil {
		return reconcile.Result{}, err
	}
	if connection.Attempt == nil || connection.Attempt.Token != attempt.Token || connection.Attempt.Phase != "outcome" {
		return reconcile.Result{}, datastore.ErrConflict
	}
	return store.publishOutcome(ctx, record, connection)
}

func (store *ResourceStore) publishOutcome(ctx context.Context, record datastore.Record, connection ConnectionProgress) (reconcile.Result, error) {
	attempt := connection.Attempt
	var progress ReceiptProgress
	receiptRecord, err := store.readState(ctx, queue.Key{Kind: ReceiptKind, ID: attempt.ID}, &progress)
	if err != nil || progress.validate() != nil {
		return reconcile.Result{}, errors.Join(err, errors.New("invalid outcome receipt"))
	}
	if progress.LastAttempt != attempt.Token {
		if receiptRecord.Version != attempt.ReceiptVersion || progress.State != "pending" {
			return reconcile.Result{}, errors.New("receipt changed before outcome handoff")
		}
		if err := store.save(ctx, receiptRecord, *attempt.Outcome); err != nil {
			return reconcile.Result{}, err
		}
	}
	connection.Attempt = nil
	if err := store.save(ctx, record, connection); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (store *ResourceStore) Acknowledgement(ctx context.Context, connection Connection, id string) (AcknowledgementStatus, error) {
	_, input, progress, err := store.receipt(ctx, id)
	if err != nil {
		return AcknowledgementStatus{}, err
	}
	if input.Connection != connection {
		return AcknowledgementStatus{}, errors.New("receipt connection mismatch")
	}
	_, account, err := store.connection(ctx, connection)
	if err != nil {
		return AcknowledgementStatus{}, err
	}
	status := AcknowledgementStatus{State: progress.State, Attempts: progress.Attempts, NextAttemptAt: progress.NextAttemptAt, DeliveredAt: progress.DeliveredAt, LastError: progress.LastError, ConnectionNotBefore: account.NotBefore}
	if attempt := account.Attempt; attempt != nil && attempt.ID == id {
		status.State = attempt.Phase
		status.Attempts = attempt.Attempts
		if attempt.Outcome != nil {
			status.State = attempt.Outcome.State
		}
	}
	return status, nil
}

func (store *ResourceStore) RedriveAcknowledgement(ctx context.Context, connection Connection, id string) error {
	record, input, progress, err := store.receipt(ctx, id)
	if err != nil {
		return err
	}
	if input.Connection != connection {
		return errors.New("receipt connection mismatch")
	}
	_, account, err := store.connection(ctx, connection)
	if err != nil {
		return err
	}
	if account.Attempt != nil && account.Attempt.ID == id {
		return errors.New("receipt attempt is still being recovered")
	}
	switch progress.State {
	case "terminal", "uncertain", "quarantined":
	default:
		return errors.New("receipt is not eligible for redrive")
	}
	progress.State = "pending"
	progress.NextAttemptAt = store.now()
	progress.LastError = nil
	return store.save(ctx, record, progress)
}
