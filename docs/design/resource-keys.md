# Resource keys

A resource key identifies what the factory must observe. It does not identify a
reconciliation attempt. Updating desired state or retrying work keeps the key.
An immutable candidate, artifact, or execution run can have its own resource key
when the factory reconciles that resource separately.

The complete identity is `(store namespace, Kind, ID)`.
`Kind` selects the reconciler. `ID` is an opaque resource reference.
Applications supply both explicitly.

```go
key := datastore.Key{
    Kind: "github-path",
    ID: "https://github.com/p5/example/blob/main/README.md",
}
err := store.Enqueue(ctx, key)
```

External resource modules should use their established resource references.
Use GitHub URLs for GitHub resources and digest-pinned references for immutable
artifacts. Factory-owned resources can use namespaced URNs, for example
`urn:factory:request:<id>`. These are conventions, not new resource parsers.
The first owning module must define its exact grammar and tests.
Existing opaque IDs remain valid.

DriftlessAF similarly queues string keys. Its reconcilers interpret GitHub URLs,
OCI references, and checksum-pinned APK references. It does not require one
universal URI grammar. We keep dispatch separate from the reference because a
factory can apply different reconciliation policies to the same external resource.
See [its queue protocol](https://github.com/driftlessaf/go-driftlessaf/blob/d34c6ab98c88d3691a2ded172d35c131ee0261a5/workqueue/workqueue.proto)
and [APK key parser](https://github.com/driftlessaf/go-driftlessaf/blob/d34c6ab98c88d3691a2ded172d35c131ee0261a5/reconcilers/apkreconciler/apkurl/apkurl.go).

## Identity ownership

The resource module validates and canonicalizes its references before it creates
keys. The core checks nonblank UTF-8 values without control characters.
Adapters can impose documented size limits.
Stores preserve the reference exactly. They must not decode escapes, lowercase
paths, strip query strings, resolve redirects, or convert URLs to filesystem paths.
For example, `a%2Fb` and `a/b` remain different IDs at the datastore boundary.

A key carries identity. Consumer-owned application records carry policy and inputs. Claim fences carry
ownership generations. Keep these concerns separate. A remote transport can carry
`Kind` and `ID` as separate fields without changing the store model.

## Alternatives considered

A single string key, as used by DriftlessAF's queue protocol, is convenient for
transport. It would force our host to infer dispatch from a reference or embed
policy into its spelling. It would also require changing persisted primary keys.

A mandatory URI scheme would provide uniform parsing, but would exclude existing
artifact reference formats and require a grammar before their owning projects
exist. We retain explicit kind-based dispatch and opaque resource references.
The shared contract tests exercise exact URL identity and kind separation against
memory, PostgreSQL, and DynamoDB.
