# sir-robs-a-bot

A distributed agent software factory for software development and agent-powered applications.
Both use a shared agent execution platform.

This repository contains the factory source and shared project conventions.
The controller and its reconcilers use Go.
Service names, module designs, and languages for other projects remain open.

Read [the architecture](docs/architecture.md) for the intended factory core.
Read [repository conventions](docs/repository.md) before you add a project.
Read [the glossary](CONTEXT.md) for the factory terms.
Read [the build guide](docs/build-system.md) for setup and project integration.
Follow [the new project guide](docs/adding-a-project.md) when you add a project.
See [repository tooling](tooling/README.md) for checks and maintenance procedures.

## Start development

Install the pinned launcher runtime, then run the same checks as CI:

```sh
bash tooling/scripts/bootstrap.sh
export PATH="$PWD/.tools/bin:$PATH"
./buck2 run //tooling:verify
```

The scaffold checks shell syntax, whitespace, Starlark formatting, workflows, and tool pins.
Run `./buck2 run //tooling:fmt` to apply Starlark formatting.
The [reconciliation library](packages/reconcile/README.md) uses the pinned Go toolchain.
The [factory entrypoint](services/intake/README.md) accepts GitHub and GitLab mentions
as durable requests through polling and signed webhooks. It includes a read-only
probe for live mention discovery.
The [resource library](packages/resources/README.md) separates immutable content,
versioned application state, and recoverable queue delivery. Its request-to-run
prototype exercises S3 and DynamoDB adapters. Intake supports
S3 and DynamoDB by default, with independent factory dispatch and recoverable
receipts.
Add other language toolchains with their first projects.

## Repository layout

| Directory | Contents |
| --- | --- |
| `services/` | Independently deployable processes |
| `apps/` | User applications |
| `packages/` | Shared libraries with explicit users |
| `contracts/` | Cross-language interface definitions |
| `tooling/` | Repository checks and development tools |
| `vendor/` | Generated external Go sources and Buck targets |
| `toolchains/` | Build toolchains, added as projects require them |
| `templates/` | Project templates, added with their first real users |
| `evaluations/` | Agent task scenarios and fixtures |
| `deploy/` | Deployment configuration, added when needed |
| `docs/` | Repository conventions, decisions, and runbooks |

Create these directories when their first projects arrive.
Organize projects by purpose. Choose the language inside each project.
Keep repositories that agents modify outside this source tree.
