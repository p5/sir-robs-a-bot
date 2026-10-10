package reconciletests

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/p5/sir-robs-a-bot/packages/reconcile"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/postgres"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/storetest"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/internal/teststore"
)

func identityIsValid(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	hasContent := false
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
		hasContent = hasContent || !unicode.IsSpace(character)
	}
	return hasContent
}

func FuzzIdentityInputs(f *testing.F) {
	for _, seed := range []string{"", "fixture", "https://example.test/a%2Fb", "urn:factory:request:one", "\x00", "\xff", "\u2003", strings.Repeat("x", 129)} {
		f.Add(seed, seed, seed)
	}
	f.Fuzz(func(t *testing.T, kind, id, namespace string) {
		key := datastore.Key{Kind: kind, ID: id}
		validKey := identityIsValid(kind) && identityIsValid(id)
		if err := key.Validate(); (err == nil) != validKey {
			t.Fatalf("Validate(%q) = %v, valid=%v", key, err, validKey)
		}
		_, err := postgres.New(new(sql.DB), namespace)
		validNamespace := identityIsValid(namespace) && len(namespace) <= 128
		if (err == nil) != validNamespace {
			t.Fatalf("New(namespace=%q) = %v, valid=%v", namespace, err, validNamespace)
		}
		postgresStore, err := postgres.New(new(sql.DB), "fixture")
		if err != nil {
			t.Fatal(err)
		}
		if !identityIsValid(kind) || len(kind) > 256 {
			if _, err := postgresStore.Claim(t.Context(), []string{kind}, time.Minute); err == nil {
				t.Fatal("claim accepted an invalid kind")
			}
		}
		if !validKey || len(kind) > 256 || len(id) > 1024 {
			if err := postgresStore.Enqueue(t.Context(), key); err == nil {
				t.Fatal("update accepted an invalid key")
			}
			if _, err := postgresStore.Get(t.Context(), key); err == nil {
				t.Fatal("get accepted an invalid key")
			}
			if err := postgresStore.Enqueue(t.Context(), key); err == nil {
				t.Fatal("wake accepted an invalid key")
			}
		}
		if !validKey {
			return
		}
		store := teststore.New()
		if err := store.Enqueue(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		resource, err := store.Get(t.Context(), key)
		if err != nil || resource.Key != key {
			t.Fatalf("identity changed: resource=%+v error=%v", resource, err)
		}
	})
}

func FuzzClaimAndCompletion(f *testing.F) {
	f.Add(uint64(1), uint64(1), int64(0), false, false, "", false)
	f.Add(uint64(math.MaxUint64), uint64(math.MaxUint64), int64(math.MinInt64), true, true, "\x00\xff", true)
	f.Add(uint64(math.MaxInt64), uint64(math.MaxInt64), int64(math.MaxInt64), true, false, "failure", true)
	f.Fuzz(func(t *testing.T, fence, sequence uint64, after int64, again, stop bool, failure string, protect bool) {
		claim := datastore.Claim{
			Key:      datastore.Key{Kind: "fixture", ID: "one"},
			Fence:    fence,
			Sequence: sequence,
		}
		validClaim := fence > 0 && sequence > 0
		if err := claim.Validate(); (err == nil) != validClaim {
			t.Fatalf("claim validation = %v, valid=%v", err, validClaim)
		}
		completion := datastore.Completion{After: time.Duration(after), Again: again, Stop: stop, Failure: failure, ProtectDelay: protect}
		validCompletion := (!protect || (again && after > 0 && !stop)) && after >= 0 && (again || after == 0) && !(stop && again) && utf8.ValidString(failure) && !strings.ContainsRune(failure, 0)
		if err := completion.Validate(); (err == nil) != validCompletion {
			t.Fatalf("completion validation = %v, valid=%v", err, validCompletion)
		}
		// Invalid requests must fail before using a database connection.
		store, err := postgres.New(new(sql.DB), "fixture")
		if err != nil {
			t.Fatal(err)
		}
		if after <= 0 {
			if _, err := store.Claim(t.Context(), []string{"fixture"}, time.Duration(after)); err == nil {
				t.Fatal("claim accepted a nonpositive lease")
			}
		}
		outOfRange := fence > math.MaxInt64 || sequence > math.MaxInt64
		if !validClaim || !validCompletion || outOfRange {
			if err := store.Commit(t.Context(), claim, completion); err == nil {
				t.Fatal("PostgreSQL accepted an invalid completion")
			}
			if !validClaim || outOfRange {
				if err := store.Release(t.Context(), claim); err == nil {
					t.Fatal("invalid release token accepted")
				}
				if err := store.Renew(t.Context(), claim, time.Minute); err == nil {
					t.Fatal("invalid renewal token accepted")
				}
			}
		}
	})
}

func FuzzEngineConfiguration(f *testing.F) {
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{255}, 64))
	valid := make([]byte, 64)
	for index, value := range []uint64{1, 60, 30, 1, 1, 4, 3} {
		binary.LittleEndian.PutUint64(valid[index*8:], value)
	}
	f.Add(valid)
	f.Fuzz(func(t *testing.T, input []byte) {
		var data [64]byte
		copy(data[:], input)
		value := func(index int) int64 { return int64(binary.LittleEndian.Uint64(data[index*8:])) }
		config := reconcile.Config{
			Concurrency: int(value(0)), LeaseDuration: time.Duration(value(1)),
			CallTimeout: time.Duration(value(2)), PollInterval: time.Duration(value(3)),
			RetryInitial: time.Duration(value(4)), RetryMax: time.Duration(value(5)), MaxFailures: uint32(value(6)), MaxAbandoned: uint32(value(7)),
		}
		valid := config.Concurrency > 0 && config.LeaseDuration >= 3*time.Nanosecond && config.CallTimeout > 0 &&
			config.PollInterval > 0 &&
			config.RetryInitial > 0 && config.RetryMax >= config.RetryInitial && config.MaxFailures > 0
		_, err := reconcile.New(teststore.New(), map[string]reconcile.Reconciler{
			"fixture": reconcile.ReconcilerFunc(func(context.Context, datastore.Key) (reconcile.Result, error) {
				return reconcile.Result{}, nil
			}),
		}, config)
		if (err == nil) != valid {
			t.Fatalf("New(%+v) = %v, valid=%v", config, err, valid)
		}
	})
}

