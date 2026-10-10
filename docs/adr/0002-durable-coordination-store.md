# Keep distributed coordination behind a store contract

Application snapshot ownership in this decision is superseded by [ADR 0004](0004-key-based-reconciler-queue.md). Backend timing and fencing decisions still apply.

The factory will run across multiple machines and deployment targets.
The reconciliation core requires a store that atomically commits observations, ownership checks, and follow-up scheduling.
This avoids gaps between a database write and delivery to a separate authoritative queue.
PostgreSQL is the first durable adapter; cloud-native adapters must satisfy the same contract.
The initial in-memory implementation was a test model only. It is now the public
[ephemeral memory adapter](../../packages/reconcile/datastore/memory/README.md).
Distributed deployments still require a durable adapter.
Claims fence coordination writes, while reconcilers remain responsible for safe repeated external effects.

## Ownership after lease expiry

Expiry permits recovery. It does not by itself revoke ownership. A completion
and a successor claim compete through the store's atomic conditional operations.
If completion wins, it publishes progress and closes that claim. If recovery
wins, it advances the fence and the previous claim cannot mutate the resource.
Duplicate completion also returns `ErrLeaseLost`.

This replaces rejection based on time at commit. The earlier rule required a
server-time predicate that cloud key-value stores may not provide. Generation
checks express the ownership rule directly. A paused worker can continue making
external effects under either design, so effects still require idempotency or
external fencing.

PostgreSQL continues to use database time for scheduling and recovery. The
[DynamoDB decision](0003-dynamodb-coordination.md) defines its adapter-clock
scheduling policy and records the resulting timing-contract clarification.

S3 belongs to consuming services that own artifacts. The core stores opaque
state bytes, which may contain artifact references. It does not upload, download,
or delete artifacts. Consumers must persist immutable artifacts before publishing
references and manage unreferenced objects after failed state writes. No shared
artifact library is required until there are explicit users.
