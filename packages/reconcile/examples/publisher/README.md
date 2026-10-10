# Publisher consumer prototype

This example checks the reconciliation library from a consuming application.
It publishes immutable document revisions. It is not a factory service or a job
runtime. Maintainer: the repository maintainers.

The controller uses only public library APIs. It runs with PostgreSQL or DynamoDB
coordination. Application state always uses PostgreSQL in this example. That
choice demonstrates separate state ownership; it does not require future
DynamoDB consumers to use PostgreSQL.

## State and recovery

`Submit` saves desired state and an outbox row in one application transaction.
`DeliverOne` enqueues the document key and then removes the outbox row. If either
acknowledgement is lost, retry delivery. Duplicate enqueues can cause another call.
Do not replace the outbox with a best-effort enqueue after a state write.

A publication uses the document ID and revision as its stable identity. Its
owning table rejects duplicates and the controller checks immutable content.
`ready_at` models an asynchronous provider. It does not start an external process.
A real provider must enforce idempotency or fencing itself. This example does not
prove that any external provider offers those guarantees.

The controller waits with a protected follow-up, then records observation through
a conditional application write. An old callback cannot acknowledge a newer
revision. Queue fences stay inside the engine; application revisions protect
application writes. Published revisions remain immutable, including superseded
revisions. There is no mutable external "latest publication" pointer to fence.

An empty document body produces a permanent error. New input can enqueue the key
again, while retaining retry budgets. Explicit redrive resets those budgets.
Applications must authorize submission, inspection, and redrive in their hosts.
This local CLI has no authentication layer.

## Run the executable

Use an isolated PostgreSQL database. Keep credentials outside the source tree.
Run commands from the repository root:

```sh
export PATH="$PWD/.tools/bin:$PATH"
export PUBLISHER_DSN='postgres://postgres@127.0.0.1:5432/publisher?sslmode=disable'
./buck2 run //packages/reconcile/examples/publisher/cmd:publisher -- migrate
./buck2 run //packages/reconcile/examples/publisher/cmd:publisher -- submit example 'document content' 100
./buck2 run //packages/reconcile/examples/publisher/cmd:publisher -- relay
./buck2 run //packages/reconcile/examples/publisher/cmd:publisher -- work
```

Run `inspect example` in another terminal. Stop workers with SIGTERM or Ctrl-C.
Workers drain active callbacks and print their host-owned registry in Prometheus
text format on shutdown. The example starts no metrics HTTP listener.
Run `work` in two terminals to use independent controller processes.
`relay` drains the current outbox once. Run it after each submission. Production
hosts need a supervised delivery loop and a restart policy for store errors.

For DynamoDB, add these flags before each command:

```sh
./buck2 run //packages/reconcile/examples/publisher/cmd:publisher -- \
  -queue dynamodb -table publisher -namespace publisher-example \
  -region us-east-1 -endpoint http://127.0.0.1:8000 inspect example
```

The table must already exist with string keys `pk` and `sk`. Omit `-endpoint` for
AWS. This example reads explicit `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and
optional `AWS_SESSION_TOKEN` credentials. A production host should supply its
own deployment credential provider. Local DynamoDB accepts dummy credentials.
The CLI never provisions a DynamoDB table. `migrate` initializes application tables
and, for PostgreSQL coordination, the queue schema. Run it before workers start.
All commands must select the same namespace and backend.

Use `submit example ''`, `relay`, and `inspect example` to observe suspension.
After saving corrected input and delivering its outbox, `redrive example` resets
budgets. Stop workers first if you need to guarantee the key is not active during
redrive; active claims return `ErrBusy`.

## Build and verify

```sh
./buck2 build //packages/reconcile/examples/publisher/cmd:publisher
./buck2 test //packages/reconcile/examples/publisher/tests:test
./buck2 run toolchains//:gofmt -- -w packages/reconcile/examples/publisher
./buck2 run //tooling:verify
```

The test target provisions pinned PostgreSQL and DynamoDB Local containers. It
requires Podman or Docker and fails if neither exists. It removes its containers
and temporary state. The root verifier includes these tests, Go vet, and native
race checks. Native tests need `PUBLISHER_BINARY` set to the built Buck artifact;
the adversarial check script supplies it automatically.

Tests exercise:

- The built CLI's submit, relay, work, inspect, failure, redrive, and metrics output.
- Lost enqueue and completion replies, with delivery and restart recovery.
- Two relay clients and two independent controller processes.
- Process loss after publication creation and after observation, before queue completion.
- New input during an active callback and a stale application observation write.
- Numeric priority, protected waits, transient retries, suspension, and redrive.
- Queue inspection and a host-owned Prometheus registry.
- Invalid identity and overflowing CLI priority.

The example uses dependencies from the library's existing `go.mod` and generated
Buck graph. It adds no dependency manifest. The build produces a Go executable;
CGo is disabled. Formatting and linting use the parent project's checks.
The schema is disposable example data, not a production migration sequence.
Use a fresh database or remove the three `prototype_*` tables when discarding it.
See [consumer validation](../../../../docs/design/consumer-validation.md) for the
measured results, interface conclusions, and remaining deployment checks.
