# Intake storage and recovery

S3 and DynamoDB are the default backend. Both providers and both transports use
the same resource workflow. The earlier PostgreSQL backend remains available
with `-storage postgres` for development and comparison. Do not dual-write
acceptance through two backends.

This service is not deployed. Schema changes can break development data. Use
fresh storage when its layout changes; no compatibility or import layer exists.

## Ownership

S3 stores immutable request, connection, and receipt inputs by digest. DynamoDB
stores their fixed content references, versioned progress, and notification
obligations. The resource commit establishes acceptance. Failed uploads or failed
resource commits can leave unused objects. A committed request keeps its first
observation, including its body, metadata, and receipt policy.

The existing reconciliation queue stores logical keys and scheduling metadata.
It owns no request bodies or application progress. Intake has its own queue;
downstream factory reconcilers use a separate queue namespace. Downstream handlers
register `factory-request` and load the original input through the intake owner.
The CLI `inspect REQUEST_ID` exposes that input today. A cross-service authenticated
read API is not implemented.

A request resource records independent obligations to create its receipt, update
the request index, and enqueue factory work. Each dispatch pass attempts all
unfinished obligations. A failed receipt handoff cannot prevent factory handoff.
An uncertain enqueue can repeat. Consumers must persist progress and use stable
identities for subsequent effects.

Every resource notification has one destination, the intake queue. A relay only
wakes the owning handler. It does not coordinate a transaction across queues.
The request index is a reconstructible projection. `list` returns up to 100 IDs
in key order and can lag acceptance until dispatch runs. It is not an acceptance
authority or a complete export operation.

## Receipt recovery

An asynchronous request commits receipt intent with acceptance. Dispatch creates
a separate receipt resource. A due receipt conditionally attaches one attempt to
its connection. Other receipts remain scheduled in the existing queue.

Before starting a ready attempt, the connection handler checks the captured
receipt version. A concurrent attachment can read a pending receipt before an
earlier attempt delivers it and clears the connection. The handler clears that
stale attachment without another provider call.

The connection handler persists `sending` before calling the provider. It then
persists the outcome and any connection cooldown in one conditional update. It
publishes that outcome to the receipt before clearing the connection attempt.
An attempt token makes interrupted outcome publication repeatable.

On restart in `sending`, the handler performs only a read-only observation.
GitHub and GitLab observers look for this receiving account's eyes reaction.
A positive observation proves delivery. A negative or failed observation marks
the receipt `uncertain`; it never authorizes another POST. The handler then frees
the connection for later receipts. A known provider cooldown remains authoritative
across restart, queue wakeups, and redrive.

Receipt states include `pending`, `delivered`, `terminal`, `uncertain`, and
`quarantined`. Inspection can also show active `ready` and `sending` phases.
Confirmed invalid or missing request content quarantines its receipt. Temporary
storage failures retry. Queue failures have a separate bounded retry budget.

This protocol cannot fence an HTTP call from a paused former worker. Before
redriving an uncertain receipt, stop former workers and establish whether the
reaction exists. There is no exactly-once external execution guarantee. Completed
reactions are not restored if a user later removes them.

## AWS configuration

Provision a DynamoDB table with string partition key `pk` and string sort key
`sk`. No secondary index, TTL, stream, or table scan is required. Provision a
private S3 bucket. Use the deployment's encryption, backup, and retention policies.
The runtime does not create infrastructure. Retain referenced objects for at
least as long as their resource records. Do not expire objects merely because a
queue item completed. Unreferenced object collection is not implemented.

| Setting | Purpose |
| --- | --- |
| `INTAKE_STORAGE=aws` | Select the AWS backend |
| `AWS_REGION` | Region for SDK clients |
| Standard AWS SDK credentials | Prefer workload identity; keep credentials outside the checkout |
| `INTAKE_TABLE` | Existing table for resources, queues, and index |
| `INTAKE_BUCKET` | Existing content bucket |
| `INTAKE_CONTENT_PREFIX` | Optional owner-specific S3 key prefix |
| `-queue-namespace` | Factory queue namespace, defaults to `factory` |
| `INTAKE_DYNAMODB_ENDPOINT` | Optional trusted endpoint override |
| `INTAKE_S3_ENDPOINT` | Optional trusted endpoint override |

For namespace `factory`, intake uses resource namespace `factory-intake-resources`
and owner queue namespace `factory-intake-work`. Factory work uses `factory`.
Keep the table, bucket, prefix, and namespaces identical across collaborating
processes. Isolate deployments with separate IAM policies and storage. Namespaces
alone do not enforce authorization. Endpoint overrides require HTTPS or numeric
HTTP loopback. Supply only trusted endpoints because SDK clients sign requests.

