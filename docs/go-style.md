# Go style

Follow [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments) and the
[Kubernetes coding conventions](https://www.kubernetes.dev/docs/guide/coding-convention/).
The core uses their explicit control flow and operation-focused functions.
It does not need Kubernetes dependencies to follow that style.

Use domain names such as `claim`, `completion`, `resource`, and `expectedVersion`.
Keep conventional names such as `ctx`, `err`, and `db` where their meaning is clear.
Do not compress independent statements onto one line.
Use guard clauses for errors and invalid input. Keep transactions and ownership
changes visible in the operation that performs them.

Split files by responsibility. Extract a helper when it names an operation or
hides a detail the caller does not need. Avoid helpers that merely shorten a line.
Keep backend queries, migrations, and row conversion inside their adapter.
Explain ordering constraints and recovery assumptions in comments.

Tests should identify the operation and expected behavior when they fail.
Use named cases when inputs vary. Synchronize concurrent tests with observable
barriers. Do not assume a goroutine has run because another goroutine slept.
Keep backend fixtures separate from behavior tests as the suite grows.
