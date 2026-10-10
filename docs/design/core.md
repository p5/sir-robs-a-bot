# Durable reconciler queue

Status: the Go engine, PostgreSQL, DynamoDB, and ephemeral memory adapters are implemented.
No factory application exists yet. See the [library guide](../../packages/reconcile/README.md).
The [ownership decision](../adr/0004-key-based-reconciler-queue.md) supersedes application snapshots in the earlier store design.

## Ownership

The queue owns resource keys, pending requests, schedules, failure diagnostics,
and expiring claims. Consuming services own desired state, observations,
application versions, artifacts, and effect identities. The queue accepts no
application payload and interprets no application state.

The engine dispatches Go reconcilers by key kind. A reconciler receives a key,
loads current application state, persists repeatable progress, and returns
follow-up scheduling instructions or an error. Queue completion is not an
application checkpoint. The engine has no authoritative in-memory queue.

## Contract

The public interface lives in [datastore/store.go](../../packages/reconcile/datastore/store.go).

| Operation | Contract |
| --- | --- |
| Enqueue | Create or coalesce an immediate request without revoking an active claim |
| Get | Read queue metadata for a known key |
| Claim | Select due work for registered kinds and grant an expiring fenced claim |
| Renew | Extend the active fenced claim without shortening its lease |
| Release | Return stopped, interrupted work to pending without charging a failure |
| Redrive | Reset budgets and enqueue known work when no claim is active |
| Commit | Validate ownership, record diagnostics and follow-up, and release the claim atomically |

Each enqueue advances a sequence. Multiple requests coalesce into one key.
An enqueue during processing survives completion, including permanent failure.
A duplicate delivery can trigger another reconciliation. Success clears failure and abandonment
diagnostics. Enqueue preserves them and can reactivate suspended work.
Explicit redrive resets budgets and diagnostics when no claim is active.
Completed entries remain stored so their fences cannot be reused. No deletion
or retention policy exists yet. Queue metadata does not contain an application
version, so consumers must reject stale application writes themselves.

Each claim advances a fence. Expiry permits recovery but does not revoke the
current claim until another claim wins. Completion and recovery compete
atomically. Superseded and duplicate completions return `ErrLeaseLost` without
writes. Reconcilers receive only the key; ownership tokens stay inside dispatch.

PostgreSQL uses database time. DynamoDB uses synchronized adapter clocks sampled
before writes. Request latency, clock skew, and clock jumps can affect recovery
and due times. Fence checks decide commit authority independently of clocks.

## Application persistence and delivery

A consuming service must close the gap between changing application state and
enqueueing its key. Choose an outbox, a recoverable change stream, or periodic
resync. Saving state and then making one best-effort enqueue call is insufficient.
The core provides no cross-store transaction and cannot repair missing delivery.

A reconciler persists progress before reporting success. A crash after that write
but before queue completion causes another call. It must reread current state and
avoid repeating completed effects. A crash before the write leaves recoverable
queue work. External effects need stable identities, idempotency, or fencing
checked by their owner. Queue fencing does not fence application writes or stop
an old controller from executing after ownership changes.

## Retry and lifecycle

The engine renews leases every third of the lease duration. A renewal has at most
one interval to finish. Renewal failure cancels the call and prevents completion.
Call timeout is finite and may exceed the lease duration. Cancellation is
cooperative; the engine cannot stop an uncooperative reconciler or external work.

An error overrides scheduling instructions. The engine persists normalized
failure text and bounded exponential delay with equal jitter. Permanent errors
and the failure limit suspend automatic retries. A later enqueue requests another
attempt without erasing diagnostics. Successful waiting does not count as failure.
Recovery of an expired active claim increments a separate abandonment counter.
`MaxAbandoned` defaults to `MaxFailures`. At exhaustion, the engine suspends work
before another reconciler invocation. Success or explicit redrive resets both budgets.

Shutdown cancels calls, waits for them to return, and releases claims with a
bounded cleanup context. An acknowledged release preserves both budgets. A failed
or uncertain release can still require expiry recovery.
Lease loss allows polling to continue. Other store errors stop workers and return
to the host. Unknown write outcomes remain errors, even if storage applied them.
Do not replay a completion to infer its result. Recover through current queue state.

`ReconcileOne` supports host-managed dispatch. `Run` polls with a per-invocation
concurrency limit. It does not enforce a cluster-wide or tenant-wide budget.

## Verification and limits

All adapters run the shared contract and independent operation-sequence oracle.
Tests cover concurrent enqueue and claim, stale and duplicate completion, events
during processing, scheduling, diagnostics, cancellation, and independent clients.
PostgreSQL tests cover row locking, rollback, process interruption, and unclean
restart. DynamoDB Local tests cover pagination, response loss, decoder failures,
conditional races, clock rollback, malformed records, and service restart.
The verifier runs vet, race checks, and bounded fuzzing.

The memory adapter supports ephemeral single-process use. Its test reopen
operation reuses an object and does not prove process durability. Emulator tests do not establish
live AWS behavior. Tests do not prove power-loss or replication-failover guarantees.
DynamoDB uses transactional ready and timer entries. Discovery excludes retained
history and future timers. It compares elapsed timer hints directly and limits
promotion to eight timers per kind per discovery call. Hint reads still scale
with the expired set. See [dispatch performance](dispatch-performance.md) and
[consumer validation](consumer-validation.md).

Metrics, priority, and protected retry delays are implemented. Shared admission
remains future work. No independent
reviewer has assessed this refactor.

See [resource keys](resource-keys.md), [controller operations](controller-operations.md),
and [adding a datastore](../adding-a-datastore.md).

See [renewal and crash budgets](../adr/0005-renewable-claims-and-crash-budgets.md) for the recovery protocol and alternatives.

## Operational inspection

`datastore.Inspector` is an optional interface implemented by all included adapters.
It reads bounded pages of queue metadata within one kind. Dispatch does not depend
on this interface. See [controller operations](controller-operations.md#queue-inspection)
for cursor, lifecycle, and consistency semantics.

## Scheduling and observability

See [priority and protected delays](../adr/0007-priority-and-protected-delays.md)
for persistence, concurrent enqueue behavior, and starvation limits. The optional
Prometheus collector reports process-local operations and active dispatches.
The host owns registration and serving; metrics never authorize queue transitions.
