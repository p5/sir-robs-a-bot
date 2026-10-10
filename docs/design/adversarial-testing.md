# Adversarial testing

The repository verifier runs deterministic Buck tests, Go vet, native Go race
checks, and every fuzz target discovered in the core consumer test package.
CI runs the same verifier on Linux x86_64 and ARM64.

## Input coverage

| Boundary | Checks |
| --- | --- |
| Kind, resource ID, namespace | Unicode and control characters, byte limits, exact identity, invalid-input rejection before database access |
| Engine configuration | Arbitrary integer and duration values, invalid combinations, nil reconciler function |
| Claim and completion | Counter ranges, zero generations, contradictory flags, negative delays, malformed failure text |
| Reconciler result and error | Extreme delays, error precedence, permanent errors, diagnostic text normalization |
| Store operation histories | Coalesced enqueue, stale and duplicate completions, enqueue retention, failure and abandonment bookkeeping, renewal, release, redrive, metadata copy isolation, independent clients |
| DynamoDB storage and responses | Constructor/key fuzzing, SDK response fuzzing, counter overflow, schema/identity rejection, indexed schedule responses, timer pagination, transaction conflicts, lost write responses |
| PostgreSQL storage | Maximum key sizes, SQL metacharacters, counter overflow rollback, rejection without mutation |
| Concurrency and recovery | Competing clients, completion after expiry during lock waits, cancellation, controller process exit, transaction rollback, unclean database restart |

Fuzz targets use the memory adapter with a controlled clock, pure adapter input
checks, and an in-process HTTP response fixture for DynamoDB SDK decoding. They do not open database connections
or launch containers in fuzz workers.
The operation-sequence oracle is separate from the memory adapter implementation.
Both durable adapters replay its fixed corpus against independent clients.
The sequence runner limits each history to 128 operations and does not model
expiry. Dedicated contract tests cover expiry and stale ownership.

The engine renders error messages as valid UTF-8 and escapes NUL bytes before
persisting them. Direct completions with invalid failure text are rejected.
Application payload validation belongs to consuming services.

## Run and reproduce

```sh
export PATH="$PWD/.tools/bin:$PATH"
./buck2 run //tooling:verify
```

Each discovered fuzz target runs for ten seconds with two workers. Fuzz seeds
also run during ordinary Buck tests. For longer local exploration:

```sh
FACTORY_FUZZ_SECONDS=60 bash packages/reconcile/checks/adversarial.sh
```

The script copies the module outside the source tree. Go writes generated
corpora there. Successful runs remove the copy. Failed runs retain it and print
its path. Reproduce a reported failure with the pinned Go launcher and that copy:

```sh
./buck2 run 'toolchains//:go[go]' -- -C /tmp/<retained-workspace>/module \
  test ./tests -run='FuzzTarget/<reported-hash>'
```

Add a minimal regression to the checked-in Go seeds or `SequenceCorpus` before
fixing a defect. Do not commit generated execution artifacts. Add sequence
regressions to the shared corpus so PostgreSQL and future adapters replay them.
CI uploads generated failing corpus entries with the verification reports.

Native race checks require a host C compiler and Podman or Docker for datastore
tests. Application Buck targets still disable CGo. The Go distribution remains
pinned. Race checks use the host compiler supplied by the development or CI host.

## Limits

Timed fuzz runs explore inputs; they do not exhaust them. The state model checks
sequential histories. It does not enumerate all concurrent interleavings.
Database replay uses the checked-in histories, not every generated fuzz input.
Tests do not simulate host power loss, network partitions, or replication failover.

The core currently has no HTTP ingress or application payload decoder. Each
future transport and owning resource module must add tests for its own parsing,
authorization, and payload-size rules. Go clients and reconciler implementations
are trusted application code. These tests do not sandbox their methods or contain
arbitrary panics from them. The DynamoDB boundary contains SDK response-decoder
panics and has a minimized regression seed. It does not catch application panics
or exhaustively test all vendored dependency internals.

Shared recovery tests cover durable abandonment, non-shortening renewals, stale
release, and explicit redrive. Engine tests cover renewal cancellation and orderly
shutdown. DynamoDB tests inject response loss in all lease mutations.

Inspection fuzzing covers kinds, exclusive resource ID cursors, page bounds, and
rejection before backend access. DynamoDB response fuzzing also exercises listing,
including continuation keys and ordered record decoding. Shared inspection tests
cover concurrent inserts and namespace isolation without claiming snapshot reads.

The memory scheduling fuzzer uses a separate scan-based oracle. It checks priority,
protected delays, clock rollback, expiry, and heap updates across generated histories.
Completion and reconciler-result fuzzers cover invalid protected-delay combinations.
Shared adapter tests check priority escalation, enqueue before and after protected
completion, and explicit redrive. DynamoDB tests place the highest-priority key
beyond the bounded candidate window.

DynamoDB indexed scheduling tests inspect resource and schedule membership after
all lifecycle transitions. They place maximum-priority work beyond the first
timer page, bound promotion across shards, interrupt a burst partway through,
race promotion with renewal and completion, and recover after a lost promotion
reply. A separate scheduling-response fuzzer exercises ready and timer decoding,
including nonadvancing continuation responses, without AWS access. Transaction-conflict
responses are injected because DynamoDB Local does not produce them.


The executable [consumer prototype](../../packages/reconcile/examples/publisher/README.md)
adds application outbox delivery, application revision races, two controller
processes, crash recovery before queue completion, CLI smoke checks, and host-owned
metrics. A disposable negative test removes the application revision predicate
and confirms that both backend scenarios reject the resulting stale write.
See [consumer validation](consumer-validation.md).
