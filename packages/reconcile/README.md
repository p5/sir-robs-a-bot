# Reconciliation core

This Go library provides a durable key-based reconciler queue across controller instances.
Maintainer: the repository maintainers.
Its intended consumer is the factory application. The executable
[publisher prototype](examples/publisher/README.md) exercises the public API
with separate application state, outbox delivery, and two controller processes.

The [datastore package](datastore/README.md) owns `Store`, `Key`, `Item`,
`Claim`, and `Completion`. Reconcilers receive `datastore.Key`. Queue ownership tokens remain inside dispatch.
The engine requires a coordination store. It owns no authoritative in-memory queue.
The [PostgreSQL adapter](datastore/postgres/README.md) and
[DynamoDB adapter](datastore/dynamodb/README.md) supply durable coordination.
The [memory adapter](datastore/memory/README.md) supports explicitly ephemeral,
single-process use. It cannot coordinate distributed controllers or survive restart.
See [adding a datastore](../../docs/adding-a-datastore.md) to implement another backend.

## Use the library

Implement `Reconciler`, then supply a store and explicit limits:

```go
engine, err := reconcile.New(store, map[string]reconcile.Reconciler{
    "request": requestReconciler,
}, reconcile.Config{
    Concurrency:   4,
    LeaseDuration: time.Minute,
    CallTimeout:   30 * time.Second,
    PollInterval:  time.Second,
    RetryInitial:  time.Second,
    RetryMax:      time.Minute,
    MaxFailures:   5,
    MaxAbandoned:  3,
})
```

Handle the constructor error before calling `engine.Run(ctx)`.
Use `engine.ReconcileOne(ctx)` when a host manages dispatch itself.
Enqueue a resource with `store.Enqueue(ctx, key)`. Repeated requests coalesce.
An enqueue during processing survives completion. `store.Get(ctx, key)` reads
queue diagnostics for a known key.

Reconcilers receive a key and return `Result` or an error. Load desired state and
persist observations through consumer-owned dependencies. Return only after
progress is durable. Use `Again: true` and `After` for delayed follow-up.
Errors override the result and use the configured retry policy.

Consumers must reliably deliver application changes through an outbox, change
stream, or periodic resync. A state write followed by a best-effort enqueue can
lose work. Repeated reconciliation must tolerate progress saved before a crash.
See [application delivery](../../docs/design/core.md#application-persistence-and-delivery).

Claims protect store writes. They do not guarantee exactly-once external effects.
Reconcilers must tolerate repeated calls and honor context cancellation.
The engine renews active claims every third of `LeaseDuration`. `CallTimeout`
may exceed the lease duration, but remains a finite bound. Renewal failure cancels
the call and prevents completion. Shutdown releases claims after calls stop.
See [the core design](../../docs/design/core.md) for races and failure semantics.

## Build and verify

Use the pinned Go 1.27.2 distributions through Buck2:

```sh
export PATH="$PWD/.tools/bin:$PATH"
./buck2 build //packages/reconcile:reconcile //packages/reconcile:fmt-check
./buck2 test //packages/reconcile/...
./buck2 run toolchains//:gofmt -- -w packages/reconcile/*.go packages/reconcile/datastore packages/reconcile/datastore/storetest packages/reconcile/tests packages/reconcile/internal packages/reconcile/examples
./buck2 run //tooling:verify
```

Tests import the library from a separate consumer package.
The shared suite is in `datastore/storetest`; future adapters supply an isolated fixture, elapsed store time, and a reopen function.
The clock harness under `internal/teststore` wraps the public memory adapter.
PostgreSQL and DynamoDB integration tests require Podman or Docker. They launch and remove isolated datastore containers. The `fmt-check` target checks project-owned Go files.

`go.mod` declares external Go dependencies. Run
`bash tooling/go/generate.sh` after dependency changes.
The root Go workspace selects dependencies. The bundled gobuckify generates
Buck targets in the shared root vendor tree.
Do not edit vendored code or generated targets by hand.
The repository verifier regenerates dependencies in a temporary directory and
checks module metadata, vendored sources, and Buck targets for drift.
The dependency macro keeps CGo disabled and narrows generated target visibility.

Build artifacts are Buck Go archives and test executables.
Supported toolchains cover Linux and macOS on x86_64 and ARM64. CI covers Linux on both architectures.
CGo is disabled. Applications own database configuration, migrations, and lifecycle.

## Resource keys and operations

Use stable resource references as IDs. Keep dispatch in `Kind` and desired inputs
in consumer-owned application state. See [resource keys](../../docs/design/resource-keys.md) for identity
rules and examples.

Return `reconcile.Permanent(err)` for failures that need changed input or an
explicit redrive. Other failures use jittered retries until the failure limit.
`Run` continues after lease loss; `ReconcileOne` returns it to a caller managing
dispatch. Set `Config.Logger` to route structured lifecycle logs.
See [controller operations](../../docs/design/controller-operations.md) for
implemented inspection and the proposed shared-capacity boundary.

## Adversarial checks

The verifier runs Go vet, race checks, and bounded fuzzing in addition to Buck
unit and datastore integration tests. See [adversarial testing](../../docs/design/adversarial-testing.md)
for the input coverage, local commands, regression workflow, and remaining limits.

## Crash recovery and redrive

Recovery of an expired active claim increments `Abandoned` atomically with the
next fence. `MaxAbandoned` limits recovered claims since the last success or
explicit redrive. Zero uses `MaxFailures`. Exhausted work is suspended before
another reconciler call. Reported errors have their own `MaxFailures` budget.

`store.Enqueue` retains diagnostics and both budgets. Use `store.Redrive(ctx, key)`
to reset them explicitly. Redrive returns `ErrBusy` for an active claim and
`ErrNotFound` for an unknown key. Consumers authorize operator access.
See [the recovery decision](../../docs/adr/0005-renewable-claims-and-crash-budgets.md).

## Inspect queue work

All included adapters implement the optional `datastore.Inspector` interface.
Use `store.List(ctx, datastore.ListRequest{Kind: "request", Limit: 50})` to read
metadata, then pass `page.Next` as `After` for the next page of the same kind.
Stop when Next is empty. Each page returns at most the requested limit, from 1
to 100. Pages do not form a snapshot across requests.
See [inspection semantics](../../docs/design/controller-operations.md#queue-inspection).

## Scheduling and metrics

Pass `datastore.EnqueueOptions{Priority: 100}` as an optional enqueue argument to
raise urgency. Success with no follow-up clears priority. Error retries preserve
backoff despite repeated enqueue. Use `Result.ProtectDelay` for an explicit
protected successful wait; redrive clears that floor.

Create `reconcile.NewMetrics()`, register it with a host-owned Prometheus registry,
and set `Config.Metrics`. The collector reports operation counters, duration
histograms, and active dispatches. It starts no listener and uses no global registry.
See [controller operations](../../docs/design/controller-operations.md) for labels,
outcome semantics, and the difference between callback return and queue completion.
The official Prometheus client is declared in `go.mod` and vendored through the
same generated dependency graph as the datastore clients.

See [dispatch performance](../../docs/design/dispatch-performance.md) for benchmark
commands, measured memory improvements, and DynamoDB discovery limits.
