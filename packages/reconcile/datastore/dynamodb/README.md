# DynamoDB coordination store

This adapter implements `datastore.Store` with the AWS SDK for Go v2.
Maintainer: the repository maintainers.
Applications supply the client, table, namespace, and Go reconcilers.
The core does not import AWS packages or choose a datastore.

## Configure

Provision a regional DynamoDB table with string partition key `pk` and string
sort key `sk`. No secondary index, Streams, TTL, or S3 bucket is required.
Start with on-demand capacity. Applications own provisioning, credentials,
backups, encryption, deletion protection, and the SDK client's lifecycle.
Construction validates local configuration and makes no network requests.

```go
client := dynamodb.NewFromConfig(awsConfig)
store, err := factorydynamodb.New(client, factorydynamodb.Config{
    Table:     "factory-coordination",
    Namespace: "production",
})
```

Here `dynamodb` is `github.com/aws/aws-sdk-go-v2/service/dynamodb` and
`factorydynamodb` is this package. Handle the error, then pass `store` to
`reconcile.New`. Applications load `awsConfig` using their deployment's credential
policy. Runtime IAM permissions are `dynamodb:GetItem`, `dynamodb:Query`,
`dynamodb:PutItem`, and `dynamodb:DeleteItem` on the table. Transactional puts
and deletes use those underlying IAM permissions. Policies must allow their
`TransactWriteItems` enclosing operation. Runtime clients need
no table creation or deletion permission. Test clients create an isolated table.

All writers to a namespace must use the same Region and table. Global tables
are not supported. Do not restore an old table over live controllers or delete
records while controllers run: either action can reuse a fence. Recovery from
backup requires stopping old controllers and fencing their external effects.

## Ownership and persistence

Each resource occupies one item. The partition key encodes namespace and kind
as separate base64url components. The sort key preserves the exact resource ID.
Kinds select reconcilers. Keys are not parsed or canonicalized.

Each resource retains its authority record. Pending resources also have one
small ready or timer entry in the same table. Completed and suspended resources
have no scheduling entry. Each successful mutation changes a random revision.
A `TransactWriteItems` request updates the authority and its scheduling entry
atomically. Moving an entry uses one resource put, one schedule delete, and one
schedule put. An unchanged scheduling key needs only two puts. Conditional
revision checks preserve racing enqueues and reject superseded owners.

There is no separate authoritative queue or cross-service commit. A matching
fence decides completion authority. Discovery entries never grant ownership.
Unknown write outcomes return errors. The adapter disables SDK retries for
mutations, including namespace initialization. It retries resource revision
conflicts only after DynamoDB explicitly cancels the entire transaction.
Transaction conflicts, throttling, and malformed responses return to the caller.
Reads retain the supplied client's retry policy. Controllers recover uncertain
claims through expiry and fencing.

The latest SDK decoder can panic on malformed JSON. The adapter contains panics
inside SDK response deserialization and returns a decoding error. It does not
catch application, serialization, transport, or reconciler panics. The boundary
adds no second parse or response-body copy. A decoding failure after a mutation
still returns an unknown write outcome. The minimized failing input is a fuzz
regression seed.

Items carry schema version 1 and the `transactional-schedules-v1` layout marker.
Direct reads reject incompatible records. This development layout replaces the
unindexed version 1 format without a migration. Stop old controllers and use a
fresh namespace before switching layouts. The adapter does not discover work
written by an older layout. Do not mix adapter layouts in one namespace.

## Discovery and capacity

Discovery uses strong base-table queries against scheduling partitions. It never
queries the retained resource partition. Timer sort keys start with effective
eligibility time: the maximum of due time, protected floor, and active lease
expiry. A key condition excludes future timers before evaluation. Ready sort
keys start with reversed numeric priority and original due time.

Claim streams timers due at its discovery cutoff in pages of at most 128 and
compares their hints with bounded ready heads. An elapsed timer can be claimed
directly. Numeric priority therefore does not depend on promotion order or lag.
The best 128 candidates per kind and overall remain in memory. Hints contain
only identity, revision, priority, due time, and eligibility. No application
payload is stored or transferred.

Each discovery call also attempts to promote at most eight timers per kind,
across all its shards. It skips the best candidate to avoid a redundant move
before claiming it. Promotion amortizes timer scans across later calls without
requiring the entire expiry burst to be rewritten before dispatch. It changes
only discovery membership and revision; it does not release ownership, advance
fences, or charge abandonment or failures. A concurrent renewal, completion,
enqueue, or promoter invalidates its revision condition. Lost promotion replies
remain errors, with either the timer or ready entry discoverable. No separate
scheduler process is required.

