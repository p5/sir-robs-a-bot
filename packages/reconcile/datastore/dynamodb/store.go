// Package dynamodb implements durable reconciliation with Amazon DynamoDB.
package dynamodb

import (
	"errors"
	"fmt"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

// Config selects an existing table and isolates resources within it.
// Applications own table provisioning, credentials, and client configuration.
type Config struct {
	Table     string
	Namespace string
	// Shards splits scheduling partitions. Zero selects one shard. All clients
	// of a namespace must agree; changing this value requires a new namespace.
	Shards uint32
	// Clock supplies scheduling time. Nil uses time.Now. All clients sharing a
	// namespace must use synchronized clocks. Tests can supply a shared clock.
	// Clock must be safe for concurrent calls and must not return a zero time.
	Clock func() time.Time
}

// Store owns coordination records. It does not own the supplied SDK client.
// Use one AWS Region; asynchronous global-table replication cannot fence owners.
type Store struct {
	client          *sdk.Client
	table           string
	namespace       string
	clock           func() time.Time
	shards          uint32
	settingsGate    chan struct{}
	settingsChecked bool
}

var _ datastore.Store = (*Store)(nil)

// New validates local configuration. It performs no network requests or schema
// changes. The table must have string partition key pk and string sort key sk.
func New(client *sdk.Client, config Config) (*Store, error) {
	if client == nil {
		return nil, errors.New("DynamoDB client is required")
	}
	if err := validateTable(config.Table); err != nil {
		return nil, err
	}
	if err := validateNamespace(config.Namespace); err != nil {
		return nil, err
	}
	clock := config.Clock
	shards := config.Shards
	if shards == 0 {
		shards = 1
	}
	if shards > 64 {
		return nil, errors.New("scheduling shards must be between 1 and 64")
	}
	if clock == nil {
		clock = time.Now
	}
	return &Store{client: client, table: config.Table, namespace: config.Namespace, clock: clock, shards: shards, settingsGate: make(chan struct{}, 1)}, nil
}

func (store *Store) schedulingTime() (int64, error) {
	now := store.clock()
	// Keep Unix milliseconds and every time.Duration addition within int64.
	if now.Year() < 1970 || now.Year() > 9999 {
		return 0, fmt.Errorf("scheduling clock must be between years 1970 and 9999")
	}
	return now.UnixMilli(), nil
}

func durationMilliseconds(duration time.Duration) int64 {
	milliseconds := int64(duration / time.Millisecond)
	if duration%time.Millisecond != 0 {
		milliseconds++
	}
	return milliseconds
}
