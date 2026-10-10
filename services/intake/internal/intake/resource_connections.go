package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"time"

	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore"
)

type ConnectionProgress struct {
	Schema      uint8
	ActivatedAt time.Time
	NotBefore   *time.Time
	Attempt     *ReceiptAttempt
}

// ReceiptAttempt records intent before a provider call and its observed outcome
// afterwards. One connection record commits the outcome and cooldown together.
type ReceiptAttempt struct {
	ID             string
	Token          string
	Phase          string
	ReceiptVersion uint64
	Attempts       int64
	Outcome        *ReceiptProgress
}

func ConnectionKey(connection Connection) queue.Key {
	raw, _ := json.Marshal(connection)
	digest := sha256.Sum256(raw)
	return queue.Key{Kind: ConnectionKind, ID: connection.Provider + ":" + hex.EncodeToString(digest[:])}
}

func EffectKind(connection Connection) string {
	return connectionEffectKind(ConnectionKey(connection))
}

func connectionEffectKind(key queue.Key) string {
	digest := sha256.Sum256([]byte(key.ID))
	return "intake-effect-" + hex.EncodeToString(digest[:])
}

func (store *ResourceStore) Activate(ctx context.Context, connection Connection, since time.Time) error {
	if connection.Validate() != nil || since.IsZero() || since.After(store.now()) {
		return errors.New("activation requires a connection and a past start time")
	}
	since = since.UTC().Truncate(time.Microsecond)
	raw, err := json.Marshal(connection)
	if err != nil {
		return err
	}
	state, err := json.Marshal(ConnectionProgress{Schema: 1, ActivatedAt: since})
	if err != nil {
		return err
	}
	record, _, err := store.requests.Create(ctx, ConnectionKey(connection), raw, state)
	if err != nil {
		return err
	}
	var progress ConnectionProgress
	if decodeConnectionProgress(record.State, &progress) != nil || !progress.ActivatedAt.Equal(since) {
		return errors.New("connection already has a different activation time or invalid state")
	}
	return nil
}

func (store *ResourceStore) connection(ctx context.Context, connection Connection) (datastore.Record, ConnectionProgress, error) {
	if connection.Validate() != nil {
		return datastore.Record{}, ConnectionProgress{}, errors.New("invalid connection")
	}
	key := ConnectionKey(connection)
	record, raw, err := store.requests.Get(ctx, key)
	if err != nil {
		return datastore.Record{}, ConnectionProgress{}, err
	}
	var identity Connection
	var progress ConnectionProgress
	if json.Unmarshal(raw, &identity) != nil || identity != connection || decodeConnectionProgress(record.State, &progress) != nil {
		return datastore.Record{}, ConnectionProgress{}, errors.New("invalid stored intake connection")
	}
	return record, progress, nil
}

func decodeConnectionProgress(raw []byte, progress *ConnectionProgress) error {
	if json.Unmarshal(raw, progress) != nil || progress.ActivatedAt.IsZero() || progress.Schema != 1 {
		return errors.New("invalid connection state")
	}
	if progress.NotBefore != nil && progress.NotBefore.IsZero() {
		return errors.New("invalid connection cooldown")
	}
	if attempt := progress.Attempt; attempt != nil {
		if attempt.ID == "" || len(attempt.ID) > 512 || len(attempt.Token) < 16 || len(attempt.Token) > 64 || attempt.ReceiptVersion == 0 || attempt.Attempts < 1 {
			return errors.New("invalid receipt attempt")
		}
		switch attempt.Phase {
		case "ready", "sending":
			if attempt.Outcome != nil {
				return errors.New("uncommitted attempt has an outcome")
			}
		case "outcome":
			if attempt.Outcome == nil || attempt.Outcome.validate() != nil || attempt.Outcome.LastAttempt != attempt.Token {
				return errors.New("invalid attempt outcome")
			}
		default:
			return errors.New("invalid attempt phase")
		}
	}
	return nil
}

func (store *ResourceStore) Activation(ctx context.Context, connection Connection) (time.Time, error) {
	_, progress, err := store.connection(ctx, connection)
	return progress.ActivatedAt, err
}