The role needs DynamoDB `GetItem`, `PutItem`, `UpdateItem`, `DeleteItem`, `Query`,
and `TransactWriteItems` on the table. It needs S3 `GetObject` and `PutObject` on
the configured prefix. Add KMS permissions if the deployment requires them.
Bucket listing, deletion, and table provisioning are not runtime operations.
Validate the final IAM and KMS policy in the deployment account.

Provision storage externally, then activate each receiving account:

```sh
./buck2 run //services/intake/cmd:intake -- -since TIMESTAMP activate
./buck2 run //services/intake/cmd:intake -- -provider gitlab -since TIMESTAMP activate
```

The `migrate` command applies only to the PostgreSQL backend. AWS storage has no
application migration command or compatibility gate.

Run `worker` continuously under a supervisor. It relays notifications and
reconciles intake dispatch without provider credentials:

```sh
./buck2 run //services/intake/cmd:intake -- -storage aws worker
```

Run a provider `poll` or `serve` process with that provider's explicit bot token
and allowlist. These processes also advance receipts. A supervised `acknowledge`
command can advance receipts separately. `relay` performs a bounded notification
pass only; it does not replace `worker`. Run the downstream factory controller
separately against its queue namespace. Intake does not execute agent workloads.

Monitor reconciliation failures, queue failure budgets, stopped receipts, and
resource notification delivery. Supervise restarts on storage failures. There is
no readiness HTTP endpoint or deployment manifest yet.

## Inspection and redrive

```sh
./buck2 run //services/intake/cmd:intake -- -storage aws inspect REQUEST_ID
./buck2 run //services/intake/cmd:intake -- -storage aws receipt REQUEST_ID
./buck2 run //services/intake/cmd:intake -- -storage aws redrive-ack REQUEST_ID
./buck2 run //services/intake/cmd:intake -- -storage aws redrive-work KIND ID
```

`inspect` and `redrive-work` require storage credentials only. `receipt` and
`redrive-ack` authenticate the provider account and restrict access to its
connection. Fix the cause before redrive. `redrive-ack` changes only a stopped
receipt to pending and resets its attachment queue budget. It preserves attempts
and connection cooldowns. If a queue reset fails after the resource update,
`redrive-work factory-request-receipt REQUEST_ID` resumes scheduling.

`redrive-work` resets an existing, unowned intake queue key. It does not alter
application progress or make a delivered receipt eligible again. Connection
handler kinds have the form `intake-effect-<digest>`; use the kind and resource ID
from reconciliation logs when recovering an exhausted connection handler. Factory
queue budgets belong to the downstream controller and are outside this command.

## Evidence and limits

Tests run the real CLI for both providers against DynamoDB Local and S3 SDK HTTP
fixtures. Recovery tests interrupt calls and state write replies, reopen durable
storage, preserve cooldowns, and prove another receipt can proceed. Shared adapter
contracts cover conditional writes and queue leases separately.
These checks do not prove live AWS IAM, S3 service behavior, provider mention
delivery, or production deployment setup.

## Live AWS checks

Live checks require explicit opt-in. Use a disposable DynamoDB table and private
S3 bucket in one Region. Both names must start with `factory-live-`. The table
needs string keys `pk` and `sk`, with on-demand billing and no secondary index.
Use fresh resources with no versioning, object lock, or other application data.
Provision and delete these resources outside the application. Normal CI uses
local fixtures and does not provision AWS resources.

Set `AWS_REGION`, `INTAKE_LIVE_TABLE`, and `INTAKE_LIVE_BUCKET`. Authenticate
through the standard SDK credential chain, including AWS CLI login. Then run:

```sh
export PATH="$PWD/.tools/bin:$PATH"
outputs=$(./buck2 build //services/intake/cmd:intake --show-full-json-output)
export INTAKE_BINARY=$(printf '%s' "$outputs" | ./tooling/bin/jq -er 'values | .[]')
INTAKE_LIVE_AWS=1 ./buck2 run 'toolchains//:go[go]' -- -C services/intake \
  test -mod=mod -v -count=1 -timeout=10m ./tests -run '^TestAWS'
```

These tests write real S3 objects and DynamoDB records. They cover the shared
resource contract, conditional S3 collisions, missing objects, duplicate intake,
both provider CLI paths, interrupted receipts, and worker restart. Provider API
calls use HTTP fixtures. Podman or Docker remains required for the local
PostgreSQL fixture used by the recovery test.

Always clean up, including after a failed test. Delete all objects in the test
bucket, delete that bucket, then delete the test table. Use only the exact names
created for this run. Confirm `head-bucket` returns 404 and `describe-table`
returns `ResourceNotFoundException`. Retain logs outside the checkout.

A live run with an admin role proves AWS service behavior under that role. It
does not prove the deployment's least-privilege IAM or KMS policies, failover,
live provider delivery, or unattended operation.
