// Package dynamodb stores resource state and delivery obligations atomically.
package dynamodb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/resources/content"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore"
)

// Client is the SDK operation set used by the adapter. Provisioning is explicit
// and belongs to the deployment, not the resource library.
type Client interface {
	GetItem(context.Context, *sdk.GetItemInput, ...func(*sdk.Options)) (*sdk.GetItemOutput, error)
	TransactWriteItems(context.Context, *sdk.TransactWriteItemsInput, ...func(*sdk.Options)) (*sdk.TransactWriteItemsOutput, error)
	Query(context.Context, *sdk.QueryInput, ...func(*sdk.Options)) (*sdk.QueryOutput, error)
	DeleteItem(context.Context, *sdk.DeleteItemInput, ...func(*sdk.Options)) (*sdk.DeleteItemOutput, error)
}

const deliveryShards = 16

// Store uses a table with string pk/sk keys and no secondary indexes. Namespace
// separates owners. All participating processes must use the same namespace.
type Store struct {
	client    Client
	table     string
	namespace string
	nextShard atomic.Uint64
}

type row struct {
	PK      string `dynamodbav:"pk"`
	SK      string `dynamodbav:"sk"`
	Kind    string `dynamodbav:"kind"`
	ID      string `dynamodbav:"id"`
	Version uint64 `dynamodbav:"version"`
	Digest  string `dynamodbav:"digest,omitempty"`
	Size    int64  `dynamodbav:"size"`
	State   []byte `dynamodbav:"state,omitempty"`
}

func New(client Client, table, namespace string) (*Store, error) {
	if client == nil || table == "" || namespace == "" || len(namespace) > 512 {
		return nil, errors.New("DynamoDB client, table and namespace are required")
	}
	hash := sha256.Sum256([]byte(namespace))
	return &Store{client: client, table: table, namespace: hex.EncodeToString(hash[:])}, nil
}

func encodedKey(key queue.Key) string {
	data, _ := json.Marshal([2]string{key.Kind, key.ID})
	return base64.RawURLEncoding.EncodeToString(data)
}

func (store *Store) resourceKey(key queue.Key) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"pk": stringValue(store.namespace + "#resource#" + encodedKey(key)), "sk": stringValue("resource")}
}

func (store *Store) deliveryPartition(shard uint64) string {
	return store.namespace + "#delivery#" + strconv.FormatUint(shard, 10)
}

func (store *Store) deliveryKey(key queue.Key) map[string]types.AttributeValue {
	digest := sha256.Sum256([]byte(encodedKey(key)))
	return map[string]types.AttributeValue{"pk": stringValue(store.deliveryPartition(uint64(digest[0]) % deliveryShards)), "sk": stringValue(hex.EncodeToString(digest[:]))}
}

func stringValue(value string) types.AttributeValue {
	return &types.AttributeValueMemberS{Value: value}
}
func numberValue(value uint64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatUint(value, 10)}
}

func decode(item map[string]types.AttributeValue) (datastore.Record, error) {
	var stored row
	if err := attributevalue.UnmarshalMap(item, &stored); err != nil {
		return datastore.Record{}, datastore.ErrCorrupt
	}
	record := datastore.Record{Key: queue.Key{Kind: stored.Kind, ID: stored.ID}, Version: stored.Version,
		Content: content.Reference{SHA256: stored.Digest, Size: stored.Size}, State: jsontext.Value(stored.State)}
	if record.Validate() != nil {
		return datastore.Record{}, datastore.ErrCorrupt
	}
	return record, nil
}

func (store *Store) Get(ctx context.Context, key queue.Key) (datastore.Record, error) {
	if err := datastore.ValidateKey(key); err != nil {
		return datastore.Record{}, err
	}
	result, err := store.client.GetItem(ctx, &sdk.GetItemInput{TableName: new(store.table), Key: store.resourceKey(key), ConsistentRead: new(true)})
	if err != nil {
		return datastore.Record{}, err
	}
	if result == nil {
		return datastore.Record{}, datastore.ErrCorrupt
	}
	if len(result.Item) == 0 {
		return datastore.Record{}, datastore.ErrNotFound
	}
	record, err := decode(result.Item)
	if err != nil || record.Key != key {
		return datastore.Record{}, datastore.ErrCorrupt
	}
	return record, nil
}

