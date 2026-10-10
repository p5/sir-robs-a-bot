# Maintain DynamoDB discovery entries transactionally

DynamoDB resource-partition traversal made each claim evaluate retained history
and future work. Keep resource authority and numeric priority unchanged, and
maintain separate ready or timer entries in the same table through conditional
transactions. This confines the change to the DynamoDB adapter and avoids a
cross-service delivery protocol or an eventually consistent discovery index.

Status: accepted.

Ready entries sort by descending numeric priority and original due time. Timer
entries sort by effective eligibility, including protected floors and lease
expiry. Claim streams due timer hints and compares them with bounded ready
heads. It can claim a timer directly. At most eight timer entries per kind are
also promoted per discovery call, across all shards. The best candidate skips
promotion. Numeric priority does not depend on promotion order or lag.

We compared draining every timer before selection, comparing timers without
promotion, and bounded promotion alongside timer comparison. Full draining
amplified first-dispatch mutations during recovery. Direct comparison alone
increased repeated hint traversal while draining a burst. Bounded promotion
spreads the mutation cost across claims and reduces later timer reads. The
[expiry-burst measurements](../design/dispatch-performance.md#expired-timer-bursts)
record the trade-off. All three preserve numeric priorities when every due hint
is compared. We do not replace priorities with fixed levels.

Total reads still scale with the due set, and competing controllers can repeat
work. The promotion budget is not a total request or latency bound. Every page
and mutation honors cancellation. The shared interface, PostgreSQL behavior,
and schema number remain unchanged.

Each mutation updates the resource, removes an obsolete schedule, and writes its
replacement atomically. A revision condition rejects concurrent state changes.
Promotion changes discovery membership without revoking ownership or charging
retry budgets. Renewal moves eligibility forward. Lost responses remain unknown
outcomes, with SDK mutation retries disabled. Only definitive resource-revision
cancellations permit a reread and retry inside the operation.

Shards route deterministically. Immutable namespace settings reject conflicting
shard counts. Start with one shard; adding shards increases empty-query fan-out.
Live resharding is deferred. The schema number remains 1 during initial
development, with an explicit layout marker. Switching from the discarded
unindexed layout requires stopping old controllers and using a fresh namespace.

Sparse GSIs would reduce write complexity but add eventual discovery. A local
secondary index supports strong reads but imposes an item-collection size limit
that includes retained fence history. SQS adds a delivery protocol and another
service. The [prototype evaluation](../design/dynamodb-discovery.md) records those
alternatives. DriftlessAF provides useful state separation and sharding patterns,
but its GCS enumeration is not an indexed DynamoDB scheduling implementation.

Shared contracts, lifecycle membership checks, priority across timer pages,
promotion races, response loss, and injected transaction conflicts cover the
adapter. Scheduling-response fuzzing exercises decoding without network access.
These tests do not establish live AWS conflict rates, capacity, replication
durability, or IAM behavior. No independent agent review was used.
