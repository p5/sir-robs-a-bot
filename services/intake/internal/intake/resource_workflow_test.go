package intake

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	objects "github.com/p5/sir-robs-a-bot/packages/resources/content/memory"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore"
	state "github.com/p5/sir-robs-a-bot/packages/resources/datastore/memory"
)

type testIndex struct{ err error }

func (index *testIndex) Put(context.Context, Request) error                 { return index.err }
func (index *testIndex) List(context.Context, Connection) ([]string, error) { return nil, index.err }

type failedReceiptCreation struct {
	datastore.Store
	err error
}

func (store failedReceiptCreation) Create(ctx context.Context, record datastore.Record) (datastore.Record, bool, error) {
	if record.Key.Kind == ReceiptKind {
		return datastore.Record{}, false, store.err
	}
	return store.Store.Create(ctx, record)
}

func TestResourceDispatchAdvancesIndependentObligations(t *testing.T) {
	for _, failure := range []string{"receipt", "factory", "index"} {
		t.Run(failure, func(t *testing.T) {
			durable := state.New()
			var backend datastore.Store = durable
			interrupted := errors.New("interrupted handoff")
			if failure == "receipt" {
				backend = failedReceiptCreation{Store: durable, err: interrupted}
			}
			objects := objects.New()
			store := openResourceStore(t, objects, backend)
			accepted, err := store.Accept(t.Context(), validSubmission())
			if err != nil {
				t.Fatal(err)
			}
			factory, index := &dispatchQueue{}, &testIndex{}
			if failure == "factory" {
				factory.err = interrupted
			}
			if failure == "index" {
				index.err = interrupted
			}
			key := queue.Key{Kind: Kind, ID: accepted.RequestID}
			if _, err := store.Dispatch(t.Context(), key, factory, index); !errors.Is(err, interrupted) {
				t.Fatalf("handoff failure missing: %v", err)
			}
			var progress RequestProgress
			if _, err := store.readState(t.Context(), key, &progress); err != nil {
				t.Fatal(err)
			}
			if failure != "factory" && !progress.Dispatched {
				t.Fatal("receipt or index failure blocked factory dispatch")
			}
			if failure != "receipt" && !progress.ReceiptCreated {
				t.Fatal("factory or index failure blocked receipt creation")
			}
			// Reopen against the healthy backend and retry only unfinished obligations.
			store = openResourceStore(t, objects, durable)
			factory.err = nil
			index.err = nil
			if _, err := store.Dispatch(t.Context(), key, factory, index); err != nil {
				t.Fatal(err)
			}
			if _, err := store.readState(t.Context(), key, &progress); err != nil {
				t.Fatal(err)
			}
			if !progress.Dispatched || !progress.ReceiptCreated || !progress.Indexed {
				t.Fatalf("handoff did not recover: %+v", progress)
			}
		})
	}
}

