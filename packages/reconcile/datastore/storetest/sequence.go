package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

// SequenceCorpus retains queue histories for backend replay and fuzzing.
// Pairs encode an operation and an argument selecting a prior claim.
func SequenceCorpus() [][]byte {
	return [][]byte{{0, 0, 2, 1, 8, 0, 9, 0, 10, 0, 2, 1, 3, 1}, {}, {0, 0, 2, 1, 3, 0}, {0, 0, 2, 1, 0, 0, 4, 0, 2, 1, 3, 1},
		{0, 0, 2, 1, 3, 0, 0, 0, 2, 1, 3, 0, 3, 1}, {0, 0, 2, 1, 5, 0, 6, 0, 7, 0, 3, 0},
		{2, 1, 0, 0, 0, 0, 2, 1, 4, 0, 7, 0}, {0, 0, 2, 1, 4, 0, 2, 1, 3, 1}}
}

// RunSequence compares queue operations with an independent state machine.
// Expiry and process interruption belong to the contract and recovery suites.
func RunSequence(t *testing.T, fixture Fixture, commands []byte) {
	t.Helper()
	key := datastore.Key{Kind: "sequence", ID: "urn:factory:request:sequence"}
	store := fixture.Store
	item := datastore.Item{Key: key}
	exists, active := false, false
	var fence, sequence uint64
	var claims []datastore.Claim
	commands = commands[:min(len(commands), 256)]
	for step := 0; step+1 < len(commands); step += 2 {
		op, arg := commands[step]%11, commands[step+1]
		switch op {
		case 0, 1:
			requireSequenceError(t, step, store.Enqueue(t.Context(), key), nil)
			exists = true
			sequence++
			item.Pending = true
		case 2:
			kind := key.Kind
			if arg%4 == 0 {
				kind = "other"
			}
			claim, err := store.Claim(t.Context(), []string{kind}, time.Minute)
			if !exists || !item.Pending || active || kind != key.Kind {
				requireSequenceError(t, step, err, datastore.ErrNoWork)
				break
			}
			requireSequenceError(t, step, err, nil)
			fence++
			active = true
			if claim.Key != key || claim.Fence != fence || claim.Sequence != sequence || claim.Failures != item.Failures {
				t.Fatalf("step %d: unexpected claim %+v", step, claim)
			}
			claims = append(claims, claim)
		case 3, 4, 5:
			claim := datastore.Claim{Key: key}
			if len(claims) > 0 {
				claim = claims[int(arg)%len(claims)]
			}
			completion := datastore.Completion{}
			if op == 4 {
				completion.Failure = "failed"
				completion.Again = arg%2 == 0
				completion.Stop = !completion.Again
			}
			if op == 5 {
				completion.After = -time.Nanosecond
			}
			err := store.Commit(t.Context(), claim, completion)
			if len(claims) == 0 || op == 5 {
				if err == nil {
					t.Fatalf("step %d: malformed completion accepted", step)
				}
				break
			}
			if !active || claim.Fence != fence {
				requireSequenceError(t, step, err, datastore.ErrLeaseLost)
				break
			}
			requireSequenceError(t, step, err, nil)
			active = false
			item.LastError = completion.Failure
			if completion.Failure == "" {
				item.Failures = 0
			} else {
				item.Failures++
			}
			item.Pending = sequence != claim.Sequence || completion.Again
		case 6:
			got, err := store.Get(t.Context(), key)
			if !exists {
				requireSequenceError(t, step, err, datastore.ErrNotFound)
				break
			}
			requireSequenceError(t, step, err, nil)
			got.Key.ID = "modified local copy"
		case 7:
			store = fixture.Reopen()
		case 8, 9:
			claim := datastore.Claim{Key: key}
			if len(claims) > 0 {
				claim = claims[int(arg)%len(claims)]
			}
			var err error
			if op == 8 {
				err = store.Renew(t.Context(), claim, time.Minute)
			} else {
				err = store.Release(t.Context(), claim)
			}
			if len(claims) == 0 {
				if err == nil {
					t.Fatal("invalid lease token accepted")
				}
				break
			}
			if !active || claim.Fence != fence {
				requireSequenceError(t, step, err, datastore.ErrLeaseLost)
				break
			}
			requireSequenceError(t, step, err, nil)
			if op == 9 {
				active = false
				item.Pending = true
			}
		case 10:
			err := store.Redrive(t.Context(), key)
			if !exists {
				requireSequenceError(t, step, err, datastore.ErrNotFound)
				break
			}
			if active {
				requireSequenceError(t, step, err, datastore.ErrBusy)
				break
			}
			requireSequenceError(t, step, err, nil)
			item.Failures = 0
			item.Abandoned = 0
			item.LastError = ""
			item.Pending = true
			sequence++

		}
		got, err := store.Get(t.Context(), key)
		if !exists {
			requireSequenceError(t, step, err, datastore.ErrNotFound)
			continue
		}
		requireSequenceError(t, step, err, nil)
		if got != item {
			t.Fatalf("step %d: got %+v, want %+v", step, got, item)
		}
	}
}
func requireSequenceError(t *testing.T, step int, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("step %d: error=%v want=%v", step, got, want)
	}
}
