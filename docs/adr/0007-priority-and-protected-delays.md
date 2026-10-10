# Preserve retry floors while accepting priority changes

The queue needs urgent work and retry delays that survive repeated notifications.
Keep priority on the authoritative queue record. Add a separate protected time
floor, checked atomically with due time and lease eligibility. Ordinary enqueue
can change urgency without clearing the floor or retry budgets.

`Enqueue(ctx, key, datastore.EnqueueOptions{Priority: 100})` escalates urgency.
The default is zero. Higher unsigned values run first among eligible work.
Repeated enqueue takes the maximum priority. Claims carry observed priority;
completion keeps the current record's priority, including concurrent escalation.
A successful completion with no retained or requested follow-up clears it.
Errors and suspended work preserve it. Explicit redrive preserves priority.

A single mutable due time would let an event storm erase backoff. Ignoring events
during backoff would lose wake-ups. Instead, keep the enqueue sequence and due
time, and add a separate floor. `Completion.ProtectDelay` sets that floor to
completion time plus `After`, even if an enqueue won before completion. Later
enqueues preserve it. Eligibility requires due time and protected floor to pass.
Lease and fence checks still decide ownership. Redrive clears the floor explicitly
when no claim is active.

Error retries use protected delays by default. A reconciler can return
`Result{Again: true, After: delay, ProtectDelay: true}` for a rate limit or other
successful wait. An ordinary unprotected follow-up remains interruptible by
enqueue. Protected delays require a positive delay and a follow-up request.
Waiting does not consume a reported failure; returning an error does.

PostgreSQL persists priority and the floor with queue completion under row locks.
Its pending-work index follows priority order. DynamoDB keeps both in the same
revision-conditional record. Its enqueue update returns the existing record on
definite condition failure, avoiding another read for priority coalescing when the
endpoint supports conflict images. Unknown writes still return without mutation
retries. The storage format remains version 1 during initial development.

Memory uses ready and future heaps maintained in the same critical section as
record mutation. A backwards custom clock rebuilds those indexes. DynamoDB
retains only the best 128 candidates while reading requested kind partitions.
Conditional claims recheck current eligibility. Contention can invalidate a whole
candidate window; the next poll discovers remaining work. No cross-process cache
or eventually consistent index determines whether a claim is authorized.

Priority does not preempt active calls or promise starvation freedom. Persistently
high-priority producers can delay lower-priority work. Concurrent changes also
prevent a total queue-wide ordering guarantee. Shared tests cover escalation,
retention, protected enqueue races, redrive, and invalid options. An independent
scan-based fuzz oracle checks memory scheduling, including clock rollback and
expiry. Backend tests cover priority beyond the candidate window. No independent
reviewer assessed this change.
