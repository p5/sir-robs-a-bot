# Polling and webhooks

Both GitHub and GitLab support account polling and signed webhook delivery.
Run either transport, or run both against the same storage configuration. Each process hosts
one provider and account. Both transports derive the same logical source ID.
Duplicate observations preserve the first accepted snapshot, queue obligation,
and receipt obligation. No schema change is needed for these adapters.

## Configure the receiving accounts

Keep credentials in your secret manager or process environment:

| Variable | Purpose |
| --- | --- |
| `INTAKE_STORAGE` | `aws` by default, or `postgres`; see the [storage guide](storage.md) |
| `INTAKE_DSN` | Shared PostgreSQL database when selecting `postgres` |
| `GITHUB_INGRESS_TOKEN` | Explicit bot account credential for GitHub API reads and eyes reactions |
| `GITHUB_WEBHOOK_SECRET` | GitHub webhook HMAC secret, at least 32 bytes |
| `GITLAB_INGRESS_TOKEN` | Explicit GitLab bot account access token with `api` scope for polling and reactions |
| `GITLAB_WEBHOOK_SIGNING_TOKEN` | GitLab signing token in `whsec_<base64>` format with a 32-byte key |

`-provider` defaults to `github`. Use `-provider gitlab` for GitLab.
`-api-url` defaults to the provider's public origin. For GitLab Self-Managed,
supply its HTTPS origin without a path or trailing slash. Instances hosted below
a URL path are not supported yet. HTTP loopback is available for local tests.
Never configure credentials through command arguments.

Use `-account` to set the expected bot username, default `sir-robs-a-bot`.
Each process verifies its API token with the provider's current-user endpoint.
Use `-allow-user-ids` to grant submission permission to specific numeric user IDs.
IDs belong to that provider instance. They grant no execution or publication rights.
The GitLab connection includes the configured instance origin and numeric bot ID.
It cannot collide with the same IDs on another instance.

Provision AWS storage as described in the [storage guide](storage.md), then
activate each account with an explicit intake floor:

```sh
./buck2 run //services/intake/cmd:intake -- -since YOUR_RFC3339_TIMESTAMP activate
./buck2 run //services/intake/cmd:intake -- \
  -provider gitlab -since YOUR_RFC3339_TIMESTAMP activate
```

For a self-managed GitLab instance, include `-api-url https://gitlab.example.com`
in every account command. Keep that origin consistent across processes.

## Poll account mentions

```sh
./buck2 run //services/intake/cmd:intake -- -allow-user-ids YOUR_GITHUB_ID poll
./buck2 run //services/intake/cmd:intake -- \
  -provider gitlab -allow-user-ids YOUR_GITLAB_ID poll
```

`scan` runs one sweep. `probe` uses API reads only and needs `-since` instead of a
datastore. A probe prints stable identities without source bodies. It creates no
requests or reactions. `poll` retries after at least one minute and respects
longer API retry delays. Run the AWS `worker` separately to relay notifications and dispatch factory work.

GitHub reads notification thread hints and verifies current author/editor
provenance through GraphQL. GitLab reads pending and completed to-dos, then reads
all notes on each referenced issue or merge request. It does not mark to-dos done.
The poller never uses payload URLs or pagination links as credential destinations.
It constructs API paths from numeric IDs. Responses have a 2 MiB bound and each
listing has a 100-page bound. A sweep has a two-minute deadline.

GitLab currently accepts issue and merge-request comments. It does not accept
issue or merge-request descriptions. REST note observations do not establish who
edited the text, so polling accepts only notes with equal creation and update
times. Edited notes are ignored. Submit a new comment to request work.
The webhook also verifies the current note through the API and requires unedited
text. Signatures alone cannot establish authorship because project maintainers
can customize hook payloads. Updates never replace an accepted snapshot.

Both inboxes are discovery hints, not durable event logs. Deleted notifications,
deleted to-dos, deleted comments, missing access, and provider delivery rules can
prevent discovery. Fixed-floor rescans increase API cost as history grows.
Live mention delivery must be proved with the intended accounts and repositories.

## Receive webhooks

Set the provider's webhook secret in the environment, then start its listener:

