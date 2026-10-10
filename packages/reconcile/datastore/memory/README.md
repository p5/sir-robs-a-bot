# Ephemeral memory datastore

This adapter implements the queue contract within one process.
Maintainer: the repository maintainers.
Use it for local development, tests, or consumers that explicitly accept lost
work after process exit. Distributed factory controllers need PostgreSQL or DynamoDB.

```go
store := memory.New(memory.Config{})
```

Pass this store to `reconcile.New`. Share the same store pointer between local
producers and controllers. Independent instances have independent records and
ownership generations. Replacing an instance loses pending work, diagnostics,
retry budgets, and fence history. Old callbacks must stop before replacement.

The adapter supports all `datastore.Store` operations and optional
`datastore.Inspector.List`. A mutex serializes operations. It returns metadata
copies; callers cannot mutate queue records through returned values. Every
ownership transition and counter change happens in the same critical section.
Counter overflow fails without changing records.

`Config.Clock` defaults to `time.Now`, which retains its process-local monotonic
clock for scheduling. A custom clock must be concurrency-safe and must not return
the zero time. The internal test harness uses this seam to advance time without
sleeping. The adapter does not start background goroutines or need a close call.

Records remain in memory to preserve fence generations. Claim uses per-kind ready and future heaps; completed records do not add claim
scan work. A backwards custom clock rebuilds those indexes. Inspection still scans
and sorts matching IDs. See [benchmarks](../../../../docs/design/dispatch-performance.md). There is no deletion, eviction, disk
persistence, replication, or coordination between processes.

The package has no external dependencies. Buck2 produces a Go archive.

```sh
export PATH="$PWD/.tools/bin:$PATH"
./buck2 build //packages/reconcile/datastore/memory:memory
./buck2 test //packages/reconcile/datastore/memory:test
```

Tests cover the shared queue and inspection contracts, operation sequences,
default-clock expiry, independent instances, and overflow. The repository verifier
also runs race checks and core fuzzers against this implementation. Reopening the
same instance in a contract fixture proves retained local state, not durability.

Priority, protected delays, and pending notifications change with the record under
its mutex. Error retries cannot be accelerated by enqueue. Redrive clears a protected
floor. The scheduling fuzzer checks heap behavior against a separate scan-based model.
