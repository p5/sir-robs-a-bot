# Add an intake provider

Each adapter belongs in `internal/providers/<provider>`. Keep API clients, event
formats, authentication, author and editor checks, command parsing, and receipt
metadata there. GitHub and GitLab implement both polling and webhook transports. The core in `internal/intake` must
not import a provider package.

## Submit an authorized request

Depend on `intake.Acceptor`, whose operation is:

```go
Accept(context.Context, intake.Submission) (intake.Acceptance, error)
```

A submission contains:

- `Source.Connection.Provider`, a stable lowercase provider name.
- `Source.Connection.Account`, the stable receiving account or installation ID.
- `Source.Scope`, the repository, workspace, or tenant that contains the source.
- `Source.Kind` and `Source.ID`, the logical source type and stable ID.
- `Author` and `SubmittedBy`, stable identities for the original author and responsible submitter.
- `Body`, `Instruction`, `CreatedAt`, and `ObservedAt`, the authorized observation.
- `Metadata`, bounded provider-owned JSON needed to acknowledge that source.
- `ReceiptMode`, explicitly `ReceiptAsync`, `ReceiptInline`, or `ReceiptNone`.

Identity strings can contain provider-specific separators. Each has a 512-byte
limit; provider names have a 64-byte limit and use lowercase letters, digits, and
hyphens. Bodies have a 64 KiB limit, instructions 16 KiB, and metadata 16 KiB.
Text must be valid UTF-8. Instructions must not be empty. Metadata must be JSON.
Intake validates these bounds again before persistence.

Verify the event, actor, and instruction before calling `Accept`. The interface
is internal trusted code, not an authenticated public endpoint. A provider name
or actor string is not proof of identity. Webhook adapters must authenticate
incoming events. Polling adapters must authenticate their receiving account and
verify source provenance. Adapters own their submission allowlist policy.

Register the connection through `Store.Activate` before accepting requests. It
preserves the initial activation time. Polling adapters can use `Store.Activation`
to recover their discovery floor. An adapter owns any additional discovery state;
intake does not prescribe a poller or webhook lifecycle.

The core derives the request ID from the complete source tuple. Do not use a
webhook delivery ID, mutable display name, timestamp of an edit, or request body
as its logical identity. Concurrent and repeated observations return the same
`Acceptance.RequestID`. `Acceptance.Created` reports whether this call inserted
the snapshot. The first accepted observation wins. An edit cannot replace it.

`Accept` records the snapshot and queue obligation in one transaction. It adds a
receipt obligation only for `ReceiptAsync`. `ReceiptInline` means the adapter
returns the durable acceptance ID in its response. `ReceiptNone` deliberately
omits a receipt. Missing or unknown modes fail validation. The first accepted
policy is immutable, including on duplicate acceptance.

Snapshots use `bytea` so PostgreSQL cannot rewrite provider JSON numbers or reject
escaped NUL characters. Intake does not query provider metadata in SQL. An uncertain reply is safe to retry with the same source.
Queue consumers retrieve the snapshot by the returned ID under `factory-request`.
Do not separately coordinate application persistence and queue insertion in an
adapter.

## Deliver the acknowledgement

Implement the second contract:

```go
type Acknowledger interface {
    Connection() intake.Connection
    Acknowledge(context.Context, intake.Request) error
}
```

Bind the sender to its authenticated connection. Decode and validate your own
metadata in `Acknowledge`. Use the accepted snapshot, not a freshly edited source
body. The GitHub sender adds an eyes reaction to the original issue or comment.
Other providers can use their own receipt operation with the same meaning.

The operation must be idempotent across retries. A lost API reply or database
commit can repeat it. Use a provider-supported idempotency key, an idempotent
reaction operation, or a recovery lookup that identifies the existing receipt.
A read followed by an unconditional reply does not establish idempotency.
If the provider cannot make receipts idempotent, document that limitation and
resolve the recovery design before enabling delivery.

Return `intake.DeliveryError` to request a longer retry delay. Set `RetryScope` to `RetryConnection` for an
account-wide condition such as rate limiting. This persists a connection cooldown
that later passes and other workers must honor. Delivery is serial per connection;
other connections and request acceptance can proceed independently.

Set `Disposition` to `DeliveryTerminal` for a confirmed permanent failure, or
`DeliveryUncertain` when a non-idempotent operation may already have succeeded.
Both stop automatic retries. Do not classify all authorization errors as permanent;
credentials and repository access can recover. Unknown dispositions and malformed
stored snapshots are quarantined. These states affect receipts, not accepted work.
A stopped state alone cannot prevent duplicates after a lost database commit.
The idempotency requirement still applies.

Ordinary errors retry after at
least one minute while other due receipts can proceed. The core persists generic
diagnostics, attempts, and next retry times. It never stores raw API error text.
Use `Store.Acknowledgement` to inspect state within the authenticated connection.
After resolving the cause, call `Store.RedriveAcknowledgement`. For an uncertain
receipt, first establish whether the external effect occurred. Redrive preserves
the account cooldown and attempt history. It refuses corrupt snapshots, completed
receipts, and requests with no receipt obligation.

Completed receipt records remain stored; duplicate acceptance cannot recreate them.

Call `Store.AcknowledgeDue(ctx, sender)` with a deadline. It selects only this
provider connection and excludes concurrent senders with row locks. Queue delivery
is independent. A rejected acknowledgement does not reject accepted work.

## Wire and verify the adapter

Add Buck targets and wire the adapter into the service host with explicit
credentials and configuration. Add its packages to `checks/verify.sh` so the
verifier discovers every fuzz target. Keep the dependency source of truth in the
Go module and regenerate the shared workspace vendor targets.

Test malformed inputs, unauthorized actors, changed source provenance, duplicate
delivery, connection isolation, rate limits, cancellation, and acknowledgement
recovery after a lost reply. Fuzz parsers and assert authorization and identity
properties as well as absence of crashes. Webhook adapters also need signature
verification and replay tests.

Use the shared PostgreSQL-backed acceptance tests. The second-provider fixture
in `tests/provider_test.go` uses opaque workspace and message IDs through the
same acceptance, queue, and acknowledgement operations. It proves the module
boundary, not delivery through a real third-party service. GitHub's executable
fixtures test the complete current CLI path. Live provider delivery needs its
own evidence before deployment.

Run from the repository root:

```sh
export PATH="$PWD/.tools/bin:$PATH"
./buck2 test //services/intake/...
bash services/intake/checks/verify.sh
./buck2 run //tooling:fmt
./buck2 audit visibility //... toolchains//...
./buck2 run //tooling:verify
```

## Recover interrupted receipts

An asynchronous adapter can implement `intake.ReceiptObserver` with
`ObserveAcknowledgement(context.Context, intake.Request) (bool, error)`.
This operation must only read provider state. Return true only when the receiving
account's receipt exists on the exact source. Absence never authorizes a repeated
mutation after an interrupted attempt. Intake stops an unproven outcome as
uncertain and frees the connection for later receipts.

Classify a transport failure after POST, a server error with an unknown effect,
or an invalid success response as `DeliveryUncertain`. Use `DeliveryRetry` only
when another attempt is safe. Propagate account-wide rate limits with
`RetryConnection`, including rate limits encountered during observation.
Test read-only recovery, account matching, uncertain replies, cooldowns, and a
second receipt after the first stops. See the [storage protocol](storage.md).
