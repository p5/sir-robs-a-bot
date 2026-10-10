package dynamodb

import (
	"context"
	"errors"
	"fmt"
	"math"

	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const timerPageSize = 128
const promotionBudget = 8

// discoverKind compares elapsed timer hints with bounded ready heads. It never
// queries retained resource history. Promotion amortizes future timer reads but
// does not determine whether an elapsed timer can be selected in this call.
func (store *Store) discoverKind(ctx context.Context, kind string) ([]candidate, error) {
	cutoff, err := store.schedulingTime()
	if err != nil {
		return nil, err
	}
	var candidates candidateHeap
	timers, err := store.discoverTimers(ctx, kind, cutoff, &candidates)
	if err != nil {
		return nil, err
	}
	for shard := range store.shards {
		entries, err := store.querySchedules(ctx, kind, readyState, shard, 0, candidateLimit)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			candidates.offer(candidate{key: entry.Key, due: entry.Due, priority: entry.Priority})
		}
	}
	// The best candidate can move directly from timer to a claimed lease.
	// Moving it to ready first would add a redundant read and transaction.
	var best candidate
	for index, candidate := range candidates {
		if index == 0 || compareCandidates(candidate, best) < 0 {
			best = candidate
		}
	}
	for _, entry := range timers {
		if entry.Key == best.key {
			continue
		}
		if err := store.promoteTimer(ctx, entry); err != nil {
			return nil, err
		}
	}
	return candidates, nil
}

// discoverTimers streams every elapsed hint to preserve numeric priority. Its
// memory is bounded by one page, the candidate heap, and the promotion budget.
// Future timers are excluded by the key condition. Reads still scale with the
// expired set; only promotion mutations have a fixed budget per kind.
func (store *Store) discoverTimers(ctx context.Context, kind string, cutoff int64, candidates *candidateHeap) ([]scheduleEntry, error) {
	promote := make([]scheduleEntry, 0, promotionBudget)
	for shard := range store.shards {
		after := ""
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			now, err := store.schedulingTime()
			if err != nil {
				return nil, err
			}
			cutoff = min(cutoff, now)
			page, err := store.querySchedulePage(ctx, kind, timerState, shard, cutoff, timerPageSize, after)
			if err != nil {
				return nil, err
			}
			for _, entry := range page.entries {
				candidates.offer(candidate{key: entry.Key, due: entry.Due, priority: entry.Priority})
				if len(promote) < promotionBudget {
					promote = append(promote, entry)
				}
			}
			if page.next == "" {
				break
			}
			after = page.next
		}
	}
	return promote, nil
}

// promoteTimer moves only a matching authority revision. It leaves ownership
// and retry budgets intact. A concurrent renew, completion, or promoter can win.
func (store *Store) promoteTimer(ctx context.Context, entry scheduleEntry) error {
	current, err := store.read(ctx, entry.Key)
	if err != nil {
		return fmt.Errorf("read timer authority: %w", err)
	}
	if current.Revision != entry.Revision || current.SchedulePartition != entry.Partition || current.ScheduleID != entry.ID {
		return nil
	}
	// Leave exhausted records untouched for claimCandidate to reject. They
	// must not prevent unrelated candidates from being discovered.
	if current.Fence == math.MaxUint64 || (current.Active && current.Item.Abandoned == math.MaxUint32) {
		return nil
	}
	if _, err := store.replace(ctx, current, current.Revision); err != nil {
		return fmt.Errorf("promote timer: %w", err)
	}
	return nil
}

type schedulePage struct {
	entries []scheduleEntry
	next    string
}

func (store *Store) querySchedules(ctx context.Context, kind, state string, shard uint32, cutoff int64, limit int) ([]scheduleEntry, error) {
	page, err := store.querySchedulePage(ctx, kind, state, shard, cutoff, limit, "")
	return page.entries, err
}

func (store *Store) querySchedulePage(ctx context.Context, kind, state string, shard uint32, cutoff int64, limit int, after string) (schedulePage, error) {
	partition := store.schedulePartition(kind, state, shard)
	input := &sdk.QueryInput{
		TableName: new(store.table), ConsistentRead: new(true), Limit: new(int32(limit)),
		KeyConditionExpression:   new("#pk = :pk"),
		ExpressionAttributeNames: map[string]string{"#pk": "pk"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: partition},
		},
	}
	if state == timerState {
		input.KeyConditionExpression = new("#pk = :pk AND #sk <= :cutoff")
		input.ExpressionAttributeNames["#sk"] = "sk"
		input.ExpressionAttributeValues[":cutoff"] = &types.AttributeValueMemberS{Value: fmt.Sprintf("%020d#~", cutoff)}
	}
	if after != "" {
		input.ExclusiveStartKey = scheduleKey(partition, after)
	}
	output, err := store.client.Query(ctx, input, containDecoderPanics)
	if err != nil {
		return schedulePage{}, fmt.Errorf("query %s schedules: %w", state, err)
	}
	if len(output.Items) > limit {
		return schedulePage{}, errors.New("scheduling response exceeds requested limit")
	}
	entries := make([]scheduleEntry, 0, len(output.Items))
	previous := after
	for _, item := range output.Items {
		entry, err := store.decodeSchedule(item, kind, state, partition)
		if err != nil {
			return schedulePage{}, err
		}
		if entry.ID <= previous || (state == timerState && entry.Eligible > cutoff) {
			return schedulePage{}, errors.New("invalid schedule order or eligibility")
		}
		entries = append(entries, entry)
		previous = entry.ID
	}
	page := schedulePage{entries: entries}
	if len(output.LastEvaluatedKey) != 0 {
		pk, validPK := output.LastEvaluatedKey["pk"].(*types.AttributeValueMemberS)
		sk, validSK := output.LastEvaluatedKey["sk"].(*types.AttributeValueMemberS)
		if !validPK || !validSK || pk.Value != partition || len(entries) == 0 || sk.Value != previous {
			return schedulePage{}, errors.New("invalid scheduling continuation key")
		}
		page.next = sk.Value
	}
	return page, nil
}
