package dynamodb

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

const schedulingLayout = "transactional-schedules-v1"
const readyState = "ready"
const timerState = "timer"

// scheduleEntry contains only discovery metadata. The resource record decides
// ownership; a listed entry never grants permission to run a reconciler.
type scheduleEntry struct {
	Partition string        `dynamodbav:"pk"`
	ID        string        `dynamodbav:"sk"`
	Schema    uint32        `dynamodbav:"schema"`
	Revision  string        `dynamodbav:"revision"`
	Key       datastore.Key `dynamodbav:"key"`
	Due       int64         `dynamodbav:"due"`
	Eligible  int64         `dynamodbav:"eligible"`
	Priority  uint32        `dynamodbav:"priority"`
}

func eligibility(current record) int64 {
	eligible := max(current.Due, current.NotBefore)
	if current.Active {
		eligible = max(eligible, current.Lease)
	}
	return eligible
}

func (store *Store) schedulePartition(kind, state string, shard uint32) string {
	return fmt.Sprintf("schedule:%s:%s:%02d", store.partition(kind), state, shard)
}

func (store *Store) entryFor(current record, state string) scheduleEntry {
	digest := sha256.Sum256([]byte(current.ID))
	shard := binary.BigEndian.Uint32(digest[:4]) % store.shards
	entry := scheduleEntry{Partition: store.schedulePartition(current.Item.Key.Kind, state, shard),
		Schema: schemaVersion, Revision: current.Revision, Key: current.Item.Key,
		Due: current.Due, Eligible: eligibility(current), Priority: current.Item.Priority}
	// Hashes keep index sort keys within DynamoDB's 1 KiB bound even for a
	// maximum-length resource ID. Marker writes also condition on identity.
	if state == readyState {
		entry.ID = fmt.Sprintf("%010d#%020d#%x", math.MaxUint32-current.Item.Priority, current.Due, digest)
	} else {
		entry.ID = fmt.Sprintf("%020d#%010d#%x", entry.Eligible, math.MaxUint32-current.Item.Priority, digest)
	}
	return entry
}

func (store *Store) validateSchedule(current record) error {
	if current.Layout != schedulingLayout || current.Shards != store.shards {
		return errors.New("unsupported scheduling layout or mismatched shard count")
	}
	if !current.Item.Pending {
		if current.ScheduleState != "" || current.SchedulePartition != "" || current.ScheduleID != "" {
			return errors.New("inactive resource retains scheduling metadata")
		}
		return nil
	}
	if current.ScheduleState != readyState && current.ScheduleState != timerState {
		return errors.New("pending resource has no scheduling state")
	}
	expected := store.entryFor(current, current.ScheduleState)
	if current.SchedulePartition != expected.Partition || current.ScheduleID != expected.ID {
		return errors.New("resource scheduling identity does not match its metadata")
	}
	return nil
}

func (store *Store) decodeSchedule(item map[string]types.AttributeValue, kind, state, partition string) (scheduleEntry, error) {
	for _, name := range []string{"pk", "sk", "schema", "revision", "key", "due", "eligible", "priority"} {
		valid := false
		switch name {
		case "pk", "sk", "revision":
			_, valid = item[name].(*types.AttributeValueMemberS)
		case "key":
			_, valid = item[name].(*types.AttributeValueMemberM)
		default:
			_, valid = item[name].(*types.AttributeValueMemberN)
		}
		if !valid {
			return scheduleEntry{}, fmt.Errorf("invalid schedule attribute %q", name)
		}
	}
	var entry scheduleEntry
	if err := attributevalue.UnmarshalMap(item, &entry); err != nil {
		return entry, fmt.Errorf("decode schedule: %w", err)
	}
	if entry.Schema != schemaVersion || entry.Revision == "" || entry.Key.Kind != kind || entry.Partition != partition || entry.Due < 0 || entry.Eligible < entry.Due {
		return entry, errors.New("invalid scheduling metadata")
	}
	if err := validateKey(entry.Key); err != nil {
		return entry, err
	}
	expected := store.entryFor(record{ID: entry.Key.ID, Item: datastore.Item{Key: entry.Key, Priority: entry.Priority}, Due: entry.Due, NotBefore: entry.Eligible}, state)
	if expected.Partition != entry.Partition || expected.ID != entry.ID {
		return entry, errors.New("invalid schedule identity")
	}
	return entry, nil
}

func scheduleKey(partition, id string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: partition}, "sk": &types.AttributeValueMemberS{Value: id}}
}
