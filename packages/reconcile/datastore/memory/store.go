// Package memory provides an ephemeral, process-local reconciliation queue.
package memory

import (
	"errors"
	"sync"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

type resourceEntry struct {
	position   int
	ready      bool
	resource   datastore.Item
	fence      uint64
	sequence   uint64
	active     bool
	leaseUntil time.Time
	dueAt      time.Time
	notBefore  time.Time
}

// Store serializes queue operations within one process. Use New to initialize it.
// Do not copy a Store after first use. It provides no persistence or shared ownership.
type Store struct {
	lock      sync.Mutex
	clock     func() time.Time
	entries   map[datastore.Key]*resourceEntry
	schedules map[string]*schedule
	lastTime  time.Time
}

// Config selects the scheduling clock. Nil Clock uses time.Now. A custom clock
// must be safe for concurrent calls and must never return the zero time.
type Config struct {
	Clock func() time.Time
}

// New creates an empty queue. Records and ownership disappear when this Store
// is discarded. Independent Store values never share work or fence history.
func New(config Config) *Store {
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Store{clock: clock, entries: make(map[datastore.Key]*resourceEntry), schedules: make(map[string]*schedule)}
}

var _ datastore.Store = (*Store)(nil)
var _ datastore.Inspector = (*Store)(nil)

func (store *Store) schedulingTime() (time.Time, error) {
	now := store.clock()
	if now.IsZero() {
		return time.Time{}, errors.New("scheduling clock must not return the zero time")
	}
	store.observeClock(now)
	return now, nil
}
