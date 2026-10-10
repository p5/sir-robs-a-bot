# Request-to-run prototype

This executable proves the shared resource contract with two owners. A request
owner preserves an immutable instruction. A run owner stores a reference to that
request and advances its own state from pending to complete. The operation hashes
the instruction. It does not run an agent or claim to complete software work.

```sh
./buck2 run //packages/resources/examples/request-run/cmd:request-run -- \
  demo 'inspect this request'
```

The demo uses independent ephemeral resource/content stores and the memory queue.
It prints the final run record as JSON and lifecycle logs on stderr. It stops
when both relays and the worker have no work. Empty instructions fail.
All state disappears when the process exits. This command needs no credentials.

Both owners use the same `resources.Repository` interface. The request reconciler
creates a run under a stable ID before marking the request routed. Interrupted
handoff repeats creation safely. The run reconciler reads the immutable request,
records its digest, and leaves terminal progress unchanged on repeated callbacks.
The owners communicate through explicit dependencies in this prototype. A
separate deployment must expose an authenticated owner interface rather than
sharing unrestricted database or bucket credentials.

The integration suite supplies S3 SDK clients and separate DynamoDB resource
namespaces to the same application. It tests repeated callbacks and failed queue
completion after persisted progress. Unit tests interrupt uploads, resource
commits, and delivery acknowledgements. See the [resource guide](../../README.md)
for contracts, backend limits, verification commands, and deployment responsibilities.
