# Add a project

Use this procedure for a service, application, shared library, or repository tool.
Run commands from the repository root.
Replace `services/your-project` with the project's actual path.

## 1. Define the purpose and location

Read [repository conventions](repository.md) and the [factory glossary](../CONTEXT.md).
Choose a name that describes the project's purpose.
Choose its language when the implementation requires one.

| Purpose | Location |
| --- | --- |
| Independently deployable process | `services/<name>/` |
| User application | `apps/<name>/` |
| Shared library with explicit consumers | `packages/<name>/` |
| Cross-language interface definition | `contracts/<name>/` |
| Repository development tool | `tooling/` |
| Agent behavior scenarios | `evaluations/<name>/` |

Create the directory when you add its implementation.
Keep projects in the root cell unless a concrete requirement needs another cell.
Add local `AGENTS.md` instructions only when project rules differ from root instructions.

## 2. Set up the build tools

```sh
bash tooling/scripts/bootstrap.sh
export PATH="$PWD/.tools/bin:$PATH"
```

Read the [build guide](build-system.md).
If the language has no configured toolchain, add it to `toolchains/` with this project.
Pin compilers, runtimes, linkers, generators, and bootstrap tools that the rules need.
Record host tools that remain required.

Complete the language-specific checks in the [build integration guide](build-integration.md#acceptance-checks).
Go is configured for the reconciliation library. Rust and TypeScript integrations remain open.

## 3. Declare targets and dependencies

Add a `BUCK` file beside the implementation.
Use the language rules selected for this project.
Declare each target's source files, generated inputs, dependencies, and outputs.
Declare build tools as execution dependencies where the rules require them.

Keep internal targets private.
Give shared targets explicit consumers through `visibility`.
Use `PACKAGE` files when repeated boundaries need a common declaration.
CLI access to a target does not require public visibility.

Keep external dependency resolution in one authoritative manifest and lockfile, where the ecosystem uses them.
Generate Buck dependency targets from that metadata.
Pin the generator and document its command.
Verify that regeneration produces no changes before CI accepts the integration.
Do not edit generated dependency targets by hand.

For generated code, declare the definition, generator, and generator configuration as action inputs.
Make consumers depend on the generated output target.
Document compatibility rules and ownership for cross-language contracts.

Use project-local source patterns.
Broad globs can include fixtures, caches, or unrelated files.
Review source discovery after you add or delete files.

## 4. Add behavior checks

Add actual test targets for the project's external behavior.
Include at least one incorrect-input or failure-path case where the interface supports it.
Connect test targets through the rules' `tests` attribute when appropriate.
Document type checks and lint targets if ordinary builds do not cover them.

```sh
project=services/your-project
./buck2 build "//$project/..."
./buck2 test "//$project/..."
```

Inspect the test summary. An exit code of zero with no tests does not prove behavior.
Introduce a deliberate behavior error in a disposable checkout.
Confirm that the corresponding test fails, then restore the implementation.

For projects with a runtime interface, document and execute a verification procedure:

1. Build and launch the actual artifact with isolated test configuration.
2. Check readiness and confirm that the instance uses the intended build.
3. Exercise a real operation through the public interface.
4. Inspect the result and observable side effects, such as files, records, or emitted messages.
5. Capture commands, outcomes, and evidence outside the source tree.
6. Stop owned processes and remove temporary state. Preserve the evidence.

Include a failure case where the interface supports it.
For libraries, exercise the public API through a consumer test.
Put repeatable verification steps in Buck targets.
Document required external systems and checks that cannot run locally.
Do not replace unavailable behavior checks with successful placeholders.

## 5. Write the project README

Include these details:

- Purpose, maintainer, and explicit consumers.
- External interface and configuration, with safe example values.
- Exact target labels for builds, tests, formatting, linting, and local development.
- Dependency-resolution and dependency-generation commands.
- Toolchain requirements, supported platforms, and artifact outputs.
- Data ownership, compatibility rules, and migrations when applicable.

State which tasks do not apply.
Do not add commands that report success without performing a check.
Keep agent workspaces, credentials, and execution artifacts outside the source tree.

## 6. Verify CI inclusion

Root-cell targets under the project enter the existing `//...` build and test scope.
That scope does not discover standalone tests in another cell.
If you add a cell, update all relevant build, test, and audit patterns in CI.

If a target supports only one platform, declare its compatibility constraints.
Update CI explicitly for the supported configurations.
Document excluded targets and the jobs that cover them.
Do not hide incompatibility through blanket skips.

```sh
./buck2 run //tooling:fmt
./buck2 run //tooling:verify
```

Run these checks from a fresh checkout with the documented setup.
Confirm that a source change rebuilds its consumers.
Confirm that dependency and generated-input changes rebuild the correct consumers.
Keep project-specific verification in Buck targets so local checks and CI use the same graph.

CI has a final `verify` job for repository rules.
The workflow file does not enable branch protection by itself.
Ask the repository maintainer to require this check through GitHub repository settings when needed.

## Before review

Confirm the following:

- The project has real contents and a documented interface.
- Its toolchain and dependency-generation path are pinned and repeatable.
- Its build produces the documented artifact.
- Its behavior tests run and detect a deliberate error.
- Its dependencies obey visibility boundaries.
- All supported platforms have explicit CI coverage.
- Its README lets another developer reproduce the checks.

Add a project template only after this integration has a real user.
Test future templates through generated projects.

For a coordination store adapter, see [adding a datastore](adding-a-datastore.md).
