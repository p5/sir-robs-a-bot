package dynamodbtests

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	adapter "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/dynamodb"
)

func assertSchedule(t *testing.T, client *sdk.Client, config adapter.Config, key datastore.Key, state string) {
	t.Helper()
	partition := base64.RawURLEncoding.EncodeToString([]byte(config.Namespace)) + ":" + base64.RawURLEncoding.EncodeToString([]byte(key.Kind))
	raw, err := client.GetItem(t.Context(), &sdk.GetItemInput{TableName: new(table), ConsistentRead: new(true), Key: map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: partition}, "sk": &types.AttributeValueMemberS{Value: key.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	actual, ok := raw.Item["schedule_state"].(*types.AttributeValueMemberS)
	if !ok || actual.Value != state {
		t.Fatalf("schedule state: %v want %q", raw.Item["schedule_state"], state)
	}
	shards := config.Shards
	if shards == 0 {
		shards = 1
	}
	count := 0
	for _, candidate := range []string{"ready", "timer"} {
		for shard := range shards {
			result, err := client.Query(t.Context(), &sdk.QueryInput{TableName: new(table), ConsistentRead: new(true), KeyConditionExpression: new("pk = :pk"), ExpressionAttributeValues: map[string]types.AttributeValue{":pk": &types.AttributeValueMemberS{Value: fmt.Sprintf("schedule:%s:%s:%02d", partition, candidate, shard)}}})
			if err != nil {
				t.Fatal(err)
			}
			for _, marker := range result.Items {
				identity, ok := marker["key"].(*types.AttributeValueMemberM)
				if !ok {
					t.Fatal("marker lacks resource identity")
				}
				id, ok := identity.Value["ID"].(*types.AttributeValueMemberS)
				if !ok || id.Value != key.ID {
					continue
				}
				count++
				if candidate != state {
					t.Fatalf("marker in %s, want %s", candidate, state)
				}
				revision, ok := marker["revision"].(*types.AttributeValueMemberS)
				resourceRevision, resourceOK := raw.Item["revision"].(*types.AttributeValueMemberS)
				if !ok || !resourceOK || revision.Value != resourceRevision.Value {
					t.Fatal("marker revision differs from authority")
				}
				for _, field := range []string{"pk", "sk"} {
					value, ok := marker[field].(*types.AttributeValueMemberS)
					pointer, pointerOK := raw.Item["schedule_"+field].(*types.AttributeValueMemberS)
					if !ok || !pointerOK || value.Value != pointer.Value {
						t.Fatal("marker does not match authority pointer")
					}
				}
			}
		}
	}
	expected := 1
	if state == "" {
		expected = 0
	}
	if count != expected {
		t.Fatalf("discovery markers=%d want %d", count, expected)
	}
}

func TestSchedulingMembershipTracksEveryTransition(t *testing.T) {
	_, client, config, clock := newFixture(t)
	config.Shards = 4
	store, err := adapter.New(client, config)
	if err != nil {
		t.Fatal(err)
	}
	key := enqueueKey(t, store, "membership")
	check := func(state string) { t.Helper(); assertSchedule(t, client, config, key, state) }
	check("ready")
	claim := claimKey(t, store)
	check("timer")
	if err := store.Renew(t.Context(), claim, 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	check("timer")
	if err := store.Commit(t.Context(), claim, datastore.Completion{Again: true, After: time.Minute, ProtectDelay: true}); err != nil {
		t.Fatal(err)
	}
	check("timer")
	if err := store.Enqueue(t.Context(), key, datastore.EnqueueOptions{Priority: 100}); err != nil {
		t.Fatal(err)
	}
	check("timer")
	clock.advance(2 * time.Minute)
	claim = claimKey(t, store)
	if claim.Priority != 100 || claim.Sequence != 2 || claim.Abandoned != 0 {
		t.Fatalf("promotion changed queue state: %+v", claim)
	}
	check("timer")
	if err := store.Enqueue(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(t.Context(), claim, datastore.Completion{}); err != nil {
		t.Fatal(err)
	}
	check("ready")
	claim = claimKey(t, store)
	if err := store.Release(t.Context(), claim); err != nil {
		t.Fatal(err)
	}
	check("ready")
	if err := store.Redrive(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	check("ready")
	claim = claimKey(t, store)
	if err := store.Commit(t.Context(), claim, datastore.Completion{Failure: "broken", Stop: true}); err != nil {
		t.Fatal(err)
	}
	check("")
	if err := store.Redrive(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	check("ready")
	claim = claimKey(t, store)
	if err := store.Commit(t.Context(), claim, datastore.Completion{}); err != nil {
		t.Fatal(err)
	}
	check("")
}

func TestDiscoveryRetainsHighestPriorityAcrossTimerPages(t *testing.T) {
	store, _, config, clock := newFixture(t)
	var urgent datastore.Key
	for index := range 140 {
		key := datastore.Key{Kind: "fixture", ID: fmt.Sprintf("delayed-%03d", index)}
		priority := uint32(0)
		if index == 139 {
			priority = ^uint32(0)
			urgent = key
		}
		if err := store.Enqueue(t.Context(), key, datastore.EnqueueOptions{Priority: priority}); err != nil {
			t.Fatal(err)
		}
		claim := claimKey(t, store)
		if err := store.Commit(t.Context(), claim, datastore.Completion{Again: true, After: time.Minute}); err != nil {
			t.Fatal(err)
		}
		// Eligibility, not priority, orders timers. Put urgent work after the
		// first page even though it has the highest scheduling priority.
		clock.advance(time.Millisecond)
	}
	clock.advance(2 * time.Minute)
	transport := new(interceptHTTP)
	observed := interceptedStore(t, config, transport)
	if claim := claimKey(t, observed); claim.Key != urgent || claim.Abandoned != 0 || claim.Failures != 0 {
		t.Fatalf("timer traversal lost priority or charged a retry: %+v", claim)
	}
	if writes := transport.writes.Load(); writes > 9 {
		t.Fatalf("promotion exceeded eight writes plus one claim: %d", writes)
	}
}

func TestRenewalAndCompletionRaceWithExpiredTimerClaim(t *testing.T) {
	for _, operation := range []string{"renew", "complete"} {
		t.Run(operation, func(t *testing.T) {
			store, client, config, clock := newFixture(t)
			key := enqueueKey(t, store, "promotion-race")
			owner := claimKey(t, store)
			clock.advance(2 * time.Minute)
			transport := &interceptHTTP{beforeWrite: func() {
				var err error
				if operation == "renew" {
					err = store.Renew(t.Context(), owner, 3*time.Minute)
				} else {
					err = store.Commit(t.Context(), owner, datastore.Completion{})
				}
				if err != nil {
					t.Fatal(err)
				}
			}}
			faulty := interceptedStore(t, config, transport)
			if _, err := faulty.Claim(t.Context(), []string{key.Kind}, time.Minute); !errors.Is(err, datastore.ErrNoWork) {
				t.Fatalf("stale promotion returned %v", err)
			}
			state := "timer"
			if operation == "complete" {
				state = ""
			}
			assertSchedule(t, client, config, key, state)
		})
	}
}

func TestLostPromotionResponsePreservesRecovery(t *testing.T) {
	store, client, config, clock := newFixture(t)
	owners := enqueueLeasedTimers(t, store, 20)
	clock.advance(2 * time.Minute)
	transport := new(interceptHTTP)
	transport.drop.Store(true)
	faulty := interceptedStore(t, config, transport)
	if _, err := faulty.Claim(t.Context(), []string{"fixture"}, time.Minute); err == nil {
		t.Fatal("unknown promotion reported success")
	}
	if transport.writes.Load() != 1 {
		t.Fatal("unknown promotion retried")
	}
	// The highest candidate bypasses promotion. The next timer is promoted,
	// retaining its former owner until a later claim actually supersedes it.
	key := datastore.Key{Kind: "fixture", ID: "promotion-18"}
	assertSchedule(t, client, config, key, "ready")
	if first := claimKey(t, store); first.Key.ID != "promotion-19" {
		t.Fatalf("highest expired priority lost: %+v", first)
	}
	next := claimKey(t, store)
	previous := owners[key.ID]
	if next.Key != key || next.Fence != previous.Fence+1 || next.Abandoned != 1 {
		t.Fatalf("promotion changed ownership or crash count: %+v", next)
	}
	if err := store.Commit(t.Context(), previous, datastore.Completion{}); !errors.Is(err, datastore.ErrLeaseLost) {
		t.Fatalf("former owner committed: %v", err)
	}
}

// DynamoDB Local does not produce production transaction-conflict errors.
// Inject one before submission and require the adapter to expose it without retry.
type transactionConflictHTTP struct{ writes atomic.Int32 }

func (transport *transactionConflictHTTP) Do(request *http.Request) (*http.Response, error) {
	if request.Header.Get("X-Amz-Target") != "DynamoDB_20120810.TransactWriteItems" {
		return http.DefaultClient.Do(request)
	}
	transport.writes.Add(1)
	return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(`{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","Message":"injected contention","CancellationReasons":[{"Code":"TransactionConflict"},{"Code":"None"}]}`)), Request: request}, nil
}
func TestTransactionConflictDoesNotRetryOrChangeScheduling(t *testing.T) {
	store, client, config, _ := newFixture(t)
	key := enqueueKey(t, store, "transaction-conflict")
	transport := new(transactionConflictHTTP)
	options := newClient().Options()
	options.HTTPClient = transport
	options.Retryer = nil
	faulty, err := adapter.New(sdk.New(options), config)
	if err != nil {
		t.Fatal(err)
	}
	err = faulty.Enqueue(t.Context(), key)
	if _, ok := errors.AsType[*types.TransactionCanceledException](err); !ok {
		t.Fatalf("conflict was hidden: %v", err)
	}
	if transport.writes.Load() != 1 {
		t.Fatal("conflict automatically replayed mutation")
	}
	assertSchedule(t, client, config, key, "ready")
	if claim := claimKey(t, store); claim.Sequence != 1 {
		t.Fatalf("rejected transaction changed resource: %+v", claim)
	}
}

func TestNamespaceShardConfigurationCannotChange(t *testing.T) {
	store, _, config, _ := newFixture(t)
	key := enqueueKey(t, store, "configured")
	config.Shards = 4
	other, err := adapter.New(newClient(), config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Claim(t.Context(), []string{key.Kind}, time.Minute); err == nil || errors.Is(err, datastore.ErrNoWork) {
		t.Fatalf("shard mismatch silently hid work: %v", err)
	}
	if err := other.Enqueue(t.Context(), datastore.Key{Kind: key.Kind, ID: "new"}); err == nil {
		t.Fatal("mixed shard configurations wrote to one namespace")
	}
	page, err := store.List(t.Context(), datastore.ListRequest{Kind: key.Kind, Limit: 10})
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Item.Key != key {
		t.Fatalf("mismatched writer changed namespace: %+v %v", page, err)
	}
}

type cancelPromotionHTTP struct {
	writes atomic.Int32
	cancel context.CancelFunc
}

func (transport *cancelPromotionHTTP) Do(request *http.Request) (*http.Response, error) {
	response, err := http.DefaultClient.Do(request)
	if err == nil && response.StatusCode == http.StatusOK && request.Header.Get("X-Amz-Target") == "DynamoDB_20120810.TransactWriteItems" {
		if transport.writes.Add(1) == 3 {
			transport.cancel()
		}
	}
	return response, err
}

func TestInterruptedTimerBurstResumesWithoutOwnershipChanges(t *testing.T) {
	store, _, config, clock := newFixture(t)
	for index := range 20 {
		key := datastore.Key{Kind: "fixture", ID: fmt.Sprintf("timer-%02d", index)}
		if err := store.Enqueue(t.Context(), key, datastore.EnqueueOptions{Priority: uint32(index)}); err != nil {
			t.Fatal(err)
		}
		claim := claimKey(t, store)
		if err := store.Commit(t.Context(), claim, datastore.Completion{Again: true, After: time.Hour}); err != nil {
			t.Fatal(err)
		}
	}
	clock.advance(time.Hour)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	transport := &cancelPromotionHTTP{cancel: cancel}
	options := newClient().Options()
	options.HTTPClient = transport
	interrupted, err := adapter.New(sdk.New(options), config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = interrupted.Claim(ctx, []string{"fixture"}, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("promotion did not honor cancellation: %v", err)
	}
	if transport.writes.Load() != 3 {
		t.Fatalf("promotion continued after interruption: %d writes", transport.writes.Load())
	}
	page, err := store.List(t.Context(), datastore.ListRequest{Kind: "fixture", Limit: 100})
	if err != nil || len(page.Entries) != 20 {
		t.Fatalf("interruption lost work: %+v %v", page, err)
	}
	for _, entry := range page.Entries {
		if !entry.Item.Pending || !entry.LeaseUntil.IsZero() || entry.Item.Failures != 0 || entry.Item.Abandoned != 0 {
			t.Fatalf("promotion changed ownership or recovery budgets: %+v", entry)
		}
	}
	reopened, err := adapter.New(newClient(), config)
	if err != nil {
		t.Fatal(err)
	}
	claim := claimKey(t, reopened)
	if claim.Key.ID != "timer-19" || claim.Fence != 2 || claim.Failures != 0 || claim.Abandoned != 0 {
		t.Fatalf("resumed discovery lost priority or advanced ownership during promotion: %+v", claim)
	}
}

func enqueueLeasedTimers(t *testing.T, store *adapter.Store, count int) map[string]datastore.Claim {
	t.Helper()
	owners := make(map[string]datastore.Claim)
	for index := range count {
		key := datastore.Key{Kind: "fixture", ID: fmt.Sprintf("promotion-%02d", index)}
		if err := store.Enqueue(t.Context(), key, datastore.EnqueueOptions{Priority: uint32(index)}); err != nil {
			t.Fatal(err)
		}
		owners[key.ID] = claimKey(t, store)
	}
	return owners
}

func TestPromotionBudgetSpansAllShards(t *testing.T) {
	_, client, config, clock := newFixture(t)
	config.Shards = 4
	store, err := adapter.New(client, config)
	if err != nil {
		t.Fatal(err)
	}
	enqueueLeasedTimers(t, store, 40)
	clock.advance(2 * time.Minute)
	transport := new(interceptHTTP)
	observed := interceptedStore(t, config, transport)
	claim := claimKey(t, observed)
	if claim.Key.ID != "promotion-39" {
		t.Fatalf("highest priority across shards lost: %+v", claim)
	}
	if writes := transport.writes.Load(); writes > 9 {
		t.Fatalf("promotion budget applied per shard instead of per kind: %d", writes)
	}
}

func TestRenewalAndCompletionRaceWithBoundedPromotion(t *testing.T) {
	for _, operation := range []string{"renew", "complete"} {
		t.Run(operation, func(t *testing.T) {
			store, client, config, clock := newFixture(t)
			owners := enqueueLeasedTimers(t, store, 20)
			clock.advance(2 * time.Minute)
			key := datastore.Key{Kind: "fixture", ID: "promotion-18"}
			transport := &interceptHTTP{beforeWrite: func() {
				var err error
				if operation == "renew" {
					err = store.Renew(t.Context(), owners[key.ID], 3*time.Minute)
				} else {
					err = store.Commit(t.Context(), owners[key.ID], datastore.Completion{})
				}
				if err != nil {
					t.Fatal(err)
				}
			}}
			observed := interceptedStore(t, config, transport)
			claim := claimKey(t, observed)
			if claim.Key.ID != "promotion-19" {
				t.Fatalf("discovery lost the best candidate: %+v", claim)
			}
			state := "timer"
			if operation == "complete" {
				state = ""
			}
			assertSchedule(t, client, config, key, state)
			// The stale timer hint cannot restore completed work or supersede
			// the renewed owner when the next candidate is selected.
			next := claimKey(t, store)
			if next.Key.ID != "promotion-17" {
				t.Fatalf("stale promotion reclaimed renewed/completed work: %+v", next)
			}
		})
	}
}
