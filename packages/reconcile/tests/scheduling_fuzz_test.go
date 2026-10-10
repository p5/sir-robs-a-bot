package reconciletests

import (
	"errors"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/memory"
)

type schedulingReference struct {
	item              datastore.Item
	due, floor, lease time.Duration
	active            bool
	protected         bool
	fence, sequence   uint64
}

// A scan-based oracle checks heap selection independently. Generated histories
// include clock rollback, expiry, priority escalation, protected delays, and
// updates to entries already present in either heap.
func FuzzMemoryScheduling(f *testing.F) {
	f.Add([]byte{0, 1, 0, 255, 1, 0, 2, 3, 0, 1, 3, 255, 1, 0})
	f.Add([]byte{0, 0, 1, 0, 3, 255, 1, 0, 4, 0, 5, 0, 3, 0})
	f.Fuzz(func(t *testing.T, commands []byte) {

		now := time.Duration(0)
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		store := memory.New(memory.Config{Clock: func() time.Time { return base.Add(now) }})
		entries := map[datastore.Key]*schedulingReference{}
		var claim datastore.Claim
		for index := 0; index+1 < min(len(commands), 256); index += 2 {
			op, arg := commands[index]%6, commands[index+1]
			key := datastore.Key{Kind: "a", ID: string(rune('a' + arg%4))}
			if arg&4 != 0 {
				key.Kind = "b"
			}
			switch op {
			case 0:
				priority := uint32(arg / 8)
				if err := store.Enqueue(t.Context(), key, datastore.EnqueueOptions{Priority: priority}); err != nil {
					t.Fatal(err)
				}
				current := entries[key]
				if current == nil {
					current = &schedulingReference{item: datastore.Item{Key: key}}
					entries[key] = current
				}
				current.item.Priority = max(current.item.Priority, priority)
				current.item.Pending, current.due = true, now
				current.sequence++
			case 1:
				var best *schedulingReference
				for _, current := range entries {
					if !current.item.Pending || current.due > now || (current.protected && current.floor > now) || (current.active && current.lease > now) {
						continue
					}
					if best == nil || referenceEarlier(current, best) {
						best = current
					}

				}
				got, err := store.Claim(t.Context(), []string{"a", "b"}, time.Second)
				if best == nil {
					if !errors.Is(err, datastore.ErrNoWork) {
						t.Fatalf("step %d: unexpected claim %+v %v", index, got, err)
					}
					continue
				}
				if err != nil || got.Key != best.item.Key || got.Priority != best.item.Priority {
					t.Fatalf("step %d: selected %+v %v, want %+v", index, got, err, best.item)
				}
				if best.active {
					best.item.Abandoned++
				}
				best.fence++
				best.active, best.lease = true, now+time.Second
				if got.Fence != best.fence || got.Sequence != best.sequence || got.Abandoned != best.item.Abandoned {
					t.Fatal("claim bookkeeping differs")
				}
				claim = got
			case 2, 4, 5:
				current := entries[claim.Key]
				completion := datastore.Completion{Again: arg&1 != 0}
				if completion.Again {
					completion.After = time.Second
					completion.ProtectDelay = arg&2 != 0
				}
				var err error
				if op == 2 {
					err = store.Commit(t.Context(), claim, completion)
				} else if op == 4 {
					err = store.Renew(t.Context(), claim, time.Second)
				} else {
					err = store.Release(t.Context(), claim)
				}
				if claim.Fence == 0 {
					if err == nil {
						t.Fatal("zero claim accepted")
					}
					continue
				}
				if current == nil || !current.active || current.fence != claim.Fence {
					if !errors.Is(err, datastore.ErrLeaseLost) {
						t.Fatal("stale claim accepted", err)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if op == 4 {
					current.lease = max(current.lease, now+time.Second)
					continue
				}
				current.active = false
				if op == 5 {
					current.item.Pending, current.due = true, now
					continue
				}
				current.item.Abandoned = 0
				current.item.Pending = current.sequence != claim.Sequence || completion.Again
				current.due = now + completion.After
				if current.sequence != claim.Sequence {
					current.due = now
				}
				current.floor = 0
				current.protected = completion.ProtectDelay
				if completion.ProtectDelay {
					current.floor = now + completion.After
				}
				if !current.item.Pending {
					current.item.Priority = 0
				}
			case 3:
				now += time.Duration(int(arg)-128) * 10 * time.Millisecond
			}
		}
	})
}

func referenceEarlier(left, right *schedulingReference) bool {
	if left.item.Priority != right.item.Priority {
		return left.item.Priority > right.item.Priority
	}
	if left.due != right.due {
		return left.due < right.due
	}
	if left.item.Key.Kind != right.item.Key.Kind {
		return left.item.Key.Kind < right.item.Key.Kind
	}
	return left.item.Key.ID < right.item.Key.ID
}
