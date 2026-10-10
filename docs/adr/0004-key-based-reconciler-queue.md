# Keep application state outside the reconciler queue

The factory needs a reusable durable reconciler queue like Driftlessaf's workqueue.
The initial core combined application snapshots with coordination. That made
progress and scheduling atomic, but required every consumer to store its desired
and observed bytes through the core. The user chose a key-based queue instead.

## Alternatives and decision

Retaining the combined store would keep atomic application progress and scheduling
but make the queue responsible for application versioning and payload limits.
A key-based queue keeps coordination reusable across services with their own
state owners. It requires each consumer to establish reliable change delivery.
Choose the key-based queue. This supersedes the snapshot ownership in ADR 0002
and the application-payload representation in ADR 0003. Their fencing and
backend timing decisions still apply.

A caller uses `store.Enqueue(ctx, key)`. The engine calls
`Reconcile(ctx, key)`. The reconciler reads and persists application state through
its own dependencies, then returns scheduling instructions. Queue completion
records only diagnostics, scheduling, and ownership release.

## Recovery obligations

The consumer must deliver state changes through an outbox, recoverable change
stream, or resync. An enqueue after a state write is not an atomic protocol.
Persist progress before returning success. A crash between progress persistence
and queue completion must be safe to repeat. Queue claims cannot authorize or
reject writes to a separate application datastore. Consumers own that concurrency
policy and external effect idempotency.

Enqueues coalesce by namespace, kind, and opaque ID. Each advances a sequence.
Completion cannot erase a later request. Completed records retain their fence;
removing them without a replacement generation strategy would permit token reuse.
Enqueue preserves failure diagnostics. Success resets them. Explicit redrive and
crash-attempt budgets remain separate future decisions.

## Storage format

Both adapters use schema version 1 for the key-based queue. The factory is still
in development; discarded prototypes have no compatibility or migration contract.
The [recovery decision](0005-renewable-claims-and-crash-budgets.md) adds renewal,
redrive, and crash budgets to this initial format.

## Evidence and limits

Shared contract tests exercise concurrent producers, exclusive claims, takeover,
late completion, enqueue retention, delays, cancellation, and independent clients.
Both backend suites retain restart and process-interruption tests. A consumer test
keeps progress outside the queue across engine replacement; its map is a test
fixture, not proof of a durable application protocol.

No real factory consumer exists yet to prove outbox or resync delivery. No
independent reviewer assessed the refactor. Lease renewal, metrics, inspection,
and bounded poison-key recovery remain follow-up work.
