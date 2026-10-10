# Consumer validation

The [publisher prototype](../../packages/reconcile/examples/publisher/README.md)
exercises the core through a consuming Go application. It uses public APIs and
builds a runnable CLI. It is a validation example, not the first factory service.

## What the consumer owns

The application owns desired document revisions, an outbox, immutable output
identities, and observed revisions. PostgreSQL persists these records for both
queue backends. No application payload enters the coordination store.

The queue owns delivery schedules, diagnostics, retry budgets, and temporary
claims. The engine keeps queue tokens inside dispatch. The consumer protects its
own writes with desired-state revision checks. Its output owner enforces a stable
publication identity. A stale queue claimant can still execute application code;
queue fencing cannot substitute for those application checks.

We compared application-owned state with embedding progress in queue records.
Embedding progress would couple workflow schema and migration to every adapter.
It would also hide the delivery boundary. The prototype keeps ownership separate
and uses an application transaction to create an outbox obligation with each
state change.

## Acceptance evidence

The same consumer scenarios run against PostgreSQL and disk-backed DynamoDB Local.
They also run in native race checks. The Buck test target passes the built CLI
artifact to its smoke test. Native checks build and supply that same artifact.

| Boundary | Exercised behavior |
| --- | --- |
| Desired state to enqueue | Lost enqueue reply retains the outbox row; a retry delivers it |
| Concurrent delivery | Two relay clients drain rows with application row locks |
| Active callback to new input | A newer revision and priority survive the old callback's completion |
| Old observation to new desired state | A blocked old write cannot acknowledge a newer revision |
| Publication to observation | Process exit retains the publication; replacement controllers adopt it |
| Observation to queue completion | Process exit retains application progress; replacement dispatch finishes the queue entry |
| Queue completion to acknowledgement | A lost reply surfaces an error; a fresh host resumes from stored schedules |
| Temporary and permanent failures | Retry floors survive enqueue; suspension and redrive retain or reset budgets as specified |
| Host integration | CLI operations, inspection, shutdown, and a host-owned metrics registry work |

Process recovery uses separate OS processes with independent pools and adapter
clients. Each interrupted case starts two replacement controllers. Output counts
must match the number of immutable document revisions, without duplicates.
The output table models provider idempotency. It does not test a real provider or
prove exactly-once external execution.

The stale-observation test waits for a real PostgreSQL row-lock conflict before
committing a new desired revision. It then checks that the old observation did
not acknowledge that revision. A deliberately broken copy of the example removes
the revision predicate to verify that the test detects the error.

## Interface conclusions

These scenarios require no new core API. `Store.Enqueue`, `Engine.Run`, the
key-based `Reconciler`, follow-up results, inspection, and redrive cover this
workflow. Keeping application revisions outside queue tokens works when the
application and effect owner enforce their own write rules.

A host must supervise outbox delivery and restart dispatch after store errors.
The example's relay drains one pass and its worker returns store errors. Those
host responsibilities are deliberate. The queue cannot make a separate
application write and enqueue atomic.

This evidence supports the current consumer boundary. It does not establish that
every future factory workflow fits it. Cluster-wide capacity, execution runtimes,
provider cancellation, authorization, and artifact storage remain application or
future adapter work. A real runtime must prove its own launch and recovery contract.

## Reproduce

```sh
export PATH="$PWD/.tools/bin:$PATH"
./buck2 test //packages/reconcile/examples/publisher/tests:test
./buck2 run //tooling:verify
```

Both commands require Podman or Docker. The tests provision and remove pinned
local services. Keep verification reports outside the source tree with
`BUCK2_REPORT_DIR`. The full verifier covers the existing adapter contracts,
consumer integration, Go vet, native race tests, and core input fuzzing.

See [dispatch performance](dispatch-performance.md) for the separate expiry-burst
experiments. Local service timing and request counts do not establish managed AWS
latency, billed capacity, throttling behavior, or availability. This review used
one agent; it was not an independent review.
