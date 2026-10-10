# Dispatch performance

The steady-state benchmarks measure an enqueue, claim, and successful completion
cycle. Burst benchmarks measure first dispatch and complete draining separately.
Run them separately from correctness checks. Container startup and fixture setup
are outside the timed loops. Durable adapter benchmarks use local containers,
not managed services. They do not establish production throughput or availability.

```sh
export PATH="$PWD/.tools/bin:$PATH"
./buck2 run 'toolchains//:go[go]' -- -C packages/reconcile test ./datastore/memory \
  -run='^$' -bench='^BenchmarkDispatchBacklog$' -benchmem -benchtime=1s
./buck2 run 'toolchains//:go[go]' -- -C packages/reconcile test ./tests \
  -run='^$' -bench='^BenchmarkEngineDispatch$' -benchmem -benchtime=1s
./buck2 run 'toolchains//:go[go]' -- -C packages/reconcile test \
  ./datastore/postgres/tests ./datastore/dynamodb/tests \
  -run='^$' -bench='^BenchmarkDispatchBacklog$' -benchmem -benchtime=10x
```

## Current implementation

Memory keeps completed records for fence history but removes them from dispatch
indexes. Ready heaps order eligible entries by priority, due time, kind, and ID.
Future heaps order delayed and active entries by eligibility time. Mutations repair
one heap entry. Claim promotes entries whose schedules have elapsed and compares
the ready head for each registered kind. This avoids a scan of retained records
on every claim. A backwards custom clock triggers an index rebuild.

PostgreSQL uses a partial pending-work index ordered by namespace, descending
priority, due time, kind, and ID. Completed records do not occupy that index.
Claims use `FOR UPDATE SKIP LOCKED`. Future high-priority records and live leases
can still add filtering work. Measure representative schedules and contention;
an index alone is not a latency guarantee.

DynamoDB keeps resource authority separate from ready and timer entries in the
same table. Each mutation maintains both through a conditional transaction.
Claim compares elapsed timer hints with bounded ready heads, preserving numeric
priority without evaluating retained completed records or future timers. It
promotes at most eight timers per kind per discovery call, across all shards,
and skips the best candidate. Direct timer claiming preserves priority while
promotion amortizes later reads. Hint traversal still scales with expired work.
Default one-shard idle discovery uses two Queries per kind. Existing-key enqueue
uses one strong read and one transaction; lease renewal also maintains its timer.
These writes cost more than the previous single-item update path.

## Measurements from 2026-10-10

These samples came from one Linux amd64 host with an Intel i9-13900H. Short runs
and host load make them unsuitable as service-level objectives.

| Memory retained completed records | Before heap indexes | After heap indexes |
| --- | --- | --- |
| 1 | 313 ns/cycle | 335 ns/cycle |
| 1,000 | 10.3 µs/cycle | 332 ns/cycle |
| 10,000 | 116 µs/cycle | 332 ns/cycle |

Both memory implementations allocated zero bytes per timed cycle. These runs used
200 ms benchmark durations. Full engine dispatch, including cooperative context
and heartbeat setup, measured about 7.8 µs without metrics and 10.8 µs with metrics,
with 35 allocations per cycle in both runs. Repeat measurements before drawing
conclusions about small changes or exporter overhead.

Before transactional scheduling, the local DynamoDB benchmark used six requests per cycle with no completed cold
records, including one discovery Query. With 256 cold records it used seven
requests, including two Queries. This demonstrates the former linear discovery
cost; bounded candidate memory does not fix it. Three-iteration latency samples
varied enough that they are not useful capacity estimates.

The indexed adapter used two discovery Queries and eight total requests per
cycle with both zero and 256 retained completed records after initialization.
These three-iteration samples confirm request-count independence from retained
history at those sizes. They do not measure expiry bursts. Repeat the benchmark
to measure transaction overhead; local elapsed time does not establish AWS
capacity or latency.

Before a large-backlog deployment, measure live capacity, throttling, transaction
conflicts, priority distribution, due-timer bursts, renewal cost, and controller
count. See [the discovery prototype evaluation](dynamodb-discovery.md) and
[the implemented decision](../adr/0008-dynamodb-indexed-scheduling.md).


## Expired timer bursts

The burst benchmark prepares delayed keys through public operations, advances a
shared test clock, and measures one claim per controller. One low-priority ready
key competes with the expired timers. Single-controller cases must select the
highest numeric priority. Competing claims must have distinct resource keys.
The drain benchmark completes every key and includes the final empty discovery.
Setup, container startup, and immutable-settings checks are outside measurement.

```sh
./buck2 run 'toolchains//:go[go]' -- -C packages/reconcile test \
  ./datastore/dynamodb/tests -run='^$' \
  -bench='^BenchmarkExpiredTimer(Burst|Drain)' -benchtime=1x -timeout=10m
```

We compared three implementations in disposable copies outside the source tree.
They used the same pinned DynamoDB Local image, fixture, and workload. These are
one-iteration samples on the host above. Request counts are observations, not
AWS billed-capacity measurements. Latency includes local service and host load.

| Strategy | First claim with 128 timers, one controller | Requests before that claim | Requests before four competing claims |
| --- | --- | --- | --- |
| Promote the entire due set | 4.17 s | 261 | 974 |
| Compare timers directly, no promotion | 54 ms | 5 | 31 |
| Compare timers and promote at most eight | 283 ms | 19 | 82 |

With 512 timers, promoting the entire set needed 1,032 requests before the first
claim and 3,884 requests before four competing claims. Direct comparison needed
eight and 43 respectively. The first claim took about 17 seconds with full
promotion and 39 ms with direct comparison in these short local runs.

The final adapter repeated the 512-timer first-dispatch test after integration.
It used 22 requests and eight transactions with one controller, with the first
claim at 277 ms. Four controllers used 93 requests and 31 transaction attempts;
their first claim arrived at 177 ms. These timings remain local samples.

First-dispatch improvement alone is insufficient. The complete 512-timer drain
also includes one previously ready key:

| Strategy | Requests per drain | Transactions per drain | Scheduling entries returned |
| --- | --- | --- | --- |
| Promote the entire due set | 4,108 | 1,538 | 58,048 |
| Compare timers directly, no promotion | 3,852 | 1,026 | 131,841 |
| Compare timers and promote at most eight | 4,202 | 1,537 | 72,960 |

Direct comparison saved promotion writes but returned over twice as many
scheduling entries during the full drain. Bounded promotion reduced that repeated
traversal while avoiding mutation work proportional to the whole burst before
first dispatch. Its drain used about 2% more requests and 26% more returned hints
than full promotion in this fixture. This is the implemented compromise.

Eight is an adapter-owned promotion budget, not a public scheduling contract or
a service-level objective. A discovery call still reads every timer due at its
cutoff. It can perform up to eight promotion transactions per kind, plus claim
attempts and clock-rollback repairs. Contention, registered kinds, and shards add
work. Total expired-set reads, billed capacity, or dispatch latency are not fixed
by this budget. Continuous large expiry sets still require deployment-specific
measurement. Never cap timer traversal and then claim only promoted work while
advertising the same numeric-priority guarantee.

Regression tests put urgent work beyond the first timer page, bound promotion
across shards, interrupt and resume discovery, and race renewal and completion
with promotion. No independent reviewer or live AWS benchmark was used.
