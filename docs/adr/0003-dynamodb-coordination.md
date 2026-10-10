# DynamoDB coordination and scheduling clocks

Application snapshot ownership in this decision is superseded by [ADR 0004](0004-key-based-reconciler-queue.md). Backend timing and fencing decisions still apply.

Status: accepted. The discovery layout is superseded by [ADR 0008](0008-dynamodb-indexed-scheduling.md).

## Context

The factory needs an AWS-native coordination adapter without adding AWS types to
reconcilers. PostgreSQL provides database-authoritative timing. DynamoDB has atomic
conditional writes and strong reads, but its expressions do not provide a server
clock. Removing the expiry check at completion did not remove this difference
from scheduling and lease creation.

## Alternatives

An S3 queue can use conditional object writes and lease metadata. It would require
more work to discover schedules and preserve versioned resource observations
across queue transitions. DynamoDB keeps those fields in one atomic item.

A DynamoDB scheduling index could reduce discovery cost. A global secondary
index is eventually consistent. It would need a recovery strategy to discover
work despite lag and to keep scheduling live. The first adapter instead queries
base partitions with strong consistency and paginates all records for each kind.
It projects only ID and due time, then fetches selected payloads on demand.
This trades evaluated-read cost for a smaller, directly testable recovery design.
It suits bounded namespaces. Projection does not reduce read capacity charges.

Retaining database-authoritative scheduling would exclude this DynamoDB adapter.
The accepted timing contract instead lets each adapter document its clock and
precision. PostgreSQL retains database time. DynamoDB samples a synchronized
adapter clock before writes. Request latency and clock skew affect scheduling
and lease eligibility. No adapter promises identical timing precision.

## Decision

The interface, coordination types, adapters, and shared contract suite live under
`packages/reconcile/datastore`. Adapters import the interface package, never the
engine.

Applications construct `dynamodb.New(client, Config{Table, Namespace})` and pass
the result as `datastore.Store`. Applications own the regional table and client.
The core has no registry and does not import the adapter. Each resource item owns
its desired state, observation, scheduling, counters, active claim, and fence.
Every mutation changes a revision. Completion conditionally updates the revision
it read, preserving concurrent updates and wakes through retry.

Expiry permits recovery; a successor or completion revokes ownership. A fence
check never depends on the clock. The adapter disables mutation retries and
returns unknown outcomes to callers. It neither stores artifacts in S3 nor
requires a separate queue. Records are never deleted during controller operation.
Only one Region may write a namespace. Global tables are unsupported.

## Evidence and limits

DynamoDB Local tests exercise conditional writes, strong queries, pagination,
the shared contract, operation-sequence histories, process interruption, and
persistence across local service restart. HTTP fault injection drops successful
write responses and inserts competing mutations before writes. Read tests inspect
actual service responses to check projections and payload fetch counts. Tests do
not prove managed-service durability, multi-region behavior, or workload capacity.
No independent agent review was used.

Sources: [conditional writes](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/WorkingWithItems.html),
[expression functions](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Expressions.OperatorsAndFunctions.html),
[query pagination](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Query.Pagination.html),
and [read capacity and projections](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Expressions.ProjectionExpressions.html).
