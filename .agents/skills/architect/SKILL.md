---
name: architect
description: Compare module designs and prove critical assumptions before implementing consequential factory changes, especially ownership, persistence, execution, and recovery.
license: See ../LICENSE.pstack and ../NOTICE.md.
---

# Architect

Design the caller's interface before implementation.
Read the repository glossary and [architecture](../../../docs/architecture.md).
Use existing domain terms. Distinguish intended design from implemented behavior.

## Ground and compare

Identify the requested behavior, current implementation, constraints, and observable acceptance condition.
For consequential choices, compare at least two structurally different designs.
Routine changes do not require competing designs.

For each design, show:

- A caller example and the smallest useful interface.
- State ownership, persistence, and external effects.
- Failure and recovery behavior at each irreversible step.
- Assumptions, trade-offs, and evidence needed to accept it.

Prefer designs that hide complexity inside its owning module.
Reject split ownership, exposed storage details, and interfaces that force callers to coordinate internal stages.
Avoid speculative shared packages and duplicate sources of truth.

## Prove and choose

Identify the assumption that matters most to the choice.
Use a disposable experiment outside the source tree to test it when practical.
For recovery, interrupt execution at the uncertain transition and observe what restart does.
Distinguish a model of a contract from proof that a real runtime satisfies it.

Report the alternatives, chosen design, evidence, and remaining uncertainty.
If the user requested design only, stop before implementation.
Otherwise implement the authorized scope and revise the design when evidence contradicts it.
Keep durable decisions in project documentation. Keep experiment state outside the source tree.

## Tools and verification

Use available tools without fixed provider or model requirements.
Delegate only when the user or applicable instructions authorize it.
If delegation is unavailable, compare designs directly and disclose the absence of independent review.

Use the pinned `./buck2` launcher for repository builds and tests.
Run `./buck2 run //tooling:verify` for scaffold changes.
Run visibility checks for build graph changes as root instructions require.
Do not treat a successful build or empty test suite as behavior proof.

Adapted from Lauren Tan's pstack architect workflow.
See [attribution](../NOTICE.md) for the source revision and license.
