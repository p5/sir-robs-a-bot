# Buck2 scale review

Reviewed on 2026-10-09.

Keep the current Buck2 foundation.
Its pinned executable, bundled prelude, and separate toolchains cell match established public patterns.
Success now depends on complete language integrations and accurate dependency declarations.
The current scaffold proves repository checks, not application builds at scale.

## Evidence from existing users

Meta reports production use across its large, multi-language monorepo.
Its experience establishes that Buck2 can scale.
It does not establish that this repository has equivalent build rules or infrastructure.
See [Meta's introduction](https://engineering.fb.com/2023/04/06/open-source/buck2-open-source-large-scale-build-system/).

| Project | Observed practice | Application here |
| --- | --- | --- |
| Buck2 itself | DotSlash bootstrap and Reindeer-generated crate targets | Keep executable pins and generated external dependency metadata |
| Antlir | Bundled prelude, separate toolchains, explicit host execution platform | Keep cells small and define platforms when concrete builds require them |
| Antlir CI | BXL graph analysis and deterministic test sharding, with explicit exclusions | Check graph correctness before selective CI; document exclusions |
| Rust compiler bootstrap | Stage-specific compiler and sysroot targets, platform constraints, and Reindeer configuration | Model tools as dependencies; keep execution and target platforms distinct |

Buck2 documents its [bootstrap and dependency generation](https://buck2.build/docs/about/bootstrapping/).
Antlir's [configuration](https://github.com/facebookincubator/antlir/blob/05b53c864e7a5ec8eec82b7c4a9e53735d135866/.buckconfig)
uses the same bundled-prelude pattern as this repository.
Its [graph check](https://github.com/facebookincubator/antlir/blob/05b53c864e7a5ec8eec82b7c4a9e53735d135866/ci/test_target_graph.bxl)
performs configured analysis, with a documented platform-related exclusion.
Its [test selector](https://github.com/facebookincubator/antlir/blob/05b53c864e7a5ec8eec82b7c4a9e53735d135866/ci/find_tests.bxl)
assigns shards from target-label hashes.

The compiler bootstrap has explicit [toolchain dependencies](https://github.com/dtolnay/buck2-rustc-bootstrap/blob/83bea6a2e6daa656d58aa29290a89e78c0759186/toolchains/BUCK)
and [Reindeer configuration](https://github.com/dtolnay/buck2-rustc-bootstrap/blob/83bea6a2e6daa656d58aa29290a89e78c0759186/reindeer.toml).
Its [README](https://github.com/dtolnay/buck2-rustc-bootstrap) reports build comparisons for the Rust compiler.
Treat those measurements as project-specific evidence, not expected factory performance.
This is a substantial codebase example, not evidence of broad organizational deployment.

These observations support the recommendations below.
We did not build those upstream projects during this review.

## Changes from this review

- The Git-based repository test has an explicit local executor.
- That executor disables remote execution and remote caching.
- CI audits visibility across the root and toolchains cells.
- CI retains JSON build reports for 14 days.
- Repository formatting and checks include BXL files.

Buck2 supports [test executor configuration](https://buck2.build/docs/rule_authors/test_execution/).
A checkout-dependent check must keep its local execution requirement when other targets gain remote execution.

The [visibility audit](https://buck2.build/docs/concepts/visibility/)
checks dependency boundaries without selecting only one configured dependency branch.
The [build report](https://buck2.build/docs/users/build_observability/build_report/)
records artifact build outcomes. It does not replace test-result reporting.

## Requirements for each language integration

Complete these checks with the first real project in each language.
Keep the evidence with that project's build documentation.

1. Build and test from a fresh checkout with the pinned toolchain.
2. Include an external dependency and a generated input used by the project.
3. Regenerate dependency targets twice. Confirm identical output and no metadata drift.
4. Change source, dependency metadata, generated input, and compiler configuration separately.
5. Confirm the required consumers rebuild. Confirm unrelated projects do not rebuild.
6. Add and delete a source file. Confirm target discovery and invalidation remain correct.
7. Introduce an incorrect result. Confirm a behavior test fails.
8. Check the required target platforms and record incompatible or skipped targets.
9. Verify editor navigation, diagnostics, and native package-tool compatibility.

Use the smallest dependency targets that preserve correct semantics.
Keep custom language rules beside their owning integration until they have explicit shared users.
Use bundled rules where they meet project requirements.
Do not hide an entire native build behind one action and claim fine-grained dependency tracking.

### Go

Evaluate the bundled hermetic Go toolchains and dependency generator.
Include Go bootstrap tools and Python bootstrap dependencies.
If the project uses CGo, include its C/C++ toolchain and linker.
Test editor integration through the packages driver.
See [toolchains](https://buck2.build/docs/users/languages/go/toolchains/),
[dependency generation](https://buck2.build/docs/users/languages/go/third_party_packages/),
and [editor integration](https://buck2.build/docs/users/languages/go/gopackagesdriver/).

### Rust

Evaluate Reindeer against the project's Cargo manifest and lockfile.
Pin the generator. Document dependency fixups beside generated metadata.
Check procedural macros, build scripts, native libraries, and enabled features when the project uses them.
Verify rust-analyzer support before the integration becomes a project template.
See [Reindeer](https://github.com/facebookincubator/reindeer).

### TypeScript

Budget for package-manager integration, compiler rules, bundler rules, and test rules.
The [published ecosystem table](https://buck2.build/docs/about/language_support/) does not list TypeScript.
The local pilot's Node.js rules and tracked actions do not establish a complete TypeScript integration.

Choose the package manager with the first project.
Use its lockfile as the dependency resolution source.
Verify workspace imports, generated sources, editor behavior, and any browser or server packaging the project needs.
Replace the pilot's whole-directory npm input with generated dependency targets before claiming selective package invalidation.

## Boundaries and CI as the repository grows

Keep projects in the root cell unless a concrete requirement needs another cell.
A cell has its own namespace and configuration boundary.
A new cell does not automatically enter the root-cell `//...` CI scope.
Update CI target patterns when adding cells.

Keep target visibility private by default.
Use explicit consumers for shared libraries.
Use `PACKAGE` files for repeated module boundaries when they become necessary.
See [package inheritance](https://buck2.build/docs/rule_authors/package_files/).

Continue complete CI until measurements justify selection or sharding.
Record clean-build time, unchanged-build time, changed-target time, and the critical path.
Include test outcomes and skipped targets in that review.

If CI becomes selective, evaluate [buck2-change-detector](https://github.com/facebookincubator/buck2-change-detector).
Validate changes to BUCK files, Starlark rules, toolchains, lockfiles, deleted sources, and generated contracts.
Include transitive consumers and their tests.
Compare selected CI against full CI before relying on it.
Do not select tests only by rule names. Our repository test uses a custom rule.

## Remote execution and caching

Add remote infrastructure after a real project's actions have complete declared inputs.
Pin compilers, runtimes, linkers, generators, and platform images.
Keep network dependency restoration explicit and checksum-verified.
Declare relevant environment values through action configuration.
Local builds can accidentally read undeclared host files.
A successful local build alone does not establish hermeticity.
See [Buck2's hermeticity qualification](https://github.com/facebook/buck2).

Before enabling a remote backend, check its protocol, digest algorithm, platform properties, and cache policy.
Test local and remote results against the same declared inputs.
Verify artifact retention before enabling deferred materialization.
Buck2 documents [remote execution](https://buck2.build/docs/users/remote_execution/)
and [artifact-expiry requirements](https://buck2.build/docs/users/advanced/deferred_materialization/).
Keep the repository check local under every build configuration.

The factory will execute agent-authored changes.
Keep untrusted jobs separate from cache-upload credentials and trusted release builds.
Build execution does not provide agent-job isolation.

## Maintenance

Review the executable, bundled prelude, and language integrations together during upgrades.
Use local configuration only for developer preferences, not required project behavior.
Add Watchman only if file-watching measurements justify it.
Do not require a service that fresh CI runners do not need.

Inspect failed actions with `./buck2 log what-failed` immediately after the failed invocation.
Use `./buck2 log critical-path` to investigate build latency.
See [logging](https://buck2.build/docs/users/build_observability/logging/).
Keep build reports and execution logs outside tracked source files.

No application modules, service names, or implementation languages changed during this review.
