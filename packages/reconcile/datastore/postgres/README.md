# PostgreSQL coordination store

This adapter implements `datastore.Store` through `database/sql`.
Applications select a driver, open the pool, and close it during shutdown.
The adapter imports no driver. Integration tests use `github.com/lib/pq`.

Call `postgres.Migrate(ctx, db)` with a migration identity before starting
controllers. Migrations serialize across clients and reject unknown schema
versions. The database must enable `fsync` and `full_page_writes`.
Every write transaction sets `synchronous_commit=on`.

Construct the adapter with `postgres.New(db, namespace)` and pass it to
`reconcile.New`. Construction does not connect, migrate, or launch goroutines.
Controllers need access to the schema version table and read/write access to
resource rows. They do not need permission to create tables.

Namespaces isolate resource keys, not database permissions. Use database roles
or separate databases when clients must not access each other's resources.
The pool's search path selects the schema. Configure it consistently on every
connection and keep untrusted users from creating objects in that schema.

The adapter uses conditional SQL, row locks, and PostgreSQL time. Claims skip
locked rows. Commit checks the active claim and fence under the row lock. Expiry permits
recovery; completion remains valid until a successor claims the resource.
Fence and enqueue counters must fit signed PostgreSQL bigint values.
Durations round up to microseconds. Kinds accept at most 256 bytes, IDs 1024
bytes, and namespaces 128 bytes. None may contain only whitespace.

## Tests

```sh
export PATH="$PWD/.tools/bin:$PATH"
./buck2 test //packages/reconcile/datastore/postgres/tests:test
```

Tests require Podman or Docker and permission to launch containers. They start
an isolated PostgreSQL 18.6 container from a pinned multi-platform image.
The port binds to loopback. Tests remove their container and volume at exit.
No credentials or runtime files enter the source tree.

The suite runs the shared store contract, competing clients, row lock contention,
transaction rollback, cancellation, namespace isolation, controller process
interruption, and an unclean database restart with its existing volume.
It does not simulate host power loss, replication failover, or managed service
availability. Deployment durability also depends on storage and replication settings.

## Storage compatibility

The queue uses schema version 1. Unsupported version numbers are rejected.
Discarded development formats have no migration contract.
Completed records remain stored to preserve fence generations. Applications own
all desired state, observations, artifacts, and reliable change delivery.

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
