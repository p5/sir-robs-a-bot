# Keep queue inspection separate from dispatch

Operators need to find queue diagnostics without knowing every key. Add an optional
`datastore.Inspector` interface with bounded pages within one kind. A cross-kind
query with state filters would require broader DynamoDB scans or another index.
Start with ordered metadata pages and let consumers select the entries they need.

`ListRequest{Kind: "request", Limit: 50, After: previous.Next}` selects the next
page. `After` is an exclusive resource ID in UTF-8 byte order. It is deliberately
transparent: callers already own these identities, and the cursor grants no
authority. Adapters bind namespace through their configured instance. Consumers
must bind the kind and namespace when exposing a cursor to an external caller.
Authorization belongs to that consumer.

Pages expose pending status, failure and abandonment counters, diagnostics, due
time, and lease expiry. They expose no ownership tokens or application state.
Inspection cannot claim, renew, release, or complete work. Each page is a current
observation, not a snapshot spanning requests. Inserts before the cursor require
a new traversal. A nonempty continuation can lead to an empty final page.

PostgreSQL uses its primary key with byte-ordered IDs and reads at most limit plus
one rows. DynamoDB makes one strongly consistent Query with a Limit and validates
every returned record and continuation key. It does not follow pages internally.
The memory adapter scans and sorts local records and has no bounded-work guarantee.
All adapters return at most 100 entries. This limits page size, not total traversal
cost or response bytes. DynamoDB also applies its service page-size limit.

Shared tests cover ordering, kind and namespace isolation, continuation across
client reopening, concurrent insertion, lifecycle observations, and cancellation.
Fuzzing covers request validation and DynamoDB response decoding. Tests do not
establish a snapshot, live AWS behavior, or large-backlog performance. No independent
reviewer assessed the change. Aggregated metrics and server-side state queries
remain separate decisions.
