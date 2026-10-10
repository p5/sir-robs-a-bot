package postgrestests

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/postgres"
)

func TestNamespaceBoundaries(t *testing.T) {
	for _, test := range []struct {
		name      string
		namespace string
		valid     bool
	}{
		{name: "maximum bytes", namespace: strings.Repeat("x", 128), valid: true},
		{name: "multibyte maximum", namespace: strings.Repeat("é", 64), valid: true},
		{name: "over limit", namespace: strings.Repeat("x", 129)},
		{name: "multibyte over limit", namespace: strings.Repeat("é", 65)},
		{name: "blank", namespace: " \u2003"},
		{name: "NUL", namespace: "bad\x00namespace"},
		{name: "invalid UTF-8", namespace: "bad\xffnamespace"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := postgres.New(openTestPool(t), test.namespace)
			if (err == nil) != test.valid {
				t.Fatalf("constructor error = %v, valid=%v", err, test.valid)
			}
			if !test.valid {
				return
			}
			key := datastore.Key{Kind: "fixture", ID: "namespace-boundary"}
			if err := store.Enqueue(t.Context(), key); err != nil {
				t.Fatalf("accepted namespace did not round trip: %v", err)
			}
			if _, err := store.Get(t.Context(), key); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRejectedInputsPreserveResource(t *testing.T) {
	store, _, _ := newFixture(t)
	key := enqueueKey(t, store, "one")
	claim := claimKey(t, store, time.Minute)
	baseline, err := store.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	badFence := claim
	badFence.Fence = math.MaxUint64
	zeroSequence := claim
	zeroSequence.Sequence = 0
	operations := map[string]func() error{
		"oversized kind": func() error {
			err := store.Enqueue(t.Context(), datastore.Key{Kind: strings.Repeat("x", 257), ID: "one"})
			return err
		},
		"oversized ID": func() error {
			err := store.Enqueue(t.Context(), datastore.Key{Kind: key.Kind, ID: strings.Repeat("x", 1025)})
			return err
		},
		"negative lease": func() error {
			_, err := store.Claim(t.Context(), []string{key.Kind}, time.Duration(math.MinInt64))
			return err
		},
		"zero lease": func() error {
			_, err := store.Claim(t.Context(), []string{key.Kind}, 0)
			return err
		},
		"empty kinds": func() error {
			_, err := store.Claim(t.Context(), nil, time.Minute)
			return err
		},
		"control in kind": func() error {
			_, err := store.Claim(t.Context(), []string{"fixture\x00"}, time.Minute)
			return err
		},
		"invalid encoding in kind": func() error {
			_, err := store.Claim(t.Context(), []string{"\xff"}, time.Minute)
			return err
		},
		"oversized claim kind": func() error {
			_, err := store.Claim(t.Context(), []string{strings.Repeat("x", 257)}, time.Minute)
			return err
		},
		"fence range":   func() error { return store.Commit(t.Context(), badFence, datastore.Completion{}) },
		"zero sequence": func() error { return store.Commit(t.Context(), zeroSequence, datastore.Completion{}) },
		"negative delay": func() error {
			return store.Commit(t.Context(), claim, datastore.Completion{Again: true, After: -time.Nanosecond})
		},
		"delay without follow-up": func() error { return store.Commit(t.Context(), claim, datastore.Completion{After: time.Second}) },
		"contradictory flags":     func() error { return store.Commit(t.Context(), claim, datastore.Completion{Again: true, Stop: true}) },
		"NUL failure":             func() error { return store.Commit(t.Context(), claim, datastore.Completion{Failure: "bad\x00error"}) },
		"invalid UTF-8 failure":   func() error { return store.Commit(t.Context(), claim, datastore.Completion{Failure: "bad\xfferror"}) },
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			if err := operation(); err == nil {
				t.Fatal("invalid input was accepted")
			}
			resource, err := store.Get(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(resource, baseline) {
				t.Fatalf("rejected input changed resource: got %+v, want %+v", resource, baseline)
			}
		})
	}
	if err := store.Commit(t.Context(), claim, datastore.Completion{}); err != nil {
		t.Fatalf("rejected input changed ownership: %v", err)
	}
}

func TestCounterOverflowRollsBack(t *testing.T) {
	for _, counter := range []string{"fence", "wake_sequence"} {
		t.Run(counter, func(t *testing.T) {
			store, db, namespace := newFixture(t)
			key := enqueueKey(t, store, "one")
			// The column names are a fixed test list, never caller input.
			if _, err := db.ExecContext(t.Context(), "UPDATE factory_reconcile_resources SET "+counter+"=$1 WHERE namespace=$2", int64(math.MaxInt64), namespace); err != nil {
				t.Fatal(err)
			}
			before, err := store.Get(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			switch counter {
			case "fence":
				_, err = store.Claim(t.Context(), []string{key.Kind}, time.Minute)
			case "wake_sequence":
				err = store.Enqueue(t.Context(), key)
			}
			if err == nil {
				t.Fatal("counter overflow was accepted")
			}
			after, err := store.Get(t.Context(), key)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("overflow changed resource: got %+v, want %+v, error=%v", after, before, err)
			}
			var active bool
			if err := db.QueryRowContext(t.Context(), "SELECT lease_until IS NOT NULL FROM factory_reconcile_resources WHERE namespace=$1", namespace).Scan(&active); err != nil || active {
				t.Fatalf("overflow installed a lease: active=%v error=%v", active, err)
			}
		})
	}
}

func TestSQLMetacharactersRemainData(t *testing.T) {
	_, db, namespace := newFixture(t)
	store, err := postgres.New(db, namespace+"'; --")
	if err != nil {
		t.Fatal(err)
	}
	key := datastore.Key{Kind: "kind'); DROP TABLE factory_reconcile_resources; --", ID: "https://example.test/a'%_?q=';--"}
	if err := store.Enqueue(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(t.Context(), []string{key.Kind}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Key != key {
		t.Fatalf("SQL metacharacters changed identity: %+v", claim.Key)
	}
	if err := store.Commit(t.Context(), claim, datastore.Completion{}); err != nil {
		t.Fatal(err)
	}
	resource, err := store.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("SQL data did not round trip: %+v %v", resource, err)
	}
}

func TestMaximumQueueKeyAndDelay(t *testing.T) {
	store, _, _ := newFixture(t)
	key := datastore.Key{Kind: strings.Repeat("k", 256), ID: strings.Repeat("é", 512)}
	if err := store.Enqueue(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(t.Context(), []string{key.Kind}, time.Duration(math.MaxInt64))
	if err != nil {
		t.Fatalf("maximum lease: %v", err)
	}
	if claim.Key != key {
		t.Fatalf("changed key: %+v", claim)
	}
	if err := store.Commit(t.Context(), claim, datastore.Completion{Again: true, After: time.Duration(math.MaxInt64)}); err != nil {
		t.Fatalf("maximum delay: %v", err)
	}
	if _, err := store.Claim(t.Context(), []string{key.Kind}, time.Minute); !errors.Is(err, datastore.ErrNoWork) {
		t.Fatalf("lost maximum delay: %v", err)
	}
}

func TestAbandonmentOverflowPreservesActiveClaim(t *testing.T) {
	store, db, namespace := newFixture(t)
	key := enqueueKey(t, store, "abandonment-overflow")
	claim := claimKey(t, store, time.Nanosecond)
	if _, err := db.ExecContext(t.Context(), "UPDATE factory_reconcile_resources SET abandoned=4294967295 WHERE namespace=$1", namespace); err != nil {
		t.Fatal(err)
	}
	before, err := store.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(t.Context(), []string{key.Kind}, time.Minute); err == nil {
		t.Fatal("abandonment overflow accepted")
	}
	after, err := store.Get(t.Context(), key)
	if err != nil || after != before {
		t.Fatalf("overflow mutated metadata: %+v %v", after, err)
	}
	if err := store.Commit(t.Context(), claim, datastore.Completion{}); err != nil {
		t.Fatal("overflow revoked original claim", err)
	}
}
