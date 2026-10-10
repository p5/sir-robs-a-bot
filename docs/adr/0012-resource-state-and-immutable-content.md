# Store resource state separately from immutable content

Status: accepted.

The factory needs a repeatable persistence and delivery contract across request,
run, job, and effect owners. Incoming provider events are observations. Queue
notifications request reconciliation of a durable resource. These identities and
lifetimes must not be conflated.

We considered using objects as the sole authority and storing resource records
that reference immutable objects. Object-only storage fits immutable submissions,
but mutable progress and reliable discovery need further conventions. Choose
resource records with fixed input references, versioned application state, and
atomic delivery obligations as the general contract.

`packages/resources` implements this contract separately from the reconciliation
queue. Its content adapter stores immutable bytes. Its resource datastore owns
conditional state writes and pending notifications. The repository hides upload,
commit, integrity checks, and delivery ordering from consuming workflows.
The queue retains logical resource keys and owns no content or application state.

Upload content before committing a reference. The resource commit establishes
acceptance and atomically records a delivery obligation. Digest-addressed objects
avoid allowing a failed, unaccepted upload to pin the input of a logical request.
Concurrent creators return the first committed resource. Retry may leave unused
objects, which require a separate retention and collection policy.

Every state update checks its expected version and creates a notification. Relay
acknowledgements check the notification generation so they cannot remove newer
work. Failed or uncertain enqueue remains recoverable. Delivery and reconciliation
may repeat. Application owners must persist progress and make effects repeatable.
There is no claim of exactly-once external execution.

The first durable adapters are S3 for content and DynamoDB for state. Hosts supply
SDK clients and enforce access rights. Owner namespaces and content prefixes do
not themselves establish authorization. Other deployments can implement the same
operation contracts. Large streaming artifacts remain a distinct capability.

A request-to-run prototype uses independent owners to exercise immutable inputs,
mutable progress, stable handoff identities, and recovery. Intake implements this
contract through its AWS runtime. Acceptance commits independent receipt and
factory handoff intent. Resource notifications wake only the intake owner, which
advances each obligation separately. Connection progress records call intent and
outcome; interrupted calls use read-only observation or stop as uncertain.
Intake is not deployed. Prefer clean schema changes and fresh development storage
over compatibility gates or legacy imports. See the
[intake storage guide](../../services/intake/storage.md). Existing request IDs
and factory request queue keys remain unchanged.

See the [resource guide](../../packages/resources/README.md) for limits,
verification evidence scope, and adapter requirements.