func prepareReceipt(t *testing.T, store *ResourceStore, submission Submission) string {
	t.Helper()
	if err := store.Activate(t.Context(), submission.Source.Connection, submission.CreatedAt.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	accepted, err := store.Accept(t.Context(), submission)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Dispatch(t.Context(), queue.Key{Kind: Kind, ID: accepted.RequestID}, &dispatchQueue{}, &testIndex{}); err != nil {
		t.Fatal(err)
	}
	return accepted.RequestID
}

type resourceSender struct {
	connection Connection
	send       func(context.Context, Request) error
	observe    func(context.Context, Request) (bool, error)
}

func (sender resourceSender) Connection() Connection { return sender.connection }
func (sender resourceSender) Acknowledge(ctx context.Context, request Request) error {
	return sender.send(ctx, request)
}
func (sender resourceSender) ObserveAcknowledgement(ctx context.Context, request Request) (bool, error) {
	if sender.observe == nil {
		return false, nil
	}
	return sender.observe(ctx, request)
}

func TestInterruptedReceiptStopsOnlyThatReceiptAndDoesNotPostAgain(t *testing.T) {
	for _, observed := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncertain", true: "observed"}[observed], func(t *testing.T) {
			store := openResourceStore(t, objects.New(), state.New())
			first := validSubmission()
			id := prepareReceipt(t, store, first)
			if _, err := store.AttachReceipt(t.Context(), queue.Key{Kind: ReceiptKind, ID: id}); err != nil {
				t.Fatal(err)
			}
			var calls int
			sender := resourceSender{connection: first.Source.Connection, send: func(context.Context, Request) error {
				calls++
				panic("process interrupted after POST")
			}, observe: func(context.Context, Request) (bool, error) { return observed, nil }}
			func() {
				defer func() {
					if recover() == nil {
						t.Error("interruption did not occur")
					}
				}()
				store.ReconcileConnection(t.Context(), sender)
			}()
			if _, err := store.ReconcileConnection(t.Context(), sender); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReconcileConnection(t.Context(), sender); err != nil {
				t.Fatal(err)
			}
			status, err := store.Acknowledgement(t.Context(), first.Source.Connection, id)
			want := "uncertain"
			if observed {
				want = "delivered"
			}
			if err != nil || status.State != want || calls != 1 {
				t.Fatalf("recovery: %+v calls=%d %v", status, calls, err)
			}
			_, connection, err := store.connection(t.Context(), first.Source.Connection)
			if err != nil || connection.Attempt != nil {
				t.Fatal("interrupted receipt permanently blocked connection", err)
			}
			next := first
			next.Source.ID = "next"
			other := prepareReceipt(t, store, next)
			if _, err := store.AttachReceipt(t.Context(), queue.Key{Kind: ReceiptKind, ID: other}); err != nil {
				t.Fatal(err)
			}
			sender.send = func(context.Context, Request) error {
				calls++
				return nil
			}
			if _, err := store.ReconcileConnection(t.Context(), sender); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReconcileConnection(t.Context(), sender); err != nil {
				t.Fatal(err)
			}
			status, err = store.Acknowledgement(t.Context(), first.Source.Connection, other)
			if err != nil || status.State != "delivered" || calls != 2 {
				t.Fatalf("next receipt did not progress: %+v calls=%d %v", status, calls, err)
			}
		})
	}
}

func TestConnectionCooldownSurvivesOutcomeHandoffAndRedrive(t *testing.T) {
	store := openResourceStore(t, objects.New(), state.New())
	now := time.Now().UTC()
	store.Clock = func() time.Time { return now }
	submission := validSubmission()
	id := prepareReceipt(t, store, submission)
	if _, err := store.AttachReceipt(t.Context(), queue.Key{Kind: ReceiptKind, ID: id}); err != nil {
		t.Fatal(err)
	}
	sender := resourceSender{connection: submission.Source.Connection, send: func(context.Context, Request) error {
		return &DeliveryError{Err: errors.New("secret response"), RetryScope: RetryConnection, RetryAfter: time.Hour, Disposition: DeliveryUncertain}
	}}
	if _, err := store.ReconcileConnection(t.Context(), sender); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileConnection(t.Context(), sender); err != nil {
		t.Fatal(err)
	}
	if err := store.RedriveAcknowledgement(t.Context(), sender.Connection(), id); err != nil {
		t.Fatal(err)
	}
	result, err := store.AttachReceipt(t.Context(), queue.Key{Kind: ReceiptKind, ID: id})
	if err != nil || result.After != time.Hour || !result.ProtectDelay {
		t.Fatalf("redrive bypassed cooldown: %+v %v", result, err)
	}
	status, err := store.Acknowledgement(t.Context(), sender.Connection(), id)
	if err != nil || status.Attempts != 1 || status.LastError != nil {
		t.Fatalf("redrive lost state: %+v %v", status, err)
	}
}

func TestConcurrentReceiptHandoffSelectsOneConnectionAttempt(t *testing.T) {
	store := openResourceStore(t, objects.New(), state.New())
	first := validSubmission()
	firstID := prepareReceipt(t, store, first)
	second := first
	second.Source.ID = "other"
	secondID := prepareReceipt(t, store, second)
	var attempts atomic.Int64
	var group sync.WaitGroup
	start := make(chan struct{})
	for _, id := range []string{firstID, secondID} {
		group.Go(func() {
			<-start
			_, err := store.AttachReceipt(t.Context(), queue.Key{Kind: ReceiptKind, ID: id})
			if err == nil {
				attempts.Add(1)
			} else if !errors.Is(err, datastore.ErrConflict) {
				t.Error(err)
			}
		})
	}
	close(start)
	group.Wait()
	_, connection, err := store.connection(t.Context(), first.Source.Connection)
	if err != nil || connection.Attempt == nil || (connection.Attempt.ID != firstID && connection.Attempt.ID != secondID) {
		t.Fatalf("connection handoff: %+v %v", connection, err)
	}
	if connection.Attempt.Attempts != 1 {
		t.Fatal("concurrent handoff incremented attempts twice")
	}
}