```sh
./buck2 run //services/intake/cmd:intake -- \
  -allow-user-ids YOUR_GITHUB_ID -listen 127.0.0.1:8080 serve
./buck2 run //services/intake/cmd:intake -- \
  -provider gitlab -allow-user-ids YOUR_GITLAB_ID -listen 127.0.0.1:8081 serve
```

Expose each listener through your HTTPS reverse proxy. Configure JSON webhooks
at its root URL. No hook registration, public endpoint, TLS certificate, or
provider credentials are created by these commands.

For GitHub repository, organization, or App hooks, enable issue comments, issues,
and pull requests. The adapter accepts comment `created` events and body `opened`
events. It verifies `X-Hub-Signature-256`, the sender and original author, and
current GraphQL provenance. It ignores edits and unrelated events. The API
credential remains the explicit bot account token. This does not implement App
installation-token generation or installation discovery.
See [GitHub signature validation](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries).
GitHub does not automatically redeliver failed webhooks. Monitor delivery failures
and redeliver them, or keep polling enabled for recovery. See
[failed webhook deliveries](https://docs.github.com/en/webhooks/using-webhooks/handling-failed-webhook-deliveries).

For GitLab project or group hooks, enable comment events and configure a signing
token. Use GitLab 19.1 or newer with Standard Webhooks support. Configure the same
signing token in `GITLAB_WEBHOOK_SIGNING_TOKEN`. Legacy `X-Gitlab-Token`
authentication is not accepted. The adapter verifies HMAC-SHA256 over the message
ID, timestamp, and raw body, with a five-minute freshness window. It accepts
creation events for issue and merge-request notes. The event user must match the
note author and be allowlisted. The API must confirm the note ID, author, body,
creation time, and absence of edits. Project and target IDs must agree within the
signed event. The receiver never trusts an instance header to choose its account.
See [GitLab signing tokens](https://docs.gitlab.com/user/project/integrations/webhooks/)
and [comment events](https://docs.gitlab.com/user/project/integrations/webhook_events/#comment-events).

Listeners bound request bodies to 2 MiB and headers to 16 KiB. They reject unsigned
requests, oversized bodies, and methods other than POST. A successful response
follows durable acceptance, or deliberate ignoring of an unsupported or
unauthorized event. Acceptance failure returns a non-success response. The
receiver never acknowledges an uncertain database commit as successful.
GitHub signatures have no timestamp; activation floors, provenance checks, and
source deduplication constrain replay. GitLab freshness checks require a receiver
clock synchronized with the sending instance.

Each listener delivers due receipts independently, at most a minute between
passes. No provider API write runs in a webhook request. SIGTERM stops the
listener and its worker. Polling and webhook receipt workers share durable
connection cooldowns and durable attempt state. GitLab uses REST add/list operations for
eyes reactions, never a toggle. A lost POST response triggers a lookup; if that
lookup cannot prove delivery, the receipt becomes `uncertain` and stops automatic
retries. Inspect the source before redrive.

GitLab validates duplicate awards at the application level. Its current schema
has no unique constraint for the user, emoji, and target tuple. Lookup handles
ordinary repeats and completed lost replies, but cannot prove atomic uniqueness
against overlapping requests outside this service or a crash before recording an
uncertain outcome. Do not infer exactly-once external effects from these tests.
See GitLab's [award model](https://github.com/gitlabhq/gitlabhq/blob/master/app/models/award_emoji.rb)
and [add operation](https://github.com/gitlabhq/gitlabhq/blob/master/app/services/award_emojis/add_service.rb).

## Inspect and deliver accepted work

These commands work with either `-provider github` or `-provider gitlab`:

```sh
./buck2 run //services/intake/cmd:intake -- -provider gitlab list
./buck2 run //services/intake/cmd:intake -- -provider gitlab inspect REQUEST_ID
./buck2 run //services/intake/cmd:intake -- -provider gitlab receipt REQUEST_ID
./buck2 run //services/intake/cmd:intake -- -provider gitlab redrive-ack REQUEST_ID
./buck2 run //services/intake/cmd:intake -- -provider gitlab acknowledge
./buck2 run //services/intake/cmd:intake -- relay
```

`inspect` and `relay` operate on application data without provider credentials.
Relay is shared across providers. Its queue namespace defaults to `factory` and
can be set with `-queue-namespace`. For AWS, supervise `worker` continuously;
`relay` alone does not advance application dispatch.
The acknowledgement confirms receipt only; this service still does not run agents.
