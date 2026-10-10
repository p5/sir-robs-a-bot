package memory

import (
	"container/heap"
	"time"
)

// Each pending record occupies one heap. Ready heaps order by priority; future
// heaps order by the first time a claim can succeed. Completed records occupy
// neither heap. All index maintenance happens under Store.lock with its mutation.
type schedule struct {
	ready  entryHeap
	future entryHeap
}

type entryHeap struct {
	entries []*resourceEntry
	timed   bool
}

func (entries entryHeap) Len() int { return len(entries.entries) }
func (entries entryHeap) Less(left, right int) bool {
	a, b := entries.entries[left], entries.entries[right]
	if entries.timed {
		if aTime, bTime := availableAt(a), availableAt(b); !aTime.Equal(bTime) {
			return aTime.Before(bTime)
		}
	}
	return earlier(a, b)
}
func (entries entryHeap) Swap(left, right int) {
	entries.entries[left], entries.entries[right] = entries.entries[right], entries.entries[left]
	entries.entries[left].position, entries.entries[right].position = left, right
}
func (entries *entryHeap) Push(value any) {
	entry := value.(*resourceEntry)
	entry.position = len(entries.entries)
	entries.entries = append(entries.entries, entry)
}
func (entries *entryHeap) Pop() any {
	last := len(entries.entries) - 1
	entry := entries.entries[last]
	entries.entries[last] = nil
	entries.entries = entries.entries[:last]
	entry.position = -1
	return entry
}

func availableAt(entry *resourceEntry) time.Time {
	due := entry.dueAt
	if entry.notBefore.After(due) {
		due = entry.notBefore
	}
	if entry.active && entry.leaseUntil.After(due) {
		due = entry.leaseUntil
	}
	return due
}

func (store *Store) reschedule(entry *resourceEntry, now time.Time) {
	kind := entry.resource.Key.Kind
	queue := store.schedules[kind]
	if queue == nil {
		queue = &schedule{future: entryHeap{timed: true}}
		store.schedules[kind] = queue
	}
	if entry.position >= 0 {
		if entry.ready {
			heap.Remove(&queue.ready, entry.position)
		} else {
			heap.Remove(&queue.future, entry.position)
		}
	}
	if !entry.resource.Pending {
		return
	}
	entry.ready = !availableAt(entry).After(now)
	if entry.ready {
		heap.Push(&queue.ready, entry)
	} else {
		heap.Push(&queue.future, entry)
	}
}

func (store *Store) selectDue(kinds []string, now time.Time) *resourceEntry {
	var selected *resourceEntry
	for _, kind := range kinds {
		queue := store.schedules[kind]
		if queue == nil {
			continue
		}
		for queue.future.Len() > 0 && !availableAt(queue.future.entries[0]).After(now) {
			entry := heap.Pop(&queue.future).(*resourceEntry)
			entry.ready = true
			heap.Push(&queue.ready, entry)
		}
		if queue.ready.Len() > 0 {
			entry := queue.ready.entries[0]
			if selected == nil || earlier(entry, selected) {
				selected = entry
			}
		}
	}
	return selected
}

// A custom clock can move backwards. Rebuild indexes on that exceptional path
// so an entry promoted before rollback cannot bypass its scheduling floor.
func (store *Store) observeClock(now time.Time) {
	if now.Before(store.lastTime) {
		store.schedules = make(map[string]*schedule)
		for _, entry := range store.entries {
			entry.position = -1
			store.reschedule(entry, now)
		}
	}
	store.lastTime = now
}