func FuzzConnectionProgress(f *testing.F) {
	f.Add([]byte(`null`))
	f.Add([]byte(`{"ActivatedAt":"2026-01-01T00:00:00Z"}`))
	f.Add([]byte(`{"Attempt":{"Phase":"sending"}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 32<<10 {
			return
		}
		var progress ConnectionProgress
		_ = decodeConnectionProgress(raw, &progress)
	})
}

type interruptedProgress struct {
	datastore.Store
	phase string
	after bool
	fired bool
}

func (store *interruptedProgress) Update(ctx context.Context, key queue.Key, version uint64, raw jsontext.Value) (datastore.Record, error) {
	var progress ConnectionProgress
	match := false
	if key.Kind == ConnectionKind && json.Unmarshal(raw, &progress) == nil {
		match = store.phase == "clear" && progress.Attempt == nil || progress.Attempt != nil && progress.Attempt.Phase == store.phase
	}
	if key.Kind == ReceiptKind && store.phase == "receipt" {
		var receipt ReceiptProgress
		match = json.Unmarshal(raw, &receipt) == nil && receipt.LastAttempt != ""
	}
	if !match || store.fired {
		return store.Store.Update(ctx, key, version, raw)
	}
	store.fired = true
	if store.after {
		if _, err := store.Store.Update(ctx, key, version, raw); err != nil {
			return datastore.Record{}, err
		}
	}
	return datastore.Record{}, errors.New("lost state write reply")
}

func TestReceiptStateWriteInterruptionsNeverRepeatUncertainPost(t *testing.T) {
	for _, phase := range []string{"sending", "outcome", "receipt", "clear"} {
		for _, after := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/committed=%t", phase, after), func(t *testing.T) {
				durable := state.New()
				backend := &interruptedProgress{Store: durable, phase: phase, after: after}
				blobs := objects.New()
				store := openResourceStore(t, blobs, backend)
				submission := validSubmission()
				id := prepareReceipt(t, store, submission)
				result, err := store.AttachReceipt(t.Context(), queue.Key{Kind: ReceiptKind, ID: id})
				if err != nil || !result.Again || !result.ProtectDelay || result.After <= 0 {
					t.Fatalf("invalid protected schedule: %+v %v", result, err)
				}
				calls := 0
				sender := resourceSender{connection: submission.Source.Connection, send: func(context.Context, Request) error {
					calls++
					return nil
				}}
				if _, err := store.ReconcileConnection(t.Context(), sender); err == nil {
					t.Fatal("state fault did not interrupt handler")
				}
				store = openResourceStore(t, blobs, durable)
				if _, err := store.ReconcileConnection(t.Context(), sender); err != nil {
					t.Fatal(err)
				}
				status, err := store.Acknowledgement(t.Context(), sender.Connection(), id)
				want := "delivered"
				if after && phase == "sending" || !after && phase == "outcome" {
					want = "uncertain"
				}
				wantCalls := 1
				if phase == "sending" && after {
					wantCalls = 0
				}
				if err != nil || status.State != want || calls != wantCalls {
					t.Fatalf("recovery: %+v calls=%d want=%s/%d err=%v", status, calls, want, wantCalls, err)
				}
				_, connection, err := store.connection(t.Context(), sender.Connection())
				if err != nil || connection.Attempt != nil {
					t.Fatalf("connection remains blocked: %+v %v", connection, err)
				}
			})
		}
	}
}

func TestOwnerRelayRoutesConnectionWithoutReadingItsContent(t *testing.T) {
	connection := validSubmission().Source.Connection
	queue := &dispatchQueue{}
	relay := OwnerQueue{Queue: queue}
	if err := relay.Enqueue(t.Context(), ConnectionKey(connection)); err != nil {
		t.Fatal(err)
	}
	if len(queue.keys) != 1 || queue.keys[0].Kind != EffectKind(connection) || queue.keys[0].ID != ConnectionKey(connection).ID {
		t.Fatalf("incorrect connection route: %+v", queue.keys)
	}
}

func TestInterruptedObservationPersistsConnectionRateLimit(t *testing.T) {
	store := openResourceStore(t, objects.New(), state.New())
	now := time.Now().UTC()
	store.Clock = func() time.Time { return now }
	submission := validSubmission()
	id := prepareReceipt(t, store, submission)
	if _, err := store.AttachReceipt(t.Context(), queue.Key{Kind: ReceiptKind, ID: id}); err != nil {
		t.Fatal(err)
	}
	record, connection, err := store.connection(t.Context(), submission.Source.Connection)
	if err != nil {
		t.Fatal(err)
	}
	connection.Attempt.Phase = "sending"
	if err := store.save(t.Context(), record, connection); err != nil {
		t.Fatal(err)
	}
	sender := resourceSender{connection: submission.Source.Connection,
		send: func(context.Context, Request) error {
			t.Fatal("recovery repeated POST")
			return nil
		},
		observe: func(context.Context, Request) (bool, error) {
			return false, &DeliveryError{RetryScope: RetryConnection, RetryAfter: time.Hour}
		},
	}
	if _, err := store.ReconcileConnection(t.Context(), sender); err != nil {
		t.Fatal(err)
	}
	status, err := store.Acknowledgement(t.Context(), sender.Connection(), id)
	if err != nil || status.State != "uncertain" || status.ConnectionNotBefore == nil || !status.ConnectionNotBefore.Equal(now.Add(time.Hour)) {
		t.Fatalf("observation lost cooldown: %+v %v", status, err)
	}
}

// pauseConnectionRead holds one attachment after it has read the receipt, while
// the connection worker finishes the preceding attempt and clears its slot.
type pauseConnectionRead struct {
	datastore.Store
	key     queue.Key
	armed   atomic.Bool
	entered chan struct{}
	resume  chan struct{}
}

func (store *pauseConnectionRead) Get(ctx context.Context, key queue.Key) (datastore.Record, error) {
	if key == store.key && store.armed.Swap(false) {
		close(store.entered)
		select {
		case <-store.resume:
		case <-ctx.Done():
			return datastore.Record{}, ctx.Err()
		}
	}
	return store.Store.Get(ctx, key)
}

func TestStaleReceiptAttachmentDoesNotRepeatDeliveredEffect(t *testing.T) {
	submission := validSubmission()
	backend := &pauseConnectionRead{Store: state.New(), key: ConnectionKey(submission.Source.Connection), entered: make(chan struct{}), resume: make(chan struct{})}
	store := openResourceStore(t, objects.New(), backend)
	id := prepareReceipt(t, store, submission)
	key := queue.Key{Kind: ReceiptKind, ID: id}
	if _, err := store.AttachReceipt(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	backend.armed.Store(true)
	attached := make(chan error, 1)
	go func() {
		_, err := store.AttachReceipt(t.Context(), key)
		attached <- err
	}()
	<-backend.entered
	var calls int
	sender := resourceSender{connection: submission.Source.Connection, send: func(context.Context, Request) error {
		calls++
		return nil
	}}
	_, deliveredErr := store.ReconcileConnection(t.Context(), sender)
	close(backend.resume)
	if err := <-attached; err != nil {
		t.Fatal(err)
	}
	if deliveredErr != nil {
		t.Fatal(deliveredErr)
	}
	if _, err := store.ReconcileConnection(t.Context(), sender); err != nil {
		t.Fatal("stale attachment recovery:", err)
	}
	if calls != 1 {
		t.Fatalf("delivered receipt repeated provider call: %d", calls)
	}
	_, connection, err := store.connection(t.Context(), sender.Connection())
	if err != nil || connection.Attempt != nil {
		t.Fatalf("stale attachment retained connection: %+v %v", connection, err)
	}
	status, err := store.Acknowledgement(t.Context(), sender.Connection(), id)
	if err != nil || status.State != "delivered" || status.Attempts != 1 {
		t.Fatalf("delivered receipt changed: %+v %v", status, err)
	}
}
