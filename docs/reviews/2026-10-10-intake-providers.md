# Intake adversarial review and provider prototypes

Reviewed on 2026-10-10. Scope includes the current uncommitted intake service,
its GitHub adapter, durable acceptance, queue relay, and acknowledgement recovery.
This was a single-agent review. No independent reviewer or live provider account
was used. Production behavior was not changed as part of this review.

## Findings to act on

### P1: accepted metadata can become unreadable and block receipt delivery

`Submission.Validate` accepts bounded JSON. PostgreSQL stores snapshots as
`jsonb`, which can change numeric representation. For example, metadata
`{"n":1e20000}` passes validation and commits successfully. PostgreSQL expands
that number to more than 20,000 digits. `Request.Validate` compacts whitespace,
but the expanded number still exceeds the metadata limit. `Get` then rejects
an accepted request as an identity mismatch.

An acknowledgement pass selects that same row, fails snapshot validation, rolls
back, and exits without moving or quarantining the obligation. Every subsequent
pass selects it again. Other receipts for the connection remain blocked. Queue
delivery can still enqueue the unreadable request.

Evidence: `TestReviewAcceptedMetadataRemainsReadable` and
`TestReviewUnreadableSnapshotDoesNotBlockOtherReceipts` fail against PostgreSQL.
See [validation](../../services/intake/internal/intake/request.go),
[storage](../../services/intake/internal/intake/postgres.go), and
[receipt selection](../../services/intake/internal/intake/acknowledgements.go).

Preserve the validated snapshot representation, for example as JSON text or bytes,
or validate the database representation before committing. Also isolate and
quarantine invalid receipt snapshots. Add an acceptance invariant test that every
successful acceptance can be retrieved and acknowledged after a database reopen.
Include exponent-form numbers and database-specific JSON edge cases in its corpus.

### P1: a rejected source can starve later commands in its GitHub thread

The poller returns a source provenance error to `Conversation`. The comment loop
then stops visiting the remaining comments. A source whose editor cannot be
identified as a human can fail permanently. Since every sweep rereads the same
thread from the start, later valid commands never reach acceptance.

Evidence: `TestReviewOneBadGitHubCommentDoesNotBlockLaterCommands` supplies two
commands. The first has an unidentifiable editor; the second has valid provenance.
Neither of two sweeps accepts the second command. Existing tests isolate failure
between threads, but do not cover failure between sources in one thread.

See [the callback](../../services/intake/internal/providers/github/poller.go) and
[the comment loop](../../services/intake/internal/providers/github/client.go).
Record source-local failures and continue visiting other sources. Still stop for
account-wide rate limits and cancellation. Surface the failed-source count and
aggregate error so continued discovery does not hide a failure.

### P2: connection-wide retry delays do not survive another delivery pass

`DeliveryError.Stop` stops only the current loop. `RetryAfter` updates only the
failed obligation. Other due obligations for the same connection remain ready.
A second worker, a process restart, or another `acknowledge` command immediately
makes another API call despite the account-wide cooldown.

Evidence: `TestReviewAccountRateLimitSurvivesAnotherPass` accepts two requests,
returns a one-hour connection-wide retry delay, then starts a second pass. The
sender is called twice immediately.

See [retry persistence](../../services/intake/internal/intake/acknowledgements.go).
Persist connection-level acknowledgement cooldowns and check them when selecting
work. Test both separate processes and concurrent senders. Distinguish request
retry timing from connection retry timing in the provider error contract.

## Interface gaps to address

Every acceptance creates an asynchronous acknowledgement obligation. A direct
HTTP caller can already receive a durable request ID in its `202` response and
have no callback destination. The HTTP prototype leaves an unsendable obligation.
Add an explicit receipt policy for asynchronous, inline, or disabled receipts.
Keep GitHub's eyes reaction enabled. Decide this in trusted adapter configuration
or authorized submission construction, not from unauthenticated input.

The delivery contract requires an idempotent operation. This fits reactions, but
not an unconditional create-comment operation. The Jira comment API documents
creation with a `201` response; its documented request does not establish a
provider idempotency-key guarantee. A model that commits a comment and loses the
reply creates two comments on retry. `TestReviewAppendOnlyReceiptCannotRecoverUnknownReply`
demonstrates that sequence. This is a limitation of the proposed receipt strategy,
not proof that every possible Jira integration is unsafe.

