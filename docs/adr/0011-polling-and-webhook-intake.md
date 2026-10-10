# Accept provider requests through polling and webhooks

Status: accepted.

The operator wants both account-level mention discovery and repository webhook
entrypoints for GitHub and GitLab. Polling reaches account-visible activity where
no hook is configured. Hooks provide signed creation events for configured
repositories or groups. Neither transport replaces the other.

We considered separate application state for each transport and common acceptance
with provider-owned discovery. Choose common acceptance. Both transports use the
same connection, scope, source kind, and source ID. Transport delivery IDs never
become request identities. The first accepted snapshot and receipt policy win.
No core schema or datastore interface change is needed.

Keep clients, signatures, source parsing, and authorization inside each provider.
The executable hosts one provider/account per process and offers `poll` and
`serve` commands. Separate processes can share PostgreSQL. Each process supplies
explicit credentials and an author allowlist. Receipts and queue relay remain
independent of webhook HTTP responses.

GitHub verifies SHA256 webhook signatures and current GraphQL provenance.
GitLab requires timestamped Standard Webhooks signatures. GitLab polling reads
pending and completed to-do thread hints and fetches notes through constructed
numeric API routes. Both GitLab transports accept only unedited comments because REST observations
do not prove the responsible editor. Webhooks also verify the current API note
against the signed event. Project maintainers can customize hook payloads, so
signatures alone cannot establish the claimed actor and text. Issue and merge-request descriptions
are outside the initial GitLab scope.

Local API fixtures and PostgreSQL tests prove overlapping transport deduplication,
queue delivery, eyes receipts, and the executable listener lifecycle. Parser and
signature fuzz targets run through the normal project verifier. These tests do
not prove native mention delivery or a deployed public endpoint.

GitLab receipt delivery uses add and lookup operations. It never toggles a
reaction. An unconfirmed POST with no observed award stops in `uncertain` state.
GitLab's application-level duplicate validation does not establish atomic
uniqueness for all overlapping requests. Record that provider limit explicitly;
operator recovery must verify the source before redrive. The factory does not
claim exactly-once external effects.

See the [transport guide](../../services/intake/transports.md) for configuration,
provider references, operational limits, and recovery steps.
