# Request intake

This project accepts external requests for the factory. GitHub and GitLab
adapters support account polling and signed webhooks.
The repository maintainer owns it. It is the first application consumer of the
reconciliation datastore interface.

The service owns provider discovery, source authorization, accepted snapshots, and
queue delivery. Its Go module and internal packages belong only to this service.
The factory is a collection of projects, not this application's module.
Workflow policy, execution, verification, and publication belong to other projects
when those capabilities have concrete implementations.

The current milestone accepts and queues requests, then adds an eyes reaction
to the source as an acknowledgement. It does not execute agents, create branches,
or open pull requests. The reaction means received, not execution started.
Jira and Slack can add adapters in this project.
Keep provider API clients, identity checks, and event parsing inside their adapters.
Adapters submit accepted requests through the service-owned persistence and queue
delivery operations. Provider-specific account state stays with its provider.
The acceptance and acknowledgement interfaces are provider-neutral. See
[polling and webhooks](transports.md) to run GitHub and GitLab. The GitHub polling
commands below retain their defaults. See [adding a provider](adding-a-provider.md) for
the adapter contract and implementation steps.

## Command syntax

Start an issue body or conversation comment with a command:

```text
@sir-robs-a-bot investigate this issue and propose a fix
```

The command must be the first non-whitespace text. Handles are case-insensitive.
Inline mentions, quoted lines, fenced code, empty instructions, and other handles
are not commands. The instruction can span multiple lines, up to 16 KiB.
The complete source body can contain at most 64 KiB.

Only explicitly allowlisted numeric GitHub user IDs can submit requests.
The service verifies the original author and current editor through GitHub.
Both must be allowlisted if another person edits a source. A GraphQL query
confirms its body and node identity still match the REST observation.
Missing editor identity or a concurrent body change fails closed.
Bot actors and the receiving account cannot submit requests.
The allowlist grants request submission only. It grants no execution,
credentials, publication, or repository write access.

## Build and verification

Run from the repository root:

```sh
export PATH="$PWD/.tools/bin:$PATH"
./buck2 build //services/intake/cmd:intake
./buck2 test //services/intake/...
./buck2 run toolchains//:gofmt -- -w services/intake/cmd services/intake/internal services/intake/tests
./buck2 run //tooling:fmt
./buck2 run //tooling:verify
```

The artifact is a native `intake` executable. Use `./buck2 build
//services/intake/cmd:intake --show-output` to find its path.
Buck builds provide compilation checks. The root verifier also runs Go vet,
race tests, dependency checks, and every discovered ingress fuzz target.
Integration tests require Podman or Docker and the pinned PostgreSQL and
DynamoDB Local images. They run the executable against local provider HTTP
fixtures, PostgreSQL, and the AWS SDK with DynamoDB Local and an S3 HTTP fixture.

Go 1.27.2 comes from the pinned toolchain. CGo is disabled for the artifact.
Native race verification requires a host C compiler. This module uses local
reconciliation and resource modules through Go `replace` directives.
`go.mod` owns dependencies. Generated Buck projections reuse matching packages
from those libraries and retain only additional SDK packages in this service.

Update dependencies with the pinned toolchain, then regenerate Buck metadata:

```sh
./buck2 run 'toolchains//:go[go]' -- -C services/intake mod tidy
bash services/intake/dependencies/generate.sh
```

Do not edit dependency versions in Buck files or format upstream vendor sources.

## Configuration

| Setting | Purpose |
| --- | --- |
| `GITHUB_INGRESS_TOKEN` | Explicit credential for the bot account |
| `INTAKE_STORAGE` / `-storage` | `aws` by default, or `postgres` for development and comparison |
| `INTAKE_DSN` | PostgreSQL connection string when selecting `postgres` |
| `-account` | Expected login, defaults to `sir-robs-a-bot` |
| `-allow-user-ids` | Required comma-separated numeric IDs for `probe`, `scan`, and `poll` |
| `-since` | Explicit activation time for `activate` and `probe`, in RFC3339 |
| `-queue-namespace` | Queue namespace, defaults to `factory` |
| `-api-url` | Defaults to `https://api.github.com`; HTTP loopback supports local tests |

Supply credentials through your secret manager or process environment.
Keep them outside the checkout. Do not put tokens in flags or shell history.
The service never reads `gh` credentials. It verifies the expected account with
`GET /user` before notification ingestion.

