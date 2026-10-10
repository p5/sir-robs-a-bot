# DynamoDB discovery evaluation

This records the prototype evaluation. Its initial recommendation was revised:
the implemented adapter preserves numeric priorities with transactional ready
and timer entries. See [the accepted decision](../adr/0008-dynamodb-indexed-scheduling.md).
The evaluation date is 2026-10-10. A later expiry-burst experiment refined the
implementation to compare elapsed timer hints directly and bound promotion.
That preserves numeric priority without draining every timer first. See the
[current expiry-burst evaluation](dispatch-performance.md#expired-timer-bursts).
The promotion-lag limits below describe the earlier designs that selected only
promoted ready work.

## Requirements

The factory needs durable coordination across Go controllers, coalesced resource
notifications, fenced completion, protected delays, and recovery after controller
failure. Application state and workload execution remain outside the queue.
See [the core](core.md) and [current dispatch measurements](dispatch-performance.md).

Completed resource records retain fences. Their count must not determine dispatch
read cost. Future retries and active leases should also stay outside the eligible
query range. Controllers must recover work without a separate mandatory service.

No production throughput target or strict priority latency target has been set.
The current API accepts arbitrary uint32 priorities. This evaluation treats a
smaller priority vocabulary as a possible contract change, not an equivalent
implementation detail.

## Approaches

| Approach | Benefit | Cost or limitation |
| --- | --- | --- |
| Current resource partition traversal | One authoritative record; strong reads; simple mutations | Dispatch evaluates completed and future records |
| Sparse due-first GSI | Excludes completed and future records; one resource write | Finding the highest due priority still traverses due work; index visibility is eventual |
| Sparse priority-first GSI | Preserves priority ordering in index | Future high-priority records still require evaluation and filtering |
| Sparse ready/timer GSI | Bounded candidate queries; one resource item | Timer promotion, index lag, and stale membership; bounded promotion weakens priority across all eligible work |
| Transactional ready/timer entries | Strong discovery queries; atomic resource and schedule changes | More writes, conditional conflicts, timer promotion, and promotion-lag semantics |
| Transactional time entries per fixed priority level | Strong bounded queries; no timer promotion | Requires bounded priority levels; queries fan out across levels and shards |
| Fixed-level local secondary index | Strong bounded queries; one resource mutation; no promoter | 10 GiB item-collection limit per partition key; index must exist at table creation |
| DynamoDB plus SQS | Managed message delivery and consumer wake-ups | Outbox or recoverable delivery, duplicate messages, timer mechanism, priority queues, and two service lifecycles |

A GSI may safely provide discovery hints when claims revalidate authoritative
state. Eventual visibility delays discovery; it does not grant stale ownership.
AWS maintains the index, so ordinary index lag does not require an application
dual write. An empty index query cannot prove that no work exists in the base
table. Polling must continue.

The SQS alternative needs a durable delivery path between DynamoDB and SQS.
Database acknowledgement followed by a best-effort SendMessage has a crash gap.
SQS delay queues support at most 15 minutes of delay, so they cannot implement
arbitrary retry schedules alone. It is a possible future wake-up mechanism, but
adds little value to the present coordination library.

A local secondary index can offer the same fixed-level query ordering while
keeping scheduling attributes on the resource itself. It supports strong reads.
However, its 10 GiB item-collection limit includes retained base records and local
index entries sharing a partition key. Fence history cannot simply be deleted to
stay under that limit. The index must be created with the table. This is an
attractive simpler design for explicitly bounded namespaces, but an awkward
default for indefinite factory history. The prototype tests its query shape,
not enforcement of the AWS size limit.

## Prototype evidence

Disposable scripts and raw results are outside the repository at
`/tmp/factory-discovery-prototypes/`. They use Python, boto3 1.43.111, and the
repository's pinned DynamoDB Local image. This temporary directory is not a
durable repository dependency or a retained test suite.

The prototypes exercise real Query, conditional UpdateItem, and
TransactWriteItems requests. They do not implement the full Store interface.
Query measurements use service-reported evaluated-item and request counts.
They deliberately avoid treating local latency or capacity reports as AWS
performance or billing evidence.

Fixtures hold ten ready resources and 1,000 future resources, with increasing
completed history. A separate due-first fixture places the highest priority last
among 1,000 due records. A priority-first fixture puts 1,000 future high-priority
records before ten ready low-priority records.

The fixed-level prototype uses four levels and four shards. Each partition sorts
by eligibility time and resource identity. A key condition selects times no
later than now. One candidate per partition is enough to compare the first
eligible resource at each priority and shard. Future resources do not enter the
query range. Completed resources have no schedule entry.

An additional local-index prototype stores 1,000 completed, 1,000 future, and ten
ready resource records. It queries fixed-level eligibility ranges with strong
consistency and one candidate per level and shard. Completed records omit the
local index sort key.

The ready/timer prototype uses four ready and four timer partitions. Ready keys
sort by reversed priority, due time, and identity. Timer keys sort by eligibility
time and identity. This permits arbitrary priorities, but requires promotion.

Independent event traces expose a priority limitation. With a promotion batch
of 32 and the most urgent eligible timer at position 1,000, that resource remains
undiscovered for 31 batches. Draining all due timers before every claim restores
visibility at the cost of unbounded dispatch work. Keeping promotion bounded
requires an explicit weaker priority guarantee and a promotion-lag objective.

The transaction prototype interrupts a child process before submission and after
acknowledgement for six transition types. Fresh clients check resource state and
discovery membership. It also races sixteen claim attempts against one revision
and rejects stale promotion after a simulated renewal revision change. This
tests atomic membership and conditional exclusion. It does not prove the full
queue's enqueue-generation, protected-delay, or lease contract.

Measured query results were:

| Completed history | Partition traversal evaluated / queries | Due-first GSI evaluated / queries | Ready/timer entries evaluated / queries |
| --- | --- | --- | --- |
| 0 | 1,010 / 32 | 10 / 1 | 10 / 8 |
| 1,000 | 2,010 / 63 | 10 / 1 | 10 / 8 |
| 10,000 | 11,010 / 345 | 10 / 1 | 10 / 8 |

All fixtures also contained 1,000 future and ten ready resources. The prototype
page limit was 32, not the production adapter's 256. These are access-pattern
counts, not production request estimates. The due-first index must still read
all due entries to find arbitrary highest priority.

The separate priority-first GSI evaluated 1,010 records in 32 queries to return
ten ready records behind future high-priority work. The two-stage GSI evaluated
ten records in eight queries. Fixed levels evaluated four candidates in sixteen
queries, one per nonempty eligible shard. The local secondary index produced the
same four-candidate, sixteen-query result with strong consistency. These batches
have different sizes; their counts do not establish equivalent throughput.

All twelve transaction interruption checks passed. Sixteen concurrent contenders
produced one successful claim. The stale promotion transaction was rejected.
After restarting the disk-backed local service, fresh clients also confirmed
retained ownership, idle completion, and matching discovery membership for three
representative records. This is not a power-loss or AWS replication test.

There was no independent agent review. These are direct comparisons and local
experiments, not live AWS acceptance tests.

## Initial recommendation for the factory

Prefer transactional scheduling entries with a small, explicit set of priority
levels, subject to accepting that priority contract change. A factory generally
needs urgency classes for interactive work, routine progress, and background
maintenance. There is no stated requirement for billions of distinct priority
values. This is a use-case assumption, not a measured workload characteristic.

For each namespace, kind, priority level, and stable shard, store schedule entries
ordered by eligibility time and resource identity. Retain a separate stable
resource record for fences, enqueue generation, budgets, diagnostics, and the
current schedule identity.

Lost transaction responses remain unknown outcomes. Return an error and recover
from durable state; do not run a callback merely because a claim might have
succeeded. DynamoDB request tokens support bounded idempotent replay, but their
ten-minute window is not durable queue identity. The prototype checks identical
token replay within that window without proposing a new mutation retry policy.

An unowned resource's eligibility is the maximum of its due time and protected
delay floor. For an owned resource, include lease expiry in that maximum.
Completed or suspended resources have no schedule entry. A conditional transaction
updates the resource, removes its old entry, and inserts its new entry.
If the entry key does not change, avoid targeting that same item twice in one
transaction. Renewing a lease usually moves the entry to a later eligibility time.

Claim queries bounded heads for the registered kinds, levels, and shards. It
compares higher levels first, then eligibility time and resource identity. This
also changes equal-priority tie-breaking from original due time to effective
eligibility time. A long-protected old resource can therefore follow a newer
resource whose delay expired earlier. Preserving original due-time order across
all eligible records would reintroduce traversal or require ready promotion.
Agree on this fairness rule across adapters before implementing it. Candidates
remain hints; ownership changes require a matching resource revision and fence.
Concurrent changes can still make a candidate stale. This does not establish a
global serial priority order across concurrent controllers.

Keep the caller's operation-based interface. An illustrative future enqueue is
`store.Enqueue(ctx, key, datastore.EnqueueOptions{Priority: datastore.PriorityUrgent})`.
That constant does not exist today. Priority validation should be shared across
adapters so switching deployment targets does not change accepted inputs.
The adapter must own schedule transactions and shard selection. Applications
must not coordinate resource writes and schedule writes themselves.

If arbitrary numeric priority remains a requirement, choose transactional
ready/timer entries instead. Define priority among promoted ready work and expose
promotion lag. The additional promoter has a real semantic cost; it is not only
an implementation detail.

Sparse fixed-level GSIs are a cheaper mutation alternative if eventual discovery
is acceptable. Prefer transactional base-table entries for this factory while
immediate discoverability after acknowledgement and simpler recovery evidence
matter more than minimizing writes. This choice needs live cost measurements.

## Costs and acceptance conditions

A schedule-key move normally touches three items: update the resource, delete
the old entry, and insert the replacement. DynamoDB transactions perform prepare
and commit work for each item. An all-sub-1-KiB three-item move therefore consumes
six transactional write units before conflicts and retries. Actual costs depend
on record sizes, write frequency, and capacity mode. Renewals add recurring cost.

Fixed levels trade promotion work for query fan-out. With four levels and four
shards, querying every partition costs sixteen Query requests per kind per poll,
even when most are empty. Production dispatch should reuse candidate batches and
stop after an eligible higher level where safe. It must periodically refresh
higher levels so batching cannot hide urgent work indefinitely. Begin with a
small fixed shard count; changing that count requires a transition plan.

Before implementation acceptance:

- Run the complete shared Store contract against the new layout.
- Race enqueue, completion, renewal, release, redrive, and expiry recovery.
- Inject lost responses and in-flight interruption around every transaction.
- Prove that pending work always retains a discoverable schedule and that stale
  transactions cannot remove a successor's schedule.
- Preserve protected floors, enqueue generations, budgets, and monotonic fences.
- Measure empty polling, retained history, future backlog, ready backlog,
  contention, renewal frequency, and hot-key pressure on AWS.
- Keep cancellation and bounded request work explicit. Test shard transitions
  before allowing shard-count changes.

The measurements above remain prototype evidence. Production adapter behavior
is described in [its README](../../packages/reconcile/datastore/dynamodb/README.md).

## Sources

- [DynamoDB Query](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_Query.html)
  describes key ordering, filtering after evaluation, and read consistency.
- [DynamoDB transactions](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/transaction-apis.html)
  describes atomic writes, conflicts, index propagation, and token scope.
- [Transactional write cost](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/BestPractices_PessimisticLocking.html)
  describes the two-write-unit cost per item up to 1 KiB.
- [Querying sharded indexes](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/bp-indexes-gsi-sharding.html)
  describes fan-out and aggregation.
- [Transactional outbox](https://docs.aws.amazon.com/prescriptive-guidance/latest/cloud-design-patterns/transactional-outbox.html)
  explains the database-to-message dual-write failure.
- [SQS delay queues](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-delay-queues.html)
  states the 15-minute maximum delay.
- [Local secondary indexes](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/LSI.html)
  describes strong reads, creation requirements, and item-collection size limits.
