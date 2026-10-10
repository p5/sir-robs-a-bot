package dynamodb

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

const schemaVersion = 1

// One item owns queue diagnostics, ownership, and scheduling. Every mutation changes
// its revision so a conditional completion can preserve concurrent input.
type record struct {
	Partition         string         `dynamodbav:"pk"`
	ID                string         `dynamodbav:"sk"`
	Schema            uint32         `dynamodbav:"schema"`
	Revision          string         `dynamodbav:"revision"`
	Item              datastore.Item `dynamodbav:"item"`
	Fence             uint64         `dynamodbav:"fence"`
	Sequence          uint64         `dynamodbav:"sequence"`
	Active            bool           `dynamodbav:"active"`
	Due               int64          `dynamodbav:"due"`
	NotBefore         int64          `dynamodbav:"not_before"`
	Lease             int64          `dynamodbav:"lease"`
	Layout            string         `dynamodbav:"layout"`
	Shards            uint32         `dynamodbav:"shards"`
	ScheduleState     string         `dynamodbav:"schedule_state"`
	SchedulePartition string         `dynamodbav:"schedule_pk"`
	ScheduleID        string         `dynamodbav:"schedule_sk"`
}

func (store *Store) partition(kind string) string {
	// Encoding each component makes separators unambiguous. IDs remain exact
	// strings in the sort key, without URL parsing or canonicalization.
	return base64.RawURLEncoding.EncodeToString([]byte(store.namespace)) + ":" + base64.RawURLEncoding.EncodeToString([]byte(kind))
}

func (store *Store) itemKey(key datastore.Key) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: store.partition(key.Kind)},
		"sk": &types.AttributeValueMemberS{Value: key.ID},
	}
}

func (store *Store) decode(item map[string]types.AttributeValue, key datastore.Key) (record, error) {
	// Reject missing or mistyped metadata rather than decoding it as zero.
	for _, name := range []string{"pk", "sk", "schema", "revision", "item", "fence", "sequence", "active", "due", "lease", "not_before", "layout", "shards", "schedule_state", "schedule_pk", "schedule_sk"} {
		valid := false
		switch name {
		case "pk", "sk", "revision", "layout", "schedule_state", "schedule_pk", "schedule_sk":
			_, valid = item[name].(*types.AttributeValueMemberS)
		case "schema", "fence", "sequence", "due", "lease", "not_before", "shards":
			_, valid = item[name].(*types.AttributeValueMemberN)
		case "item":
			_, valid = item[name].(*types.AttributeValueMemberM)
		case "active":
			_, valid = item[name].(*types.AttributeValueMemberBOOL)
		}
		if !valid {
			return record{}, fmt.Errorf("missing or invalid coordination attribute %q", name)
		}
	}
	// Nested metadata must be present with exact DynamoDB types. Missing
	// diagnostics or pending flags must not silently become valid zero values.
	metadata := item["item"].(*types.AttributeValueMemberM).Value
	if _, ok := metadata["Key"].(*types.AttributeValueMemberM); !ok {
		return record{}, errors.New("missing or invalid queue key")
	}
	if _, ok := metadata["Priority"].(*types.AttributeValueMemberN); !ok {
		return record{}, errors.New("missing or invalid priority")
	}
	if _, ok := metadata["Abandoned"].(*types.AttributeValueMemberN); !ok {
		return record{}, errors.New("missing or invalid abandonment count")
	}
	if _, ok := metadata["Failures"].(*types.AttributeValueMemberN); !ok {
		return record{}, errors.New("missing or invalid failure count")
	}
	if _, ok := metadata["LastError"].(*types.AttributeValueMemberS); !ok {
		return record{}, errors.New("missing or invalid failure diagnostic")
	}
	if _, ok := metadata["Pending"].(*types.AttributeValueMemberBOOL); !ok {
		return record{}, errors.New("missing or invalid pending flag")
	}
	var current record
	if err := attributevalue.UnmarshalMap(item, &current); err != nil {
		return record{}, fmt.Errorf("decode coordination record: %w", err)
	}
	if err := store.validateRecord(current, key); err != nil {
		return record{}, err
	}
	if current.Revision == "" || current.Sequence == 0 {
		return record{}, errors.New("invalid coordination revision or enqueue sequence")
	}
	if current.Due < 0 || current.Lease < 0 || current.NotBefore < 0 {
		return record{}, errors.New("invalid coordination schedule")
	}
	if current.Active {
		if current.Fence == 0 || !current.Item.Pending || current.Lease == 0 {
			return record{}, errors.New("invalid active claim metadata")
		}
	} else if current.Lease != 0 {
		return record{}, errors.New("inactive claim retains a lease")
	}
	if err := store.validateSchedule(current); err != nil {
		return record{}, err
	}
	return current, nil
}

func (store *Store) validateRecord(current record, key datastore.Key) error {
	if current.Schema != schemaVersion {
		return fmt.Errorf("unsupported DynamoDB record schema %d", current.Schema)
	}
	if current.Partition != store.partition(key.Kind) || current.ID != key.ID || current.Item.Key != key {
		return errors.New("coordination record identity does not match its key")
	}
	if err := validateKey(current.Item.Key); err != nil {
		return fmt.Errorf("invalid stored key: %w", err)
	}
	return validateCompletion(datastore.Completion{Failure: current.Item.LastError})
}

func number(value uint64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatUint(value, 10)}
}
