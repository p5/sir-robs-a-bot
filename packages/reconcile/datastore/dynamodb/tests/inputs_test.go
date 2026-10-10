package dynamodbtests

import (
	"encoding/base64"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	adapter "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/dynamodb"
)

func TestRejectedQueueInputsPreserveOwnership(t *testing.T) {
	store, _, _, _ := newFixture(t)
	key := enqueueKey(t, store, "invalid-input")
	claim := claimKey(t, store)
	baseline, err := store.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	for _, completion := range []datastore.Completion{
		{Again: true, After: -time.Nanosecond}, {After: time.Second}, {Again: true, Stop: true},
		{Failure: "bad\x00failure"}, {Failure: "bad\xfffailure"}, {Failure: strings.Repeat("x", adapter.MaxFailureBytes+1)},
	} {
		if err := store.Commit(t.Context(), claim, completion); err == nil {
			t.Fatalf("accepted invalid completion %+v", completion)
		}
	}
	for _, key := range []datastore.Key{{Kind: "fixture", ID: ""}, {Kind: "\xff", ID: "one"}, {Kind: strings.Repeat("k", 257), ID: "one"}, {Kind: "fixture", ID: strings.Repeat("i", 1025)}} {
		if err := store.Enqueue(t.Context(), key); err == nil {
			t.Fatal("accepted invalid enqueue")
		}
		if _, err := store.Get(t.Context(), key); err == nil || errors.Is(err, datastore.ErrNotFound) {
			t.Fatalf("did not validate get: %v", err)
		}
	}
	for _, ttl := range []time.Duration{0, -time.Nanosecond} {
		if _, err := store.Claim(t.Context(), []string{"fixture"}, ttl); err == nil {
			t.Fatal("accepted invalid lease")
		}
	}
	for _, bad := range []datastore.Claim{{Key: key, Fence: 0, Sequence: 1}, {Key: key, Fence: 1, Sequence: 0}} {
		if err := store.Commit(t.Context(), bad, datastore.Completion{}); err == nil {
			t.Fatal("accepted malformed claim")
		}
	}
	after, err := store.Get(t.Context(), key)
	if err != nil || after != baseline {
		t.Fatalf("invalid input changed item: %+v %v", after, err)
	}
	if err := store.Commit(t.Context(), claim, datastore.Completion{}); err != nil {
		t.Fatal("invalid input consumed claim", err)
	}
}
func TestMaximumQueueKeyAndDelay(t *testing.T) {
	store, _, _, _ := newFixture(t)
	key := datastore.Key{Kind: strings.Repeat("k", 256), ID: strings.Repeat("é", 512)}
	if err := store.Enqueue(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(t.Context(), []string{key.Kind}, time.Duration(math.MaxInt64))
	if err != nil {
		t.Fatal(err)
	}
	if claim.Key != key {
		t.Fatal("changed key")
	}
	if err := store.Commit(t.Context(), claim, datastore.Completion{Again: true, After: time.Duration(math.MaxInt64)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(t.Context(), []string{key.Kind}, time.Minute); !errors.Is(err, datastore.ErrNoWork) {
		t.Fatalf("lost maximum delay: %v", err)
	}
}

func TestOverflowAndCorruptRecordsFailClosed(t *testing.T) {
	for _, field := range []string{"sequence", "fence", "schema", "revision", "Failures", "Pending", "Abandoned"} {
		t.Run(field, func(t *testing.T) {
			store, client, config, clock := newFixture(t)
			key := enqueueKey(t, store, "corrupt")
			claim := claimKey(t, store)
			rawKey := map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: base64.RawURLEncoding.EncodeToString([]byte(config.Namespace)) + ":" + base64.RawURLEncoding.EncodeToString([]byte(key.Kind))}, "sk": &types.AttributeValueMemberS{Value: key.ID}}
			path := "#field"
			names := map[string]string{"#field": field}
			value := types.AttributeValue(&types.AttributeValueMemberN{Value: "18446744073709551615"})
			switch field {
			case "schema":
				value = &types.AttributeValueMemberN{Value: "999"}
			case "revision":
				value = &types.AttributeValueMemberS{Value: ""}
			case "Pending":
				path = "#item.#field"
				names["#item"] = "item"
				value = &types.AttributeValueMemberS{Value: "true"}
			case "Failures", "Abandoned":
				path = "#resource.#field"
				names["#resource"] = "item"
				value = &types.AttributeValueMemberN{Value: "4294967295"}
			}
			if _, err := client.UpdateItem(t.Context(), &sdk.UpdateItemInput{TableName: new(table), Key: rawKey, UpdateExpression: new("SET " + path + " = :value"), ExpressionAttributeNames: names, ExpressionAttributeValues: map[string]types.AttributeValue{":value": value}}); err != nil {
				t.Fatal(err)
			}
			before, err := client.GetItem(t.Context(), &sdk.GetItemInput{TableName: new(table), Key: rawKey, ConsistentRead: new(true)})
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "sequence":
				err = store.Enqueue(t.Context(), key)
			case "Abandoned":
				clock.advance(2 * time.Minute)
				_, err = store.Claim(t.Context(), []string{key.Kind}, time.Minute)
			case "fence":
				clock.advance(2 * time.Minute)
				_, err = store.Claim(t.Context(), []string{key.Kind}, time.Minute)
			default:
				err = store.Commit(t.Context(), claim, datastore.Completion{Failure: "failure", Stop: true})
			}
			if err == nil {
				t.Fatal("corruption or overflow accepted")
			}
			after, err := client.GetItem(t.Context(), &sdk.GetItemInput{TableName: new(table), Key: rawKey, ConsistentRead: new(true)})
			if err != nil || !reflect.DeepEqual(before.Item, after.Item) {
				t.Fatal("rejected write changed record", err)
			}
		})
	}
}
