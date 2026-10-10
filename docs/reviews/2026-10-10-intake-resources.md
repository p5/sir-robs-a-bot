# Intake resource workflow review

Reviewed on 2026-10-10. Scope includes the pending intake service, shared resource
library, AWS runtime, provider boundaries, tests, and build integration. Two
adversarial passes covered recovery and external input boundaries. One agent
performed both passes. They are not independent reviews.

## Act on: stale receipt attachment repeats an external effect

Severity: P1. Status: fixed, with a reproduced regression.

A receipt attachment reads the receipt and connection separately. It can read a
pending receipt, then pause before reading the connection. Meanwhile, the earlier
connection worker delivers that receipt, publishes its outcome, and clears the
connection. The paused attachment resumes and attaches the old receipt version.

Previously, the connection handler called the provider again. Outcome publication
then rejected the changed receipt version. This sequence could repeat an external
effect and leave the connection blocked by an outcome it could not publish.

[The regression](../../services/intake/internal/intake/resource_workflow_test.go)
uses a barrier between the two reads. It lets the earlier delivery finish before
resuming attachment. The test failed before the fix with `receipt changed before
outcome handoff` after the second provider call.

[The connection handler](../../services/intake/internal/intake/resource_receipts.go)
now checks the receipt version and pending state before starting a ready attempt.
It clears a stale attachment without a provider call. The regression asserts one
call, the original delivered receipt, and a free connection. The checks include
race detection. Conditional connection updates still exclude competing owners.

## Consider: deployment evidence remains separate

Live tests use real DynamoDB and S3 in `us-east-1`, with an authenticated admin
role. Provider HTTP calls remain fixtures. This covers AWS service semantics,
not least-privilege IAM, KMS policy, provider notification delivery, or failover.
Validate those properties against the deployment configuration before unattended
operation. No provider token, production request, or deployment was used.

Tests use fresh `factory-live-` infrastructure and isolated application
namespaces. Cleanup removes objects, the bucket, and the table. Verification
checks that the bucket and table no longer exist. The application itself never
provisions or deletes infrastructure. See [live checks](../../services/intake/storage.md#live-aws-checks).

## Dismissed: queue duplication requires duplicate effects

An unknown enqueue reply can repeat a notification. The queue schedules a logical
resource key. The resource records retain immutable input and versioned progress.
Receipt attempts persist `sending` before the provider call. Recovery from that
phase performs read-only observation or stops as uncertain. It never authorizes
another POST from a negative observation.

These rules address repeated delivery, not exactly-once HTTP execution. A paused
former worker cannot be fenced by a DynamoDB write. Operator redrive requires
stopping former workers and checking the provider. The
[recovery guide](../../services/intake/storage.md#receipt-recovery) states this limit.

## Reviewed boundaries

The input pass traced signed webhooks, bounded bodies, allowlisted author and
editor identities, provider pagination, trusted endpoints, reaction targets, and
request validation. It checked that provider clients reject redirects and that
unknown POST replies stop or use provider-owned read-only recovery.

The persistence pass traced upload-before-commit, duplicate creation, conditional
state updates, notification generations, independent dispatch obligations, scoped
connection queues, cooldowns, retry budgets, and redrive. Existing regressions
cover lost state replies before and after commit. Shared adapter contracts cover
concurrent updates and stale notification acknowledgements.

No other demonstrated correctness finding remains from these passes. This does
not prove that the implementation has no defects. Malformed administrative
storage writes can still require operator repair. Referenced content needs a
retention policy; the library does not collect unused objects or restore missing
content. Cross-service authenticated input reads remain future work.

## Verification

The complete `./buck2 run //tooling:verify` passed all eight stages. This includes
19 Buck test targets, dependency regeneration, build visibility, Go vet, race
checks, local datastore integration, consumer prototypes, and all discovered fuzz
targets. Repository shell and workflow lint also passed.

Live AWS checks passed for the shared resource contract, S3 conditional writes
and missing objects, both provider CLI paths, interrupted receipt recovery, and
the provider-independent worker. Reopening the resource contract used independent
SDK clients. Both temporary tables and buckets were deleted. Their absence was
verified, and no test containers remained after local verification.

The live role had admin access. Least-privilege IAM and live provider accounts
remain unverified. Logs and source snapshots stay outside the repository at
`/tmp/factory-pr-review`. This local evidence does not replace CI reports.