func FuzzReconcilerResult(f *testing.F) {
	f.Add(int64(0), false, false, false, "failure", false)
	f.Add(int64(math.MaxInt64), true, false, false, "failure", true)
	f.Add(int64(-1), true, true, true, "bad\x00\xfffailure", true)
	f.Fuzz(func(t *testing.T, after int64, again, fail, permanent bool, failureText string, protect bool) {
		store := teststore.New()
		key := enqueueKey(t, store, "one")
		config := testConfig()
		config.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
		engine, err := reconcile.New(store, map[string]reconcile.Reconciler{
			"fixture": reconcile.ReconcilerFunc(func(context.Context, datastore.Key) (reconcile.Result, error) {
				result := reconcile.Result{Again: again, After: time.Duration(after), ProtectDelay: protect}
				if !fail {
					return result, nil
				}
				failure := errors.New(failureText)
				if permanent {
					failure = reconcile.Permanent(failure)
				}
				return result, failure
			}),
		}, config)
		if err != nil {
			t.Fatal(err)
		}
		reconcileOne(t, engine, true)
		resource, err := store.Get(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		validResult := (!protect || (again && after > 0)) && after >= 0 && (again || after == 0)
		if fail || !validResult {
			wantPending := !(fail && permanent)
			if resource.Failures != 1 || resource.Pending != wantPending {
				t.Fatalf("failure published an invalid outcome: %+v", resource)
			}
			return
		}
		if resource.Failures != 0 || resource.Pending != again {
			t.Fatalf("successful result changed: %+v", resource)
		}
	})
}

func FuzzStoreOperations(f *testing.F) {
	for _, commands := range storetest.SequenceCorpus() {
		f.Add(commands)
	}
	f.Fuzz(func(t *testing.T, commands []byte) {
		store := teststore.New()
		storetest.RunSequence(t, storetest.Fixture{
			Store:  store,
			Reopen: func() datastore.Store { return store },
		}, commands)
	})
}
