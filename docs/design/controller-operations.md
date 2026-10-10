# Controller operations

## Implemented behavior

`ReconcileOne` returns `ErrLeaseLost` when the store rejects a completed or replaced
claim. `Run` treats this as an ownership outcome and continues polling.
Other store errors stop its workers and return to the host. An unknown commit
outcome remains an error. The engine must not replay a commit to guess its result.

`reconcile.Permanent(err)` suspends retries after that call. Wrapping preserves
`errors.Is` behavior. An enqueue can make the resource due again, but preserves retry budgets.
Explicit redrive resets them when no claim is active.
A concurrent enqueue survives even a permanent failure.
Other reconciler errors use the configured failure limit.

Retry ceilings double from `RetryInitial` to `RetryMax`. The actual delay uses
equal jitter in the upper half of that ceiling, rounded to a positive duration.
This spreads retries across controllers. The store persists the selected delay.

`Config.Logger` accepts a standard `slog.Logger`. Nil uses `slog.Default()`.
Lifecycle records include `event`, `kind`, `resource_id`, `fence`, `abandoned`, and `failures`.
Completion records include call duration and requested scheduling.
Events are `claimed`, `completed`, `retry_requested`, `suspension_requested`,
`interrupted`, `lease_lost`, `commit_failed`, `renewal_failed`, and `release_failed`.
The queue holds no application payloads. Error messages are logged.

Completion events follow the store's acknowledgement. A commit error emits no
completion event, even if the backend might have committed it. Retry and suspension
events describe this attempt's requested outcome. A concurrent enqueue can keep the
resource pending. The engine cannot infer its final schedule from that request.
Logs are operational evidence, not a durable event history or metrics API.
Hosts own log destinations and metrics serving.

## Queue inspection

All included adapters implement `datastore.Inspector` separately from dispatch.
Call `List(ctx, datastore.ListRequest{Kind: "request", Limit: 50})` to read a page.
Pass a nonempty `page.Next` as `After` for the next request with the same kind.
An empty Next ends traversal. Limits must be between 1 and 100.
The configured store instance selects the namespace; consumers authorize access.

Entries include queue diagnostics, due time, and lease expiry. A nonzero
`LeaseUntil` means a claim is active, including an expired claim awaiting recovery.
`Item.Pending` includes active and delayed work. A nonpending entry with a failure
diagnostic is suspended. Success clears diagnostics and both retry budgets.
Use `Redrive` to reset those budgets and request another attempt.

IDs sort by UTF-8 bytes. The cursor is the last resource ID, not an ownership token.
It need not still identify a record. Inserts before the cursor appear only in a
new traversal. Concurrent updates can change diagnostics between pages. A nonempty
Next can lead to an empty final page. These observations are not an event history
or a consistent queue-wide snapshot.

DynamoDB makes one strongly consistent Query per page and follows no pages
internally. A store instance also checks immutable namespace settings on its first
operation; it caches those settings once they exist. PostgreSQL uses an indexed keyset query. Memory scans local records.
No adapter applies a server-side state filter. Consumers can select suspended
entries from pages, and should bound the number of pages each operation reads.
See [the decision](../adr/0006-bounded-queue-inspection.md) for alternatives and limits.

## Shared capacity

`Config.Concurrency` limits one `Run` invocation. With three hosts configured for
four workers each, the factory can run twelve calls. It provides no cluster-wide
or tenant-wide capacity guarantee.

Before deploying multiple hosts against a bounded external service, define the
budget's scope, owner, and recovery policy. A durable capacity reservation must
be acquired atomically with the resource claim. Expiry and completion must release
that reservation without allowing a stale claimant to release a newer reservation.
Counting process workers or reading an eventually consistent metric cannot enforce
this invariant.

Keep admission inside the adapter's claim operation. Do not make reconcilers
coordinate capacity across separate calls. Extend the contract and shared tests
when the first deployment establishes the required budget. Current adapters do
not enforce a shared budget.

## Lease maintenance and recovery budgets

The engine renews every third of the lease duration with a one-interval request
timeout. Renewal failure cancels the callback and prevents completion. The engine
waits for callback return and heartbeat termination. Context cancellation cannot
force uncooperative code to stop.

Recovered expired claims increment `Abandoned`. At `MaxAbandoned`, dispatch records
an exhaustion diagnostic and suspends work without invoking the reconciler.
Ordinary enqueues do not reset the budget. `Redrive` resets both abandonment and
reported failures, and rejects active claims with `ErrBusy`.

Orderly cancellation releases ownership after the call stops, using a cleanup
context bounded to one renewal interval. Release preserves counters and pending
requests. If its outcome is unknown, expiry remains the recovery path.

## Priority and protected delays

`Enqueue(ctx, key, datastore.EnqueueOptions{Priority: 100})` raises scheduling
priority. Higher values run first among eligible keys; omission uses zero.
Ordinary enqueue preserves the maximum stored priority. Success with no follow-up
clears it. Priority does not preempt active calls or guarantee starvation freedom.

Error retries use protected delays. Enqueue during or after completion retains the
request but cannot make it runnable before the retry floor. Reconcilers can return
`Result{Again: true, After: delay, ProtectDelay: true}` for a protected successful
wait. Other follow-ups remain interruptible. Redrive clears the protected floor
explicitly. Inspection exposes that floor in `Entry.NotBefore`.
See [the scheduling decision](../adr/0007-priority-and-protected-delays.md).

## Metrics

The core uses the official Prometheus Go client. Create `reconcile.NewMetrics()`,
register it with a host-owned `prometheus.Registry`, and set `Config.Metrics`.
Share one collector between engines that should contribute to the same series.
There is no global registration or listener. Nil disables collection.

Metrics are `factory_reconcile_operations_total`,
`factory_reconcile_operation_duration_seconds`, and `factory_reconcile_active`.
Operations are claim, callback, renew, release, commit, and dispatch. Phase outcomes
are success, idle, lease_lost, cancelled, and error. Dispatch outcomes are completed,
retried, suspended, interrupted, renewal_error, and commit_error. Callback success
means the callback returned nil; only dispatch completion follows an acknowledged
queue commit. Unknown commit outcomes never count as completed dispatches.

Labels include kind and fixed operation/outcome names. Claim uses an empty kind
because selection spans the registry. There are no resource IDs, error strings,
priority values, or tenant identifiers in labels. Histograms observe seconds.
The active gauge includes dispatch through queue completion and drops on return.

These are process-local observations. They are not durable events, queue depth,
cluster-wide admission, or billing evidence. Hosts choose endpoint authorization,
exporter configuration, scrape intervals, and retention. Use SDK instrumentation
for backend request counts and capacity. See [dispatch benchmarks](dispatch-performance.md)
for metrics overhead and backend costs.
