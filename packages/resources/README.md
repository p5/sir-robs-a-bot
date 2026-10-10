# Resources

This Go library stores immutable resource inputs, versioned application state,
and durable reconciliation notifications. The repository maintainers own it.
The request and run owners in the [consumer prototype](examples/request-run/README.md)
are its first users. Intake uses S3 and DynamoDB by default. See the
[intake storage guide](../../services/intake/storage.md).

The reconciliation library owns queue coordination. This library owns persistence
operations that consuming applications can use. It defines no workflow, provider,
authorization policy, network server, or process supervisor.

## Use the interface

An owner supplies a content adapter and a resource datastore adapter:

```go
objects, err := s3.New(s3Client, "factory-content", "request-owner")
// Handle err.
state, err := dynamodb.New(dynamoClient, "factory-resources", "request-owner")
// Handle err.
repository, err := resources.New(objects, state)
// Handle err.
record, created, err := repository.Create(ctx,
    datastore.Key{Kind: "request", ID: requestID},
    acceptedJSON,
    jsontext.Value(`{"phase":"accepted"}`),
)
// Handle err. A duplicate returns the first committed record.
```

The example uses the reconciliation datastore's `Key`. The resources datastore
package defines `Record` and its adapter interface. Use import aliases when both
packages are needed.

`Create` validates identifiers and state, uploads content, then atomically commits
its reference, initial state, and a notification. Its successful database commit
is the acceptance point. An upload alone does not accept work. Concurrent creators
preserve the first committed record. Duplicate creation never replaces its input
or recreates a delivered notification.

`Get(ctx, key)` returns the record and verified input bytes. It checks SHA-256 and
length before returning content. `Update(ctx, key, expectedVersion, state)` changes
application state and creates a notification in the same transaction. The original
content reference cannot change. A stale update returns `datastore.ErrConflict`.
After an unknown write outcome, reread state and apply the owning workflow's
idempotency rules. Do not blindly increment a version and repeat an effect.

The owner chooses when an application operation returns success. Provider-specific
receipts and other external effects remain separate durable workflow state. This
library does not make such effects exactly once.

## Deliver work

Run `repository.DeliverOne(ctx, queueStore)` repeatedly in a supervised relay.
The relay enqueues only the logical resource key. It removes the notification
after acknowledged enqueue. Failed enqueue or an unknown result leaves work
recoverable. The notification includes a resource version; an old relay cannot
remove a concurrent update's newer notification. Concurrent relays may enqueue
repeatedly. The queue coalesces keys, and reconcilers must tolerate repeat calls.

Keep the target queue namespace consistent across the owner's relay processes.
Owners sharing a queue must allocate distinct reconciler kinds. If the same kind
means different workflows in different owners, use separate queue namespaces.
A resource datastore namespace does not scope its key inside the target queue.
An obligation has one destination. Broadcasting to several owners requires
separate, explicitly persisted obligations; one queue is not an event bus.
Supervise both the relay and the queue worker. Monitor failures and suspended
queue items. The library starts neither process and does not silently redrive
exhausted work.

Reconcilers load current resource state and make repeatable progress. Persist
progress before returning. Domain operations, stable effect identities, and
conditional state updates protect application work. Queue leases alone do not
fence application writes or external effects.

## Storage and isolation

`content.Store` implements immutable put and bounded get. `content.Repository`
provides digest addressing and end-to-end integrity checks. A content reference
contains SHA-256 and byte length, with no physical endpoint or credentials.
References resolve within the owner's configured content store. Cross-owner
references require the owning module's authorized read interface; they are not
universal URLs.

The S3 adapter uses the official AWS SDK, conditional `PutObject` with
`If-None-Match: *`, and an upload checksum. It verifies existing bytes after a
conditional collision. Hosts supply SDK clients, credential providers, HTTP
budgets, endpoint configuration, and retries. Use workload identity in deployment;
the library does not discover credentials. S3-compatible providers must pass the
same semantics before deployment. R2 compatibility has not been tested.

