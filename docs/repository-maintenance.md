# Repository maintenance tooling

Invest first in tools that detect incorrect builds or reduce repeated maintenance work.
Keep implementation languages open until a tool needs an implementation.

## Invest now

| Tool | Reason | Current state |
| --- | --- | --- |
| Regression tests for repository scripts | Prevent false-success checks and filename-handling failures | Implemented as `//tooling/tests:repository-tools-test` |
| Stable CI result job | Keep the required status check stable as jobs grow | Implemented as `verify`; repository settings must require it |
| Pinned workflow and shell linting | Detect invalid workflows and shell mistakes in ordinary CI | Implemented as `//tooling/checks:lint` |
| Launcher consistency checks | Detect missing platforms, unpinned URLs, and Buck2/formatter release drift | Implemented as `//tooling/checks:tool-pins` |

Prefer maintained lint tools over custom parsers.
Pin their executables before adding CI targets.
Run the same targets locally and in CI.
Actionlint and ShellCheck are candidates for the existing YAML and Bash files.
See [Actionlint](https://github.com/rhysd/actionlint) and [ShellCheck](https://github.com/koalaman/shellcheck).

A launcher checker should compare the committed Buck2 and formatter manifests.
Check matching dated releases, required platform entries, digest fields, and executable permissions.
Do not download every platform binary during each repository check.
Keep the pinned launcher manifests as the source of truth.

The shared `./buck2 run //tooling:verify` command runs the audit, build, and test sequence locally and in CI.
See [tooling documentation](../tooling/README.md) for the directory layout and individual targets.
GitHub Actions receive weekly Dependabot update proposals.
See [the dependency audit](dependency-audit.md) for tool release checks.

## Add with the first project that needs it

| Tool | Trigger |
| --- | --- |
| Dependency metadata drift check | The first dependency-target generator arrives |
| Project README or interface checks | Real projects establish a stable documentation structure |
| Documentation link check | Documentation growth causes broken local references |
| Tool upgrade automation | Tool upgrades become recurring maintenance work |
| Ownership metadata | Multiple maintainers need review routing |

The dependency drift check must run the pinned generator and fail if generated metadata changes.
Use the relevant generator's check mode when it has one.
A second invocation should produce identical results.
Do not build a generic cross-language package manager.

Upgrade automation should propose a reviewable change and run complete verification.
It should update the executable and bundled-rule assumptions together.
GitHub Actions pins can use a maintained updater instead of a repository-specific bot.

## Defer until measured need

| Tool | Evidence required |
| --- | --- |
| Project generator | A tested integration has repeated users |
| Affected-target selection | Full CI latency requires it, and selection matches full-run outcomes |
| Test sharding | A real test suite dominates CI latency |
| Remote build cache or execution | Complete declared inputs and measured build cost |
| Repository dashboard | Existing CI and build reports cannot answer a concrete maintenance question |
| Broad build-rule abstraction | Multiple explicit consumers need the same behavior |

Keep a short upgrade and verification procedure before building maintenance automation.
Record build latency and maintenance failures as evidence for the next investment.
See the [scale review](buck2-scale-review.md) for dependency and execution requirements.
