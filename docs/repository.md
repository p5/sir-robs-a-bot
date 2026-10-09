# Repository conventions

Projects share commands and documentation conventions across languages.
Each project owns its implementation and dependency declarations.
Follow [the new project guide](adding-a-project.md) for the complete procedure.

## Project placement

Use `services/<name>` for an independently deployable process.
Use `apps/<name>` for a user application.
Use `packages/<name>` for a shared library.
Name projects for their purpose, not their language.

Add directories when they have real contents.
The top-level placeholders reserve places for future projects.
They do not prescribe service names, languages, or deployment topology.

## Project documentation

Each project needs a README with:

- Its purpose and owning maintainer.
- Its external interface and required configuration.
- Its development, verification, and build commands.
- Its dependencies and artifact output.
- Its data ownership and migrations, if applicable.

Keep project instructions beside the project.
Keep the root instructions short.
Record domain terms in `CONTEXT.md`.
Record costly architectural choices in `docs/adr/` when a real trade-off occurs.

## Command interface

Use the following names for equivalent tasks across projects.
Use `./buck2 build` for artifacts and `./buck2 test` for automated tests.
Use `./buck2 run` for executable development tools.
See [the build guide](build-system.md) for target syntax and setup.

| Task | Behavior |
| --- | --- |
| `fmt` | Apply formatting |
| `lint` | Check code rules without changes |
| `check` | Check types and compilation |
| `test` | Run automated tests |
| `build` | Produce an artifact |
| `dev` | Start local development, where applicable |
| `verify` | Run required checks without changes |

Document tasks that do not apply to a project.
Do not add successful placeholder commands for missing checks.
CI must call the same checks that developers and agents use locally.
The `//:check` target checks repository files. Each application needs its own behavior checks.

## Dependencies and contracts

Keep business logic inside its owning module.
Give shared libraries explicit users.
Keep dependency declarations in one authoritative form.
Generate other build metadata when another tool needs it.

Put cross-language interface definitions in `contracts/`.
Define ownership, compatibility rules, and generation commands with each contract.
Keep generated clients with their language-specific packages.
Each deployable process owns its data and migrations.

## Agent execution

Keep factory source separate from repositories that agents modify.
Give each job an isolated execution environment with explicit capabilities and resource limits.
File separation alone does not provide execution isolation.
Keep production credentials and host control outside agent jobs.

Keep automated code tests with their projects.
Put agent behavior scenarios and fixtures in `evaluations/`.
Define success criteria and cost limits with each scenario.

## Continuous integration

Start with complete verification across all projects.
Add affected-project selection when measured execution time requires it.
Include dependent projects and contract consumers in that selection.
Changes to shared tools require broader verification.

Pin CI actions and tool versions.
Test project templates through generated projects before other projects use them.
Add deployment configuration when a concrete deployment target exists.
