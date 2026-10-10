package intake

import (
	"context"
	"encoding/json/v2"
	"errors"
	"time"

	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/resources"
	"github.com/p5/sir-robs-a-bot/packages/resources/content"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore"
)

var ErrInvalidRequest = errors.New("invalid stored factory request")

const ReceiptKind = "factory-request-receipt"
const ConnectionKind = "intake-connection"

// ResourceStore owns intake resources. Notifications always target the intake
// queue. Factory dispatch and provider effects have separate durable progress.
type ResourceStore struct {
	requests *resources.Repository
	state    datastore.Store
	Clock    func() time.Time
}

func NewResourceStore(objects content.Store, state datastore.Store) (*ResourceStore, error) {
	requests, err := resources.New(objects, state)
	if err != nil {
		return nil, err
	}
	return &ResourceStore{requests: requests, state: state, Clock: time.Now}, nil
}

func (store *ResourceStore) now() time.Time { return store.Clock().UTC() }

type RequestProgress struct {
	Schema         uint8
	Dispatched     bool
	ReceiptCreated bool
	Indexed        bool
}

func initialProgress(mode ReceiptMode) RequestProgress {
	return RequestProgress{Schema: 1, ReceiptCreated: mode != ReceiptAsync}
}

// Accept fixes the first committed observation and both obligations. A later
// reconciler materializes the receipt without coordinating another acceptance.
func (store *ResourceStore) Accept(ctx context.Context, submission Submission) (Acceptance, error) {
	if err := submission.Validate(); err != nil {
		return Acceptance{}, err
	}
	id, err := submission.Source.RequestID()
	if err != nil {
		return Acceptance{}, err
	}
	return store.createRequest(ctx, Request{ID: id, Submission: submission}, initialProgress(submission.ReceiptMode))
}

func (store *ResourceStore) createRequest(ctx context.Context, request Request, progress RequestProgress) (Acceptance, error) {
	if err := request.Validate(); err != nil {
		return Acceptance{}, err
	}
	snapshot, err := json.Marshal(request)
	if err != nil {
		return Acceptance{}, err
	}
	state, err := json.Marshal(progress)
	if err != nil {
		return Acceptance{}, err
	}
	_, created, err := store.requests.Create(ctx, queue.Key{Kind: Kind, ID: request.ID}, snapshot, state)
	if err != nil {
		return Acceptance{}, err
	}
	return Acceptance{RequestID: request.ID, Created: created}, nil
}

func (store *ResourceStore) Get(ctx context.Context, id string) (Request, error) {
	_, snapshot, err := store.requests.Get(ctx, queue.Key{Kind: Kind, ID: id})
	if err != nil {
		return Request{}, err
	}
	return decodeResourceRequest(id, snapshot)
}

func decodeResourceRequest(id string, snapshot []byte) (Request, error) {
	var request Request
	if err := json.Unmarshal(snapshot, &request); err != nil || request.ID != id || request.Validate() != nil {
		return Request{}, ErrInvalidRequest
	}
	return request, nil
}

// DeliverOne only wakes the owning intake reconciler. It performs no provider
// call and does not fan out into independently owned queues.
func (store *ResourceStore) DeliverOne(ctx context.Context, target resources.Enqueuer) (bool, error) {
	return store.requests.DeliverOne(ctx, target)
}

func (store *ResourceStore) save(ctx context.Context, record datastore.Record, progress any) error {
	state, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	_, err = store.requests.Update(ctx, record.Key, record.Version, state)
	return err
}

func (store *ResourceStore) readState(ctx context.Context, key queue.Key, progress any) (datastore.Record, error) {
	record, err := store.state.Get(ctx, key)
	if err != nil {
		return datastore.Record{}, err
	}
	if json.Unmarshal(record.State, progress) != nil {
		return datastore.Record{}, errors.New("invalid intake resource state")
	}
	return record, nil
}

var _ Acceptor = (*ResourceStore)(nil)
