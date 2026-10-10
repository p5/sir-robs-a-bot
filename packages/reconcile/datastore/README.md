# Coordination datastores

This package owns the queue-store interface and its portable metadata types.
Maintainer: the repository maintainers.
The reconciliation engine depends on this package. Adapters depend on this
package and their backend clients; they do not import the engine.

| Path | Responsibility |
| --- | --- |
| `store.go` | Store operations, queue diagnostics, claims, renewal, release, redrive, completion, and sentinel errors |
| `key.go`, `validation.go` | Portable identity and input rules |
| `inspection.go` | Optional bounded queue inspection and observations |
| `memory/` | Ephemeral single-process coordination |
| `postgres/` | PostgreSQL persistence and database-authoritative scheduling |
| `dynamodb/` | AWS-native persistence and adapter-clock scheduling |
| `storetest/` | Shared behavior contract and operation-sequence checks |

Applications construct an adapter and pass it to `reconcile.New`. No driver
registry or default backend exists. Adapter READMEs describe client ownership,
provisioning, timing, input limits, and recovery assumptions.

Use `datastore.Key` for resource identity. Claims and completions coordinate dispatch
and adapters. Types are defined here once. The engine owns reconcilers, scheduling results,
retry policy, and dispatch configuration.

```sh
export PATH="$PWD/.tools/bin:$PATH"
./buck2 build //packages/reconcile/datastore:datastore
./buck2 test //packages/reconcile/...
```

See [adding a datastore](../../../docs/adding-a-datastore.md) before implementing
another backend. Each adapter must run `storetest.Run` and the sequence corpus,
plus backend-specific fault and recovery tests. The interface package has no
external dependencies. It produces a Go archive and owns no execution artifacts.
