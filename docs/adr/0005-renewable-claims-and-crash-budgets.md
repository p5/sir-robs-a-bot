# Renew claims and bound abandoned work

The key-based queue recovered expired claims but counted only reported errors.
A poison key could repeatedly crash controllers without reaching a retry limit.
Calls also had to fit one fixed lease. Add durable abandonment accounting,
engine-owned renewal, orderly release, and explicit redrive.

## Alternatives and decision

Fixed leases with bounded observations keep ownership simple but constrain call
duration and leave crash loops unbounded. Counting every claim as a failed
attempt bounds crashes, but makes successful waiting and orderly shutdown consume
the same budget unless completion reverses that accounting.

Keep reported failures and abandoned claims separate. Recovery of an active,
expired claim atomically advances the fence and increments `Abandoned`. Successful
completion clears both counters. Reported errors preserve abandonment history.
Ordinary enqueue preserves both counters. `MaxAbandoned` defaults to `MaxFailures`.
When recovery reaches the abandonment limit, the engine suspends the key before
calling its reconciler. The terminal completion records an exhaustion diagnostic.
A concurrent enqueue can retain pending work, but cannot reset the budget or
cause another reconciler invocation. Controllers sharing work must use consistent
budgets; these limits belong to dispatch policy, not a stored queue configuration.

Renewal belongs to dispatch. Reconcilers continue to implement
`Reconcile(ctx, key)` and receive no lease handles. The engine renews every third
of `LeaseDuration`. Each renewal has at most that interval to complete.
`CallTimeout` remains finite and may exceed the lease duration. Any renewal error
cancels the call; the engine joins the heartbeat and waits for the reconciler to
stop before returning. It does not complete work after a renewal error.

`Renew(ctx, claim, duration)` checks the active fence and never shortens a lease.
Expiry permits takeover but does not alone revoke ownership. Renewal and takeover
compete through atomic conditional writes. A successor invalidates the old token.
Queue fencing still cannot stop a paused process or protect application effects.

## Shutdown and operations

After parent cancellation and cooperative callback return, dispatch calls
`Release(ctx, claim)` with a cleanup timeout of one renewal interval. Release
preserves diagnostics and counters, makes work due, and relinquishes ownership.
It never releases a successor's claim. An acknowledged release avoids charging
orderly shutdown as abandonment. A failed or unknown release can still leave work
for expiry; the engine reports that uncertainty and does not retry the mutation.
A shutdown that kills the process before release has the same durable evidence as
any other abandoned claim. No protocol can distinguish intent that was not saved.

`Redrive(ctx, key)` resets both budgets and diagnostics, advances the request
sequence, and makes known work due. It rejects active claims with `ErrBusy`, even
after expiry. Recover and suspend abandoned work before redriving it. Authorization
and any user-facing operation belong to the consuming service. Redrive retains
fence history and never deletes a queue record.

## Persistence and proof

PostgreSQL updates lease and counters under row locks. DynamoDB uses strong reads
and revision-conditional writes for lease transitions. Definite revision conflicts
retry; unknown write outcomes return errors without mutation retries. Renewals do
not copy or interpret application data, because the queue stores none.

The storage format remains version 1. These changes define the initial queue
format; discarded development formats need no migration path. Both adapters
continue to reject unsupported version numbers.

Shared adapter tests cover renewal, takeover, stale release, non-shortening leases,
abandonment persistence, orderly release, redrive, and invalid inputs. Engine tests
cover heartbeats, cancellation after renewal failure, budgets, and shutdown.
DynamoDB fault tests cover response loss and concurrent enqueues during lease
changes. Operation-sequence fuzzing covers renewal, release, and redrive.

Tests do not prove live AWS behavior, host clock synchronization, or enforcement
against an uncooperative reconciler. No independent reviewer assessed this change.
Paginated inspection, queue metrics, priority, protected delays, and measured
large-backlog dispatch remain future work.
