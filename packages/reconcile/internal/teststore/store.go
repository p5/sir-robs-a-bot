// Package teststore supplies a controllable clock for the public memory adapter.
package teststore

import (
	"sync"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/memory"
)

type Store struct {
	*memory.Store
	clock *clock
}

type clock struct {
	lock sync.Mutex
	now  time.Time
}

func New() *Store {
	clock := &clock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return &Store{Store: memory.New(memory.Config{Clock: clock.read}), clock: clock}
}

func (clock *clock) read() time.Time {
	clock.lock.Lock()
	defer clock.lock.Unlock()
	return clock.now
}

func (store *Store) Advance(duration time.Duration) {
	store.clock.lock.Lock()
	defer store.clock.lock.Unlock()
	store.clock.now = store.clock.now.Add(duration)
}
