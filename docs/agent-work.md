# Agent work

Use this procedure for substantial implementation tasks and work that spans sessions.
Keep task notes, checkpoints, logs, and evidence outside the source tree.
Keep durable project procedures and automated checks with their owning projects.

## Define the task

Before implementation, record:

- The requested result and an observable acceptance condition.
- The affected projects, allowed changes, and excluded work.
- The commands and observations that prove the result.
- Known limits on time, cost, retries, and external capabilities.

Use limits supplied by the operator or execution platform.
If no limit exists, state that fact. Do not invent approval or spending authority.
A time limit bounds work. It does not define success.

## Implement and verify

Complete small units of work with checks between units.
For a bug, reproduce the failure before the fix when practical.
Exercise the actual interface and inspect its observable effects.
Use the [project guide](adding-a-project.md#4-add-behavior-checks) for verification procedures.

Record the revision, commands, outcomes, and evidence locations.
For a dirty checkout, retain the patch and relevant untracked inputs outside the source tree.
A commit SHA alone does not identify uncommitted code.
Keep credentials and secret values out of reports and retained patches.

Distinguish passed, failed, blocked, and inconclusive results.
Missing evidence and unavailable checks do not count as passes.
After code changes, rerun the affected checks. Do not present older evidence as current verification.

Give concurrent attempts separate workspaces and mutable runtime state.
File separation alone does not isolate processes, credentials, ports, or data.
Review shared writes before parallel work starts.

When a mistake repeats, prefer a structural fix or an automated check over another instruction.
Prove that a new check catches the mistake that motivated it.

## Hand off or resume

At a safe boundary, write a checkpoint with:

- The task, scope, acceptance condition, and remaining limits.
- The checkout path, branch, revision, and uncommitted changes.
- Completed work and checks, including evidence locations.
- Failed attempts, unresolved questions, and known blockers.
- Owned processes or temporary resources that still need cleanup.
- The next concrete action.

Keep checkpoints in operator-selected storage outside the source tree.
Use persistent storage when work must survive host loss.
On resume, inspect the checkout and current external state before relying on the checkpoint.
Recheck evidence if the code or environment changed.