The DynamoDB adapter assumes one authoritative AWS Region for each resource
namespace. Cross-Region operation needs separate consistency validation. It uses
a table with string `pk` and `sk` keys. It requires
`GetItem`, `TransactWriteItems`, `Query`, and `DeleteItem` permissions. Provision
the table and bucket explicitly. Each owner uses a distinct namespace and bucket
prefix. Namespaces are logical partitions, not authorization controls. Enforce
access isolation with the deployment's IAM and owning module interfaces.

Resource writes atomically update a dedicated outbox entry. Discovery queries
16 fixed outbox partitions with strong consistency and rotates the starting
partition. No resource scan, GSI, stream, or S3 notification is required for
correctness. The shard layout is persistent schema version 1. Changing it needs
migration. A pass can issue up to 16 queries when partitions are empty. Malformed
stored records fail visibly and require operator repair.

Memory adapters preserve operation semantics within one instance. They are
explicitly ephemeral and cannot prove distributed ownership or crash durability.
A PostgreSQL resource adapter and other deployment adapters are not implemented.
The existing PostgreSQL queue adapter is a separate capability.

Snapshot inputs have a 16 MiB limit. State has a 32 KiB JSON limit. Kinds have a
128-byte limit and IDs have a 512-byte limit in addition to portable queue rules.
Versions range from 1 through the signed 64-bit maximum. These are explicit
contract limits. Large streaming artifacts need a separate interface; do not
buffer unbounded files through this one.

## Recovery and retention

An interruption after upload but before resource commit can leave an unreferenced
object. Retry can accept a different observation because the resource record
chooses the winner. Do not delete an upload immediately after an uncertain commit.
Another attempt may reference the same content.

Objects and references must remain available for the resource's retention period.
The library offers no object deletion or garbage collector. Do not apply an S3
expiration policy to live referenced content. A future collector must account for
in-flight uploads and unknown commits before deleting unreferenced objects.
Configure encryption, retention, deletion access, backups, and recovery with the
deployment. Deny object replacement and deletion to ordinary consumers where
possible. Integrity checks detect corruption; they cannot restore missing bytes.

## Build and verify

Use the pinned Go 1.27.2 toolchain from the repository root:

```sh
export PATH="$PWD/.tools/bin:$PATH"
./buck2 build //packages/resources/...
./buck2 test //packages/resources/...
./buck2 run toolchains//:gofmt -- -w packages/resources/content packages/resources/datastore packages/resources/examples packages/resources/tests packages/resources/repository.go
bash packages/resources/dependencies/check.sh
bash packages/resources/checks/verify.sh
./buck2 run //tooling:verify
```

The root verifier includes module drift, vet, race, integration, and discovered
fuzz targets. Integration tests require Podman or Docker and the existing pinned
DynamoDB Local image. They exercise the actual S3 SDK against a signed HTTP
storage fixture. This proves client handling and recovery contracts, not live S3
behavior, IAM, AWS failover, or compatibility with another object provider.
The executable prototype proves the public resource and reconciler interfaces.
There is no deployment or workload execution in this project.

`go.mod` and `go.sum` own dependencies. Run
`bash packages/resources/dependencies/generate.sh` after dependency changes.
The pinned bundled gobuckify generates Buck targets. The projection reuses
identical packages from the reconciliation module's vendor tree and retains only
additional sources here. Shared packages must select identical module versions.
No dependency is added to the queue module solely for this library. Do not edit
generated sources or Buck files by hand. Native Go checks use `-mod=mod` because
the projected vendor tree intentionally omits shared packages and local modules.
Dependency regeneration requires Python 3 in addition to repository bootstrap
tools. Buck artifacts include Go archives and the prototype executable. This
project supports the same Linux/macOS and amd64/arm64 toolchains as the core.

## Add an adapter

Implement `content.Store` or `datastore.Store` in the corresponding backend
directory. Adapter selection belongs to the host, with no central driver registry.
Adding a backend must not require changes to the repository operations.
Keep SDK types and provisioning details inside its adapter.

Run `datastore/storetest.Run` with isolated fixtures and a reopen function for a
state adapter. Reopen must create an independent client for durable backends.
Add backend-specific tests for lost responses, malformed records, namespace
isolation, and bounded queries. Content adapters must prove conditional collisions,
missing objects, corruption, bounded reads, and response-body cleanup. Test
repository ordering through `Create`, `Get`, `Update`, and `DeliverOne`.
