# Accept GitHub account mentions through notification polling

Status: accepted for the initial entrypoint.

The operator owns the `sir-robs-a-bot` GitHub account and wants a native mention
entrypoint in repositories without a factory App installation.
Use the account notification inbox for initial discovery. Preserve a separate
GitHub App integration path for installed repositories.

## Alternatives

| Design | Caller and authority | Recovery and limits |
| --- | --- | --- |
| App webhook | A comment in an installed repository supplies an instruction; installation permissions govern App actions | Signed event delivery and redelivery support ingestion; no events from repositories outside the installation |
| Account notification polling | A native account mention produces a notification when GitHub's delivery rules permit it; account access governs discovery | Thread hints require source reads and deduplication; polling needs a classic PAT and cannot guarantee universal mention delivery |

Select polling for this milestone because installation-free discovery is the
requested behavior. A shared application acceptance operation owns immutable
requests and queue delivery obligations. Future webhooks can call that operation.
Provider integrations do not change the reconciliation library.

Only explicit allowlisted numeric user IDs can submit requests.
REST reports the original author after another user edits a source. A GraphQL
provenance query checks the current body, node identity, author, and editor.
Both the original author and current editor must satisfy the allowlist.
Unavailable provenance or concurrent body changes fail closed.
The first non-whitespace text must address the receiving account and contain an
instruction. Authorization to submit does not authorize execution or publication.

Save the first accepted source snapshot and outbox record in one PostgreSQL
transaction. Identify it by provider, receiving account, repository scope, source type, and
source ID. Later edits and repeated observations cannot replace the request.
A lost queue acknowledgement leaves an obligation for duplicate-safe delivery.

Start from an explicit, durable activation floor. Rescan read and unread
participating notifications from that floor. Read issue bodies and all comments,
rather than trusting notification reasons or the latest-comment reference.
Avoid a durable offset checkpoint over GitHub's mutable paginated lists.
This trades API reads for conservative replay, not guaranteed event delivery.
Page, response, and sweep limits fail visibly.

The executable integration tests prove local parsing, allowlist enforcement,
immutable snapshots, transactional acceptance, duplicate queue delivery, and
process crash recovery. They use a local HTTP fixture and PostgreSQL.
They do not prove GitHub emits mentions for the real account in external
repositories. The read-only probe in the [service guide](../../services/intake/README.md)
must establish that assumption before deployment relies on it.

For notifications and credentials, see the
[GitHub API reference](https://docs.github.com/en/rest/activity/notifications).
GitHub provides no personal-mention webhook, as described in
[webhook types](https://docs.github.com/en/webhooks/types-of-webhooks).
Organization membership can affect mention delivery; see
[mention rules](https://docs.github.com/en/get-started/writing-on-github/getting-started-with-writing-and-formatting-on-github/basic-writing-and-formatting-syntax).

## Receipt acknowledgement

Accepted requests create an eyes reaction obligation in the same transaction as
request persistence and queue delivery. A separate sender adds the reaction to
the source issue or comment. Polling and the explicit `acknowledge` command drain
due obligations. Probe remains read-only.

The reaction means received. It does not promise execution. Queue delivery does
not depend on reaction delivery. GitHub returns an existing reaction when the
same account repeats the same content, which permits recovery after lost replies.
Keep delivered status so a rescan cannot recreate completed obligations.

The provider-neutral boundary and unreleased schema change are recorded in
[ADR 0010](0010-provider-neutral-intake.md).