GitHub currently documents a classic PAT for the notifications API, with the
`notifications` or `repo` scope. GraphQL source provenance also needs repository
read access. Public repository queries with a classic PAT require `public_repo`.
Repository reads also need the access required
by the target repositories. Start with public repository testing and the least
privilege that satisfies those operations. GitHub App tokens cannot read the
account's notification inbox. See the
[notifications reference](https://docs.github.com/en/rest/activity/notifications).

## Probe real mention delivery

The probe uses REST GET requests and GraphQL queries. It never adds reactions. It performs no mutations,
changes no notifications, and stores no work.
Provide `GITHUB_INGRESS_TOKEN` for `sir-robs-a-bot` through your secret manager.
Find an allowed author's stable ID with `gh api users/YOUR_LOGIN --jq .id`.

1. Choose a disposable repository without the factory App installed.
2. Record a UTC time before the test, such as `date -u +%Y-%m-%dT%H:%M:%SZ`.
3. Post a new issue comment with the command syntax above, from the allowed account.
4. Run the probe with that timestamp and author ID:

```sh
./buck2 run //services/intake/cmd:intake -- \
  -allow-user-ids YOUR_NUMERIC_ID \
  -since YOUR_RFC3339_TIMESTAMP probe
```

A discovered command prints its stable request ID, repository, and author ID.
The summary prints thread and matching-source counts without source bodies.
A zero-match result does not prove that delivery works. Confirm the exact comment
was discovered. Test both a personally owned repository and an organization-owned
repository where the bot is not a member.

GitHub documents membership and read-access restrictions for mention
notifications. Native delivery outside installed repositories remains unverified
until this probe observes it for the intended account and repository types.
See [GitHub mention rules](https://docs.github.com/en/get-started/writing-on-github/getting-started-with-writing-and-formatting-on-github/basic-writing-and-formatting-syntax).

## Run durable ingestion

For AWS configuration, worker topology, and receipt recovery, follow the
[storage guide](storage.md). The commands below describe the PostgreSQL backend. Set `INTAKE_STORAGE=postgres`
when using those examples.

Provide `INTAKE_DSN` for a dedicated PostgreSQL database with TLS as required
by the deployment. Run migrations explicitly before starting pollers:

```sh
./buck2 run //services/intake/cmd:intake -- migrate
./buck2 run //services/intake/cmd:intake -- -since YOUR_RFC3339_TIMESTAMP activate
./buck2 run //services/intake/cmd:intake -- -allow-user-ids YOUR_NUMERIC_ID scan
./buck2 run //services/intake/cmd:intake -- -allow-user-ids YOUR_NUMERIC_ID poll
```

`activate` stores the account ID and intake start time. Repeating the same
activation is safe. A different start time fails instead of silently replaying
old instructions. Changing the floor requires an explicit data migration.
Sources created before activation are ignored, including old sources edited later.

`scan` performs one bounded sweep and exits with a nonzero status on failure.
`scan` and `poll` also deliver due acknowledgements after ingestion.
`poll` repeats sweeps and writes failures to stderr. It waits at least one minute
between sweeps, obeys GitHub's polling interval, and honors rate-limit retry times.
Each sweep has a two-minute deadline. Each API response has a 2 MiB size limit.
Each notification or comment listing has a 100-page limit. Hitting a limit fails
visibly; it does not advance a checkpoint.

Inspect the accepted data and deliver queue obligations:

```sh
./buck2 run //services/intake/cmd:intake -- list
./buck2 run //services/intake/cmd:intake -- inspect REQUEST_ID
./buck2 run //services/intake/cmd:intake -- relay
```

`list` returns up to 100 accepted request IDs for the authenticated account.
`inspect` reads the original snapshot from PostgreSQL and does not need a GitHub
token. Its output contains untrusted source text; treat it as data.
`relay` drains the outbox into the PostgreSQL queue under kind `factory-request`.
It does not start a worker. A deployment supervisor must run relay repeatedly
until a later host integrates continuous delivery.

## Acknowledgements

After durable acceptance, the service adds an eyes reaction to the source comment
or issue. Pull request bodies use issue reactions. It sends no reply comment.
The request transaction creates separate obligations for queue delivery and
acknowledgement. GitHub failures cannot roll back an accepted request or prevent
its queue delivery.

The acknowledgement worker uses the authenticated receiving account. It retries
failed reactions after at least one minute and honors longer API retry delays.
Rate limits stop the current delivery pass and persist a connection cooldown.
A restart or concurrent worker cannot bypass it. Receipt delivery is serial per
connection. Its lock permits new requests to be accepted during delivery.
Other source failures do not prevent
acknowledgement of accessible sources. The AWS backend persists call intent before delivery. After an interrupted call,
it performs a read-only check or stops that receipt as uncertain. See the
[receipt recovery protocol](storage.md#receipt-recovery).
Delivered obligations remain recorded. Duplicate ingestion does not recreate them.

Run a bounded delivery pass without rescanning:

```sh
./buck2 run //services/intake/cmd:intake -- acknowledge
```

This command needs both the database and bot credential. It exits nonzero on
failure and prints the successful acknowledgement count. A supervisor can retry
it independently of polling. Each pass has a one-minute deadline.
The bot credential needs permission to create reactions on the target repository.
See [GitHub reaction endpoints](https://docs.github.com/en/rest/reactions/reactions).
Rejected reactions retain attempts, a sanitized error, and the next retry time
in the acknowledgement table. Deleted sources and lost access need operator
attention; the accepted request remains available.

Inspect a receipt or redrive a stopped obligation after resolving its cause:

```sh
./buck2 run //services/intake/cmd:intake -- receipt REQUEST_ID
./buck2 run //services/intake/cmd:intake -- redrive-ack REQUEST_ID
```

Receipt states are `pending`, `delivered`, `terminal`, `uncertain`, and
`quarantined`. Adapters explicitly classify permanent and uncertain failures;
ordinary errors retry. Malformed stored snapshots are quarantined so later
receipts can proceed. Inspect and repair the cause before redrive. For uncertain
delivery, first establish whether the external effect occurred. Redrive preserves
attempt history and the connection cooldown. Completed receipts cannot be redriven.
Both commands require the expected account credential and scope access to that
connection. See [adding a provider](adding-a-provider.md) for receipt policy.

Run `migrate` before deployment. Completed reactions are not continuously
restored if a user later removes them.

## Ownership and recovery

The application owns four tables:

- `intake_connections` retains activation floors and receipt cooldowns per provider and receiving account.
- `intake_requests` stores immutable accepted snapshots and stable source identities.
- `intake_outbox` retains queue delivery obligations until acknowledged.
- `intake_acknowledgements` retains reaction delivery status and retry times.

`migrate` also installs the existing queue adapter's schema.
This initial application migration only creates its tables. Later schema changes
need explicit versioned migrations; `CREATE TABLE IF NOT EXISTS` does not upgrade
an existing table. This unreleased revision replaces the earlier GitHub-only
schema and request IDs. `migrate` rejects databases with `github_ingress_requests` or the earlier
JSONB snapshot layout. Accepted snapshots now use `bytea` to preserve their JSON
encoding, including numeric exponents and escaped NUL characters.
Use a fresh database for local development, or explicitly migrate records,
activation floors, pending queue keys, and delivery status before reuse. No
automatic legacy conversion is implemented.

A request ID hashes the provider, receiving account, scope, source kind, and
source ID. The tuple uses unambiguous JSON encoding. Each identity is an opaque
string; GitHub converts its numeric IDs in its adapter.
The first accepted observation wins. Source edits never replace a saved request
or create a second request. Submit a new comment to request new work.
Authorization uses the observed source author and current editor IDs. Once accepted, a
request remains stored if the allowlist changes; future execution must recheck
its own authorization.

Request creation and outbox insertion share one transaction. Duplicate observers
cannot create multiple requests or obligations. Queue delivery can repeat after
a lost reply. The relay removes an obligation only after acknowledged enqueue.
Application data stays separate from queue metadata.

## Discovery limits

Notifications are thread-level hints. Their reason can persist after unrelated
comments, and the latest-comment reference can omit earlier mentions.
Every sweep reads all participating notifications since activation, including
read notifications, then reads the issue body and all issue comments.
It does not mark notifications read or rely on their reason.

No durable moving pagination checkpoint exists in this milestone. A partial
sweep can persist some requests; restart rescans and deduplicates them.
Fixed-floor rescans avoid permanently trusting a mutable pagination offset.
They increase API reads as history grows and do not make GitHub notifications
a durable event log. Deleted comments, deleted notifications, access loss, or
activity that GitHub never delivers can still prevent discovery.
Inaccessible threads make the sweep fail, but do not prevent discovery in other
accessible threads. A candidate with invalid identity or unverifiable provenance
increments `FailedSources` and does not prevent later commands in its thread.
Storage failures stop that thread; cancellation and rate limits stop the sweep.
Rate limits stop further API calls until the required delay.
Stored requests remain recoverable.

Initial scope covers issue bodies, pull request bodies, and conversation comments.
Inline review comments, reviews, discussions, edited historical commands,
and installation-token management remain outside this milestone. Signed webhook
receivers now feed the same acceptance operation. See [transports](transports.md)
for the supported GitLab scope and transport limits.

No deployment manifests, readiness endpoint, or production supervision exists yet.
The tests establish local ingestion and recovery behavior, not live GitHub delivery
or a production deployment.

See the [provider prototype review](../../docs/reviews/2026-10-10-intake-providers.md)
for the original findings, fixes, and remaining provider limitations.

## Resource-backed persistence

The AWS backend runs acceptance, connection activation, independent factory
handoff, receipt recovery, and inspection on the shared resource library.
It uses S3 for immutable content and DynamoDB for state and queue scheduling.
The service is not deployed. Schema changes can require fresh development storage.
See the [storage guide](storage.md) for configuration and recovery procedures.
