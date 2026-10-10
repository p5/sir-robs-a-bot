# Store contract tests

Every datastore adapter runs `storetest.Run`. The suite imports only the public
`datastore` interface. Add portable behavior tests here so every adapter gets them.
Keep SQL, service APIs, and backend fault injection in the adapter's tests.

Supply a fresh fixture for each subtest. Register cleanup with `t.Cleanup`.
`Elapse` must advance the adapter's scheduling clock. A backend that uses server
time can wait on that clock. `Reopen` must create an independent client to the same namespace.
An ephemeral memory fixture can return the same instance. This does not prove durability.

```go
func TestStoreContract(t *testing.T) {
    storetest.Run(t, func(t *testing.T) storetest.Fixture {
        store, namespace := newIsolatedStore(t)
        return storetest.Fixture{
            Store: store,
            Elapse: func(duration time.Duration) {
                waitForStoreTime(t, duration)
            },
            Reopen: func() datastore.Store {
                return openStoreClient(t, namespace)
            },
        }
    })
}
```

The helper functions in this example belong to the adapter's test fixture.
The PostgreSQL tests provide a complete working example.
The suite tests coalesced enqueue, concurrent ownership, expiry, stale writes,
notifications during claims, durable schedules, failure diagnostics, and cancellation.
It also checks renewal, abandoned-claim accounting, orderly release, and redrive.
Lease recovery tests check that successor claims fence renewals and releases.
Passing it does not prove durability after power loss or availability during failover.
See [adding a datastore](../../../../docs/adding-a-datastore.md) for required recovery tests.

`RunSequence` compares operation histories with a separate state model.
Replay every entry from `SequenceCorpus` against each durable adapter.
Add minimized sequence regressions to that corpus so all adapters test them.
The core fuzzer explores generated histories against the test store; replay against
a real backend covers only the checked-in histories.

Adapters that support `datastore.Inspector` also run `RunInspection` with their
isolated fixture. The suite checks pagination, byte ordering, kind and namespace
isolation, concurrent inserts, lifecycle metadata, cancellation, and invalid bounds.