Choose a proven idempotent receipt operation or explicitly support uncertain
receipt outcomes and provider-owned recovery. A receipt marker and a lookup need
their own concurrency and consistency proof. Do not claim that row locks make
external comment creation exactly once.

Delivery errors also lack a terminal outcome. A deleted source retries forever;
there is no supported receipt cancellation, terminal state, or redrive operation.
`TestReviewTerminalReceiptFailureCanStopRetrying` demonstrates the pending state.
Consider an explicit terminal classification and operator redrive. An adapter
must decide whether a failure is terminal; an HTTP status alone is insufficient.
Accepted work must remain independent of receipt failure.

## Prototype results

All prototypes are test-only and use the real PostgreSQL acceptance and receipt
store. They do not add production connectors or credentials.

| Provider shape | Experiment | Result and limits |
| --- | --- | --- |
| Slack signed events | HMAC and timestamp validation, actor allowlist, message identity distinct from event delivery identity, local HTTP reaction with lost reply followed by `already_reacted` | Existing acceptance and receipt interfaces fit. A real host must acknowledge event delivery within Slack's deadline; that HTTP response is distinct from the eyes reaction. No URL challenge handler, OAuth, or live delivery is implemented. |
| Jira polled comment | REST-shaped author/editor observation, narrow ADF parser, immutable comment identity, editor rejection and code-block rejection | Acceptance fits structured input after provider normalization. The prototype has no polling cursor or REST client. Append-only receipt recovery remains unresolved. |
| GitLab self-hosted notes | Signed webhook body and timestamp, create-event allowlist, duplicate observations, equal numeric IDs on two configured instances | Acceptance fits if instance identity is part of the connection. Numeric bot/project IDs alone are insufficient across self-hosted instances. Edits are rejected without fresh provenance. Reaction delivery is not proved by this prototype. |
| Direct HTTP caller | Verified fixture principal, caller idempotency key, durable `202` receipt, duplicate submission, queue delivery | Acceptance fits. Asynchronous acknowledgement is mandatory today even when the caller has no asynchronous receipt channel. |

The prototypes live in `services/intake/tests/prototype_*_test.go`. Shared
prototype configuration lives in `provider_prototypes_test.go`.

## Verification and evidence

Run the passing prototype suite with:

```sh
./buck2 test //services/intake/...
bash services/intake/checks/verify.sh
```

The failing reproductions were run in an isolated copy outside the checkout.
They assert desired behavior and fail against the reviewed implementation. Their
source, logs, and checkout snapshot are retained at `/tmp/factory-provider-review`.
This directory is local evidence, not persistent CI storage. Passing repository
checks do not dismiss these demonstrated failures.

Fix the three correctness findings before unattended deployment. Then add receipt
policy and explicit retry scope before building a production Slack, Jira, or HTTP
connector. Keep the small acceptance interface and opaque source identities;
these prototypes do not justify a common polling or webhook lifecycle.

## Provider references

- [Slack request verification](https://docs.slack.dev/authentication/verifying-requests-from-slack/)
- [Slack Events API delivery deadlines and retries](https://docs.slack.dev/apis/events-api/)
- [Slack reaction responses](https://docs.slack.dev/reference/methods/reactions.add/)
- [Jira comments and ADF](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-comments/)
- [GitLab comment event fields](https://docs.gitlab.com/user/project/integrations/webhook_events/)
- [GitLab signed webhook format](https://docs.gitlab.com/user/project/integrations/webhooks/)

## Resolution

The follow-up implements the three correctness fixes and receipt policy changes.
The findings above describe the pre-fix implementation.

- Snapshots now store encoded JSON in `bytea`. Regression cases cover large
  positive and negative numeric exponents and escaped NUL characters.
- Bad candidate identity or provenance no longer aborts the remaining GitHub
  comments. Source failures remain visible in sweep counts and errors.
- Connection cooldowns persist in PostgreSQL. A connection lock excludes another
  sender while permitting new acceptance. Redrive preserves the cooldown.
- Receipt mode is explicit. The HTTP prototype now uses an inline acceptance
  receipt and creates no asynchronous obligation.
- Malformed stored snapshots are quarantined. Terminal and uncertain provider
  outcomes stop automatic retries. Scoped inspection and redrive support recovery.

`tests/receipt_recovery_test.go` turns the reproduced failures into permanent
regressions and tests recovery through the actual CLI. The provider prototypes
remain test-only. Jira append-only delivery still needs provider idempotency or
a recovery lookup before production use. Stopping an uncertain reply does not
solve a lost database commit. Live provider delivery remains unverified.