func (store *Store) commit(ctx context.Context, record datastore.Record, expected uint64) error {
	key := store.resourceKey(record.Key)
	resource := row{PK: key["pk"].(*types.AttributeValueMemberS).Value, SK: "resource", Kind: record.Key.Kind, ID: record.Key.ID,
		Version: record.Version, Digest: record.Content.SHA256, Size: record.Content.Size, State: record.State}
	item, err := attributevalue.MarshalMap(resource)
	if err != nil {
		return err
	}
	delivery := store.deliveryKey(record.Key)
	delivery["kind"] = stringValue(record.Key.Kind)
	delivery["id"] = stringValue(record.Key.ID)
	delivery["version"] = numberValue(record.Version)
	condition := "attribute_not_exists(pk)"
	var names map[string]string
	var values map[string]types.AttributeValue
	if expected != 0 {
		condition = "#version = :expected"
		names = map[string]string{"#version": "version"}
		values = map[string]types.AttributeValue{":expected": numberValue(expected)}
	}
	_, err = store.client.TransactWriteItems(ctx, &sdk.TransactWriteItemsInput{ClientRequestToken: new(rand.Text()), TransactItems: []types.TransactWriteItem{
		{Put: &types.Put{TableName: new(store.table), Item: item, ConditionExpression: new(condition), ExpressionAttributeNames: names, ExpressionAttributeValues: values}},
		{Put: &types.Put{TableName: new(store.table), Item: delivery}},
	}})
	if failure, ok := errors.AsType[*types.TransactionCanceledException](err); ok {
		for index, reason := range failure.CancellationReasons {
			if reason.Code == nil {
				continue
			}
			// Contention can occur on either the resource or its outbox item.
			// Capacity, validation, and service failures remain ordinary errors.
			if *reason.Code == "TransactionConflict" || (index == 0 && *reason.Code == "ConditionalCheckFailed") {
				return datastore.ErrConflict
			}
		}
	}
	return err
}

func (store *Store) Create(ctx context.Context, record datastore.Record) (datastore.Record, bool, error) {
	if err := record.Validate(); err != nil {
		return datastore.Record{}, false, err
	}
	if record.Version != 1 {
		return datastore.Record{}, false, errors.New("new resource version must be one")
	}
	err := store.commit(ctx, record, 0)
	if errors.Is(err, datastore.ErrConflict) {
		existing, err := store.Get(ctx, record.Key)
		return existing, false, err
	}
	return record, err == nil, err
}

func (store *Store) Update(ctx context.Context, key queue.Key, expected uint64, state jsontext.Value) (datastore.Record, error) {
	if expected == 0 || expected >= math.MaxInt64 {
		return datastore.Record{}, datastore.ErrConflict
	}
	if err := datastore.ValidateState(state); err != nil {
		return datastore.Record{}, err
	}
	record, err := store.Get(ctx, key)
	if err != nil {
		return datastore.Record{}, err
	}
	if record.Version != expected {
		return datastore.Record{}, datastore.ErrConflict
	}
	record.Version++
	record.State = state
	if err := store.commit(ctx, record, expected); err != nil {
		return datastore.Record{}, err
	}
	return record, nil
}

// Pending queries fixed outbox partitions with strong consistency. It never
// scans resource records or depends on an eventually consistent index. Rotate
// the starting shard so a busy shard does not permanently hide another.
func (store *Store) Pending(ctx context.Context, limit int) ([]datastore.Delivery, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("delivery limit must be between one and 100")
	}
	deliveries := make([]datastore.Delivery, 0, limit)
	start := store.nextShard.Add(1) - 1
	for offset := range uint64(deliveryShards) {
		partition := store.deliveryPartition((start + offset) % deliveryShards)
		result, err := store.client.Query(ctx, &sdk.QueryInput{TableName: new(store.table), ConsistentRead: new(true),
			KeyConditionExpression: new("pk = :pk"), ExpressionAttributeValues: map[string]types.AttributeValue{":pk": stringValue(partition)}, Limit: new(int32(limit - len(deliveries))),
		})
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, datastore.ErrCorrupt
		}
		for _, item := range result.Items {
			var stored row
			if attributevalue.UnmarshalMap(item, &stored) != nil {
				return nil, datastore.ErrCorrupt
			}
			delivery := datastore.Delivery{Key: queue.Key{Kind: stored.Kind, ID: stored.ID}, Version: stored.Version}
			if datastore.ValidateKey(delivery.Key) != nil || delivery.Version == 0 || delivery.Version > math.MaxInt64 {
				return nil, datastore.ErrCorrupt
			}
			expectedKey := store.deliveryKey(delivery.Key)
			if stored.PK != partition || stored.PK != expectedKey["pk"].(*types.AttributeValueMemberS).Value || stored.SK != expectedKey["sk"].(*types.AttributeValueMemberS).Value {
				return nil, datastore.ErrCorrupt
			}
			deliveries = append(deliveries, delivery)
		}
		if len(deliveries) >= limit {
			break
		}
	}
	return deliveries, nil
}

func (store *Store) Delivered(ctx context.Context, delivery datastore.Delivery) error {
	if err := datastore.ValidateKey(delivery.Key); err != nil {
		return err
	}
	if delivery.Version == 0 || delivery.Version > math.MaxInt64 {
		return errors.New("invalid delivery version")
	}
	_, err := store.client.DeleteItem(ctx, &sdk.DeleteItemInput{TableName: new(store.table), Key: store.deliveryKey(delivery.Key),
		ConditionExpression: new("#version = :version"), ExpressionAttributeNames: map[string]string{"#version": "version"},
		ExpressionAttributeValues: map[string]types.AttributeValue{":version": numberValue(delivery.Version)},
	})
	if _, ok := errors.AsType[*types.ConditionalCheckFailedException](err); ok {
		return nil
	}
	if err != nil {
		return fmt.Errorf("acknowledge resource delivery: %w", err)
	}
	return nil
}

var _ datastore.Store = (*Store)(nil)
