# Keep intake acceptance independent of providers

Status: accepted.

Intake will accept requests from GitHub and later from systems such as Jira,
Slack, and GitLab. Their discovery mechanisms and identities differ. GitHub's
numeric repository and comment IDs must not define the common persistence API.

We considered a common source runner with discovery, authorization, acceptance,
and acknowledgement methods, and separate acceptance and acknowledgement
contracts. Choose the smaller contracts. A webhook should not implement a polling
lifecycle. An adapter should not own the transaction between durable acceptance
and queue delivery.

`Acceptor.Accept` records an already-authorized submission and creates queue and explicitly selected
asynchronous acknowledgement obligations atomically. Identity consists of opaque provider,
receiving account, scope, source kind, and source ID strings. Hash their JSON tuple
to produce an unambiguous stable request ID. Keep immutable actor, instruction,
observation, and bounded provider-owned metadata in the accepted snapshot.

Adapters own API clients, parsing, authorization, source provenance, discovery
state, and receipt delivery. `Acknowledger` binds an authenticated connection to
an idempotent receipt operation. `DeliveryError` carries retry timing and whether
to persist a connection cooldown or stop automatic retries. Intake owns selection, concurrency exclusion, durable retry
state, and completed receipt records. It imports no provider package.

The CLI hosts GitHub only for now. A second fixture adapter proves that opaque
workspace and message identities need no core or schema changes. It exercises
acceptance, duplicate observations, queue delivery, receipt retry, and provider
and account isolation through PostgreSQL. It does not implement a real Slack or
Jira integration.

This unreleased change replaces the earlier GitHub-only tables and request IDs.
`migrate` refuses the legacy schema. Existing durable data and queue keys require
an explicit migration before reuse. Fresh development databases use the neutral
schema. We retain schema version 1; we do not promise compatibility for the
unreleased layout.

The provider review exposed JSONB numeric rewriting and mandatory receipt gaps.
Store encoded snapshots in `bytea`; the application never queries their JSON
fields. Require an explicit receipt mode for asynchronous, inline, or omitted
receipts. Keep terminal, uncertain, and quarantined obligations for inspection
and scoped operator redrive. Do not infer provider idempotency from retry state.
Connection row locks serialize receipt delivery and persist account cooldowns.
Use locks that allow foreign-key checks for new acceptance during delivery.
