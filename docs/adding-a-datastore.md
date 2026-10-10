# Add a datastore

Implement `datastore.Store` in `packages/reconcile/datastore/<backend>`.
Applications construct the adapter and pass it to `reconcile.New`.
The interface and portable types live in `packages/reconcile/datastore`.
Adapters import that package, not the engine.
The engine has no backend registry, driver discovery, or datastore selection logic.
Adding an adapter must not require editing the engine or importing the adapter into it.

Start with [the Store interface](../packages/reconcile/datastore/store.go) and
[the coordination design](design/core.md). The interface has seven operations.
Their atomicity and recovery behavior are the compatibility contract.
A backend must support those guarantees before it can host distributed controllers.
The public memory adapter is an explicit exception for ephemeral, single-process
use. It keeps the operation semantics but has no durability or independent clients.
Its tests reuse the same instance and cannot prove restart recovery.

## Directory layout

Use the PostgreSQL adapter as an example. Create files when they have a clear purpose.
Keep small adapters in fewer files.

```text
packages/reconcile/datastore/<backend>/
  BUCK
  README.md          # Setup, guarantees, limits, and test commands
  store.go           # Constructor and client ownership
  resources.go       # Store operations
  validation.go      # Backend limits and request validation
  tests/
    BUCK
    fixture_test.go  # Isolated backend and independent clients
    contract_test.go # Shared contract suite
    recovery_test.go # Backend failures and restart behavior
```

Keep schema migrations, queries, transaction helpers, and SDK types inside the
adapter. Do not add them to the core interface. Do not expose a generic transaction
API to reconcilers. A backend can use a different storage representation.
Name private helpers after their operation or invariant.
Share implementation helpers only when two adapters actually need them.

## Implement and wire the adapter

1. Accept the backend client and namespace in an explicit constructor. Document
   who closes the client. Separate migrations and provisioning from construction.
2. Add `var _ datastore.Store = (*Store)(nil)` to check the interface at build time.
3. Implement all seven operations. Return the core sentinel errors through
   `errors.Is`. Add operation context when wrapping other errors.
4. Document the scheduling clock and precision. Use conditional writes and
   durable schedules. Preserve
   enqueue requests that arrive during a claim. Check the fence and
   active claim at the atomic commit decision. Expiry permits recovery; only a
   successor claim or completion revokes ownership. Never reuse a fence for a key.
5. Honor cancellation. Return errors for unknown write outcomes. Do not report
   success before the backend acknowledges durability.
6. Run `storetest.Run` with isolated fixtures and independent clients. See
   [its fixture guide](../packages/reconcile/datastore/storetest/README.md).
7. Add backend tests for partial failure, interrupted controllers, restart after
   acknowledged writes, and competing clients. Test backend limits and migrations.
   Document which failure modes the tests do not cover.
8. Declare the Go dependency in `packages/reconcile/go.mod`, then run
   `packages/reconcile/dependencies/generate.sh`. This generates vendored sources
   and Buck targets with the bundled gobuckify. Do not edit generated targets.
9. Add a Buck library and a consumer test target. Copy the PostgreSQL targets'
   structure and change the package paths and dependencies. Keep CGo disabled
   unless the backend requires a deliberate toolchain change.
10. Run the adapter tests, Go formatting, visibility audit, and repository verifier.

AWS and Cloudflare adapters must establish how their chosen service supplies
atomic ownership, lease timing, and scheduling. The interface does
not assume SQL, but wrapping a key-value SDK alone does not establish these guarantees.
Record any service limits in the adapter README. Avoid weakening the contract to
accommodate a backend. Propose a design change when its guarantees differ.

Preserve [resource keys](design/resource-keys.md) exactly. Call `Key.Validate`
before key-based operations, then enforce documented backend limits.
Resource parsing and canonicalization belong to the owning resource module.

Run `storetest.RunSequence` for every `storetest.SequenceCorpus` entry with
independent clients. See [adversarial testing](design/adversarial-testing.md).
Use `Claim.Validate`, `Completion.Validate`, and `ValidateKind` for portable
input rules, then enforce backend-specific limits before writing.

Store only queue metadata. Application snapshots, versions, and payload storage
belong to consumers. Preserve completed entries until a tested fence-retention
policy exists. See [the ownership decision](adr/0004-key-based-reconciler-queue.md).

Lease renewal must preserve or extend the current expiry. Renewal, release,
completion, and takeover must check the active fence atomically. Recovery of an
expired active claim increments abandonment atomically with its new fence.
Success clears both budgets. Release preserves them. Redrive resets them only
without active ownership and retains fence history. Run the shared recovery suite
and extend service-specific fault injection to renewal, release, and redrive.

## Optional inspection

Implement `datastore.Inspector.List` if the backend supports operational listing.
Keep it separate from the seven dispatch operations. Run `storetest.RunInspection`
with the adapter's isolated fixture. Preserve UTF-8 byte ordering, kind and namespace
isolation, exclusive cursors, and the 1 to 100 entry limit. Document read consistency
and cost. Never retain a transaction or ownership lock across pages.
See [the inspection decision](adr/0006-bounded-queue-inspection.md).

Priority escalation must take the maximum stored and requested values. Preserve
priority through retries and concurrent enqueue; clear it on successful completion
without pending follow-up. A protected completion delay sets a separate scheduling
floor even when a newer enqueue exists. Ordinary enqueue preserves it, and redrive
clears it. Eligibility checks that floor with due time and lease expiry. The shared
contract includes these schedules. Keep the full transition atomic.
