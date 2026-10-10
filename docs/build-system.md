# Build with Buck2

Buck2 owns the repository build graph.
Use the committed `./buck2` launcher for all build commands.
Service designs and implementation languages remain open.

## Setup

Install Git, Bash, curl, tar, and a SHA-256 tool.
The bootstrap supports Linux and macOS on x86_64 and ARM64.

```sh
bash tooling/scripts/bootstrap.sh
export PATH="$PWD/.tools/bin:$PATH"
./buck2 run //tooling:verify
```

The bootstrap installs DotSlash 0.5.9 in the ignored `.tools/bin` directory.
It checks the downloaded archive against a pinned SHA-256 digest.
Add that directory to your shell PATH for later sessions.

The Buck2 and Starlark formatter manifests pin release `2026-10-01`.
DotSlash selects the platform binary and checks its digest.
Buck2 uses the prelude bundled with that binary.
No separate prelude checkout is required.
See [Buck2 installation](https://buck2.build/docs/getting_started/install/).

## Commands

| Command | Purpose |
| --- | --- |
| `./buck2 run //tooling:verify` | Audit, build, and test with the same command as CI |
| `./buck2 build //...` | Build all root-cell targets |
| `./buck2 test //...` | Run all root-cell tests |
| `./buck2 test //:check` | Check repository files |
| `./buck2 run //tooling:fmt` | Format Buck and Starlark files |
| `./buck2 audit visibility //... toolchains//...` | Check dependency boundaries across configurations |
| `./buck2 build //:scaffold --show-output` | Produce the scaffold source bundle |
| `./buck2 uquery 'deps(//:check)'` | Inspect the repository check dependencies |

The source bundle declares scaffold inputs. It is not a release artifact.
Future projects add targets such as `//services/<name>:<target>`.
The reconciliation library has Go build and consumer-test targets.
The factory entrypoint has Go build, ingress, and executable integration targets.

## Cells and execution

The root cell contains projects and repository tools.
The `toolchains` cell contains toolchain declarations.
The external `prelude` cell contains bundled rules.
Compatibility aliases in `.buckconfig` support those rules.

Builds use the bundled local execution platform.
Remote execution and shared remote caches are not configured.
Add target platforms when a project requires cross-compilation.

The repository check uses a small rule in `tooling/rules/repository_check.bzl`.
It runs in the checkout and reads Git metadata and untracked files.
Test execution caching is disabled for this check.
Its executor explicitly disables remote execution and remote caching.
It uses host Bash, Git, and DotSlash, so this check is not hermetic.

## Add a project

Follow [the new project guide](adding-a-project.md) for setup, target declarations, and acceptance checks.

1. Choose a purpose and a location under the repository conventions.
2. Choose the implementation language when the task requires one.
3. Add its pinned toolchain to the toolchains cell.
4. Declare source files, generated inputs, dependencies, and target visibility.
5. Add behavior checks and document the project's interface and commands.

Declare compiler, runtime, linker, and bootstrap distributions as build inputs when the rules require them.
Use narrow target visibility. Share targets with their explicit users.
Use public visibility for intentional external interfaces.

Keep dependency declarations in one authoritative form.
If a native manifest owns external dependency resolution, generate Buck targets from it.
Document the generation command and pin the generator.
Do not maintain both graphs by hand.

Complete each integration when its first project needs it.
Add templates only when they have real users.

Follow the [build integration guide](build-integration.md) for dependency generation, invalidation, editor support, and remote execution.

## CI and upgrades

CI runs the shared verification command on Ubuntu 26.04 for x86_64 and ARM64.
The bootstrap also supports macOS, but the current CI workflow has Linux coverage only.
The workflow bootstraps the pinned DotSlash runtime.
It audits target visibility and retains JSON build reports for 14 days.
Test build reports describe test artifacts, not individual test results.
Read test outcomes in the workflow logs.
The final `verify` job requires every platform job to pass.
Configure `verify` as the required status check in GitHub repository rules.
The workflow also supports merge queues through the `merge_group` event.

To upgrade Buck2, replace both launcher manifests from one dated release.
Review the platform digests, format the rules, and run builds and tests.
Check each existing language integration against the new bundled prelude.

To upgrade DotSlash, update its version and archive digests in `tooling/scripts/bootstrap.sh`.
Keep agent workspaces and execution artifacts outside the source tree.

See [repository tooling](../tooling/README.md) for individual checks and tool layout.
See [tool maintenance](../tooling/README.md#update-tools) for release sources and upgrade procedures.

## Go

The reconciliation library uses hermetic Go 1.27.2 distributions.
The toolchains cell pins archives and SHA-256 digests for Linux and macOS on x86_64 and ARM64.
CGo is disabled for this first library.
Go rule bootstrap scripts use pinned CPython 3.14.8 from python-build-standalone release 20261009.
Neither Go nor Python needs to be installed on the host.

The root target platform disables CGo for standalone libraries and test targets.
Use the formatting command in the library guide for project-owned Go files.
Each Go project owns its `go.mod` and `go.sum`. The root `go.work` enrolls projects
in one workspace. Go selects one external version per module for workspace builds.
`go work vendor` produces one root `vendor/` tree. Buck uses generated targets in
that tree. No project stores dependencies for another project.

Run these commands after a dependency change:

```sh
GOWORK=off ./buck2 run 'toolchains//:go[go]' -- -C PROJECT mod tidy
bash tooling/go/generate.sh
bash tooling/go/check.sh
```

The generator stages the workspace outside the checkout. It runs the pinned Go
vendor command and bundled gobuckify, then publishes the completed vendor tree.
The generator supplies a temporary, dependency-free `go.mod` because gobuckify
requires a module name to exclude this repository's packages. That file is not a
workspace member and does not declare or resolve dependencies.

The check verifies workspace membership, independently tidied module manifests,
vendor sources, and generated Buck targets. Native package loading also checks
that the standard workspace vendor tree works without Buck. Project race and
integration checks run with `GOWORK=off` to expose dependency errors that the
workspace could hide. Local replacements remain in project manifests so those
independent checks can resolve unpublished repository libraries.

Go tools and editors discover `go.work` from a project directory. For example:

```sh
./buck2 run 'toolchains//:go[go]' -- -C services/intake test ./internal/...
```

This uses workspace vendoring by default. Do not set a global `-mod=mod` override.
Use `GOWORK=off` and `-mod=readonly` for independent checks. Do not edit vendor
sources or generated Buck targets. See [Go workspace tooling](../tooling/go/README.md)
for project enrollment, version selection, and generator limits.
