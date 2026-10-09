# Build integration

Add language toolchains and dependency generators with their first real projects.
Use bundled Buck2 rules where they meet project requirements.
Keep custom rules with their owning integration until they have explicit shared users.

## Acceptance checks

Complete these checks before other projects depend on a language integration.
Keep project-specific commands in its README.

1. Build and test from a fresh checkout with pinned tools.
2. Include an external dependency and a generated input used by the project.
3. Regenerate dependency targets twice. Confirm identical output and no metadata drift.
4. Change source, dependency metadata, generated input, and compiler configuration separately.
5. Confirm that consumers rebuild and unrelated projects do not rebuild.
6. Add and delete source files. Confirm correct target discovery and invalidation.
7. Introduce an incorrect result. Confirm that a behavior test fails.
8. Check supported platforms and document incompatible targets.
9. Verify editor navigation, diagnostics, and native package-tool compatibility.

Keep dependency resolution in one authoritative manifest and lockfile.
Generate Buck targets from that metadata instead of maintaining a second dependency graph.
Declare compilers, runtimes, linkers, generators, and required configuration as action inputs.
Use dependency targets small enough to preserve useful invalidation boundaries.

## Language considerations

### Go

Evaluate the bundled Go toolchains and dependency generator.
Include bootstrap tools and any Python dependencies required by the rules.
For CGo projects, include the C/C++ compiler and linker.
Check editor integration through the packages driver.
See [toolchains](https://buck2.build/docs/users/languages/go/toolchains/),
[dependency generation](https://buck2.build/docs/users/languages/go/third_party_packages/),
and [editor integration](https://buck2.build/docs/users/languages/go/gopackagesdriver/).

### Rust

Evaluate [Reindeer](https://github.com/facebookincubator/reindeer) against the project's Cargo manifest and lockfile.
Pin the generator and document dependency fixups beside generated metadata.
Check procedural macros, build scripts, native libraries, and enabled features when used.
Verify rust-analyzer support before creating a project template.

### TypeScript

Select a package manager with the first project and use its lockfile for dependency resolution.
Provide compiler, bundler, packaging, and test actions as required by the application.
Generate package dependency targets rather than treating the complete installed dependency tree as one input.
Verify workspace imports, generated sources, editor behavior, and browser or server packaging.

## Build boundaries and CI

Keep projects in the root cell unless a concrete requirement needs another cell.
A new cell does not automatically enter the root-cell `//...` CI scope.
Update build, test, and visibility audit patterns when adding cells.

Keep targets private by default and declare explicit consumers for shared libraries.
Use [PACKAGE files](https://buck2.build/docs/rule_authors/package_files/) when repeated boundaries need common declarations.

Keep complete CI until measured latency requires selection or sharding.
For selective CI, evaluate [buck2-change-detector](https://github.com/facebookincubator/buck2-change-detector).
Include transitive consumers, deleted sources, generated contracts, build rules, toolchains, and lockfiles.
Compare selected tests against complete CI before relying on selection.

## Remote execution and caches

Add remote infrastructure after real project actions have complete declared inputs.
A successful local build does not prove hermeticity.
Check protocol support, digest algorithms, platform properties, cache access, and artifact retention.
Compare local and remote outputs for the same inputs.
See [remote execution](https://buck2.build/docs/users/remote_execution/)
and [deferred materialization](https://buck2.build/docs/users/advanced/deferred_materialization/).

Keep checkout-dependent repository checks local with remote execution and test caching disabled.
Keep untrusted agent jobs separate from cache-upload credentials and trusted release builds.
Build execution does not provide agent-job isolation.

## Upstream examples

These projects provide examples of explicit toolchains, dependency generation, and build graph analysis:

- [Buck2 bootstrap](https://buck2.build/docs/about/bootstrapping/) documents its compiler bootstrap and generated dependency metadata.
- [Antlir](https://github.com/facebookincubator/antlir) demonstrates bundled rules, platform configuration, and CI graph analysis.
- [Rust compiler bootstrap](https://github.com/dtolnay/buck2-rustc-bootstrap) demonstrates compiler stages, sysroots, and Reindeer configuration.

Use these as implementation references. Their build performance does not predict this repository's performance.