The promotion mutation budget is fixed; total discovery cost is not. Claim reads
all timers due at its cutoff to compare arbitrary numeric priorities. Large
bursts still require proportional hint reads, and competing controllers can
repeat those reads or race on promotion. Traversal and mutations honor
cancellation. Future timers and retained history stay outside dispatch queries.
This adapter does not promise constant dispatch latency during recovery bursts.
Concurrent changes also prevent a global serial priority ordering. See the
[expiry-burst evaluation](../../../../docs/design/dispatch-performance.md#expired-timer-bursts).

Full resource metadata is read only for promotion and claim attempts.
Resource-ID digests keep schedule sort keys within DynamoDB limits. Identity
conditions reject collisions; ties within a truncated equal-priority/equal-due
candidate window follow digest order rather than global resource-ID order.

Get and inspection read resource records. Existing-key enqueue now needs one
strong resource read and one transaction, rather than an atomic update alone.
Completion and lease changes also read authority before transacting. Updating a
resource with large diagnostics costs more capacity than updating a tiny record.
A three-item move below 1 KiB per item consumes six transactional write units.
Measure actual capacity, renewal frequency, conflicts, and contention on AWS.

## Scheduling shards

`Config.Shards` selects 1 through 64 shards. Zero selects one. Start with one
unless measured partition pressure requires more. Keys route deterministically
by SHA-256 of their exact ID. An idle claim makes two queries per shard per
registered kind: one timer query and one ready query. Empty queries still cost
capacity. Increasing shards adds fan-out as well as write distribution.

The first enqueue conditionally creates immutable namespace settings. Each store
instance validates and caches those settings on its first operation. Reads do
not create a namespace. A conflicting shard configuration returns an error
instead of silently missing work. Settings validation adds an initial GetItem.
Do not delete or edit namespace settings while clients run. Changing shard count
requires a fresh namespace and an explicit stop-and-transfer plan; live resharding
is not implemented.

## Timing

DynamoDB conditions have no server-time function. This adapter uses `time.Now`
by default. `Config.Clock` permits a shared deterministic clock in tests. All
production clients must use synchronized host clocks. Controllers do not supply
operation timestamps. Due times and leases use millisecond precision; positive
durations round up. Time samples precede writes, so request latency can consume
part of a lease or delay. Clock skew and jumps can make recovery early or late.
There is no claim that the lease begins at the service's atomic write instant.

Expiry makes a resource eligible for recovery. Only a successor claim or
completion revokes the current claim's commit authority. Fence checks are
independent of clocks. External effects still require idempotency or their own
fencing. Use PostgreSQL when database-authoritative scheduling time is required.
See [the decision](../../../../docs/adr/0003-dynamodb-coordination.md).

## Limits

Namespaces are nonblank UTF-8 without control characters, up to 128 bytes.
Kinds fit 256 bytes; resource IDs fit 1024 bytes. Failure text fits 16 KiB and
follows core text validation. Counters use unsigned Go ranges and reject overflow
without writes. Applications own payload and artifact storage, including S3.

The storage format remains version 1. Discarded development formats have no
migration contract. Unsupported version numbers are rejected.
Completed records retain fence generations; do not delete them while controllers
can hold old tokens.

## Verify

```sh
export PATH="$PWD/.tools/bin:$PATH"
./buck2 build //packages/reconcile/datastore/dynamodb:dynamodb
./buck2 test //packages/reconcile/datastore/dynamodb/tests:test
./buck2 run //tooling:verify
```

Tests launch and remove a disk-backed DynamoDB Local 3.3.1 container. Its pinned
image index supports Linux amd64 and arm64. Podman or Docker is required; tests
fail if neither is available. Random namespaces isolate cases. Removing the
container removes all test data. No AWS credentials or live AWS account is used.

The suite runs the shared store contract and operation-sequence corpus. It also
checks write-response loss, competing enqueues during completion, takeover races,
bounded indexed discovery, timer pagination, schedule membership, key limits, counter overflow,
malformed records, process interruption, clock rollback, and local service
restart. Native race checks cover this adapter. Core fuzz targets cover adapter
validation, resource and schedule response decoding without network access.
Fault injection covers transaction conflicts, lost promotion responses, and
renewal or completion racing with promotion.

DynamoDB Local does not prove AWS replication durability, IAM enforcement,
production throttling, network-partition behavior, multi-region consistency, or
capacity at scale. Local also omits production transaction-conflict exceptions.
Those behaviors require fault injection and deployment tests and measurements.

## Renewals and recovery budgets

`Renew` checks the current active fence and never shortens a lease. `Release`
returns stopped work to pending without changing budgets. Recovery of an expired
active claim increments abandonment atomically with the new fence. Success clears
abandonment and reported failures. `Redrive` resets budgets and diagnostics only
when no claim is active, while retaining fence history. Unknown write outcomes
remain errors. See [the recovery decision](../../../../docs/adr/0005-renewable-claims-and-crash-budgets.md).

## Queue inspection

`List` implements the optional `datastore.Inspector` contract. It returns bounded
pages within one kind, scoped to this store's namespace. Entries include diagnostics,
due time, and lease expiry. Cursors preserve UTF-8 byte ordering and do not grant
ownership. Pages do not form a snapshot across requests.
See [controller operations](../../../../docs/design/controller-operations.md#queue-inspection).

## Scheduling and performance

Priority and protected delay floors live on authoritative queue records. Enqueue
can raise priority but cannot bypass a floor. Successful completion without a
follow-up clears priority; redrive clears the floor explicitly. These fields remain
part of the initial version 1 format.
See [the scheduling decision](../../../../docs/adr/0007-priority-and-protected-delays.md)
and [dispatch benchmarks](../../../../docs/design/dispatch-performance.md).
