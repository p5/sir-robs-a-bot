---
name: interrogate
description: Adversarially review a factory design or change for concrete correctness, ownership, isolation, and recovery failures when the user requests a challenge or review.
license: See ../LICENSE.pstack and ../NOTICE.md.
---

# Interrogate

Challenge the design or change against its stated intent.
Read the relevant implementation and [architecture](../../../docs/architecture.md).
Treat the architecture as a proposed contract, not evidence that behavior exists.

## Scope and examine

State the review scope and intended result before reviewing.
Include relevant uncommitted and untracked files when the request concerns current work.
Trace callers, state owners, persisted formats, and external effects beyond the diff.

For execution and recovery changes, examine applicable cases:

- A controller crashes before or after launch, but before it records the result.
- A repeated request or stale owner starts duplicate work.
- Cancellation races with completion or leaves child execution active.
- A retry resets budgets or repeats an external effect.
- A changed candidate reuses old evidence or altered checks create a false pass.
- Artifact capture fails, escapes its workspace, or cleanup deletes evidence.

These cases guide investigation. Do not invent findings when a case does not apply.

## Validate findings

For each finding, show the trigger, failing sequence, consequence, and supporting code or contract.
Run the smallest useful reproduction when practical.
Mark untested assumptions and distinguish demonstrated failures from plausible risks.
Use Buck targets for existing checks and disposable external directories for experiments.
Green CI and agreement between reviewers do not prove the absence of a defect.

Independent reviewers are useful when authorized and available.
Give them the same intent and scope without supplying expected findings.
Use supported tools and models. Do not require a particular provider or automatically spawn agents.
If only one reviewer is available, disclose that limitation.

## Report

Rank findings by consequence and confidence, with file locations where available.
Classify findings as act on, consider, or dismissed, with a concrete reason.
Do not dismiss a demonstrated failure merely because only one reviewer found it.
If there are no findings, say what was checked and what remains unverified.
Do not edit code, publish comments, or create a PR unless that action is separately authorized.

Adapted from Lauren Tan's pstack interrogate workflow.
See [attribution](../NOTICE.md) for the source revision and license.
