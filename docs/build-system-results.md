# Buck2 evaluation results

Date: 2026-10-09.

Buck2 passed the local build experiment across Go, Rust, and TypeScript.
The repository now uses Buck2 after this evaluation.
See [the build guide](build-system.md) for the current scaffold.
Complete language dependency integration and toolchain pinning as projects require them.

The examples remained outside the repository.
They do not select application services, module designs, or implementation languages.

## Scope

The experiment ran on Linux x86_64.
Each language example used an external dependency and source generated from one shared JSON definition.

| Language | External dependency | Buck2 integration |
| --- | --- | --- |
| Go | `github.com/google/uuid` v1.6.0 | Native `go_library` and `go_test` rules |
| Rust | `itoa` 1.0.15 | Native `rust_library` and `rust_test` rules |
| TypeScript | `is-number` 7.0.0 | Tracked compiler and bundler actions, then `nodejs_test` |

The shared definition generated a Go package, a Rust crate, and a TypeScript source file.
This tested generated-source dependencies, not schema validation or interface compatibility.

## Results

| Check | Observed result |
| --- | --- |
| Fresh checkout | Build and all three test targets passed after `npm ci` |
| Unchanged build | No build actions ran |
| Go source edit | Go actions ran; Rust and TypeScript actions did not run |
| Go dependency edit | Dependency actions ran; Rust and TypeScript actions did not run |
| Shared definition edit | All three language consumers rebuilt |
| Incorrect shared value | All three test targets failed |
| Invalid TypeScript assignment | The type check failed with `TS2322` |
| Restored sources | Build and all three test targets passed |
| Reverse dependency query | Buck2 returned the generated Go package and its consumers |
| Generated Go dependency target | `gobuckify` generated the UUID target; the build and consumer tests passed |
| Native tools | `go test`, `cargo test`, TypeScript checks, bundling, and JavaScript tests passed |

The dependency edit changed a source comment.
Buck2 rebuilt the dependency and reused unchanged downstream outputs.
The shared definition edit changed behavior, so every language test detected the incorrect result.

## Local timings

These are single observations for tiny examples, not a comparison with native tool performance.
The clean build included compiler download and bootstrap work.
The timings exclude npm installation.

| Operation | Wall time |
| --- | --- |
| Clean build from fresh checkout | 11.95 seconds |
| Unchanged build | 0.075 seconds |
| Go source comment edit | 0.088 seconds |
| Go dependency comment edit | 0.115 seconds |
| Shared definition edit | 1.118 seconds |

The clean build ran 501 local commands and downloaded 57 MiB.
No remote execution or shared remote cache took part.

## Integration findings

### Go

`gobuckify` generated dependency targets from a vendored module declared in `go.mod`.
This provides a tested route to generated dependency metadata.
See [third-party package integration](https://buck2.build/docs/users/languages/go/third_party_packages/).

The upstream example pinned Go 1.24.4.
The current bundled generator used `sync.WaitGroup.Go`, which requires Go 1.25 or later.
Updating the disposable Linux toolchain to Go 1.25.1 resolved the failure.
Pin and test the Buck2 binary, bundled rules, and compiler together.

The Go tests used `cgo_enabled = False`.
CGo support remains unverified.

### Rust

Stable Rust 1.90.0 worked with `nightly_features = False` in the system toolchain rule.
The experiment used a manually declared target for one external crate.

Reindeer provides dependency-target generation, but this experiment did not run it.
See [Reindeer](https://github.com/facebookincubator/reindeer).
Dependency generation, procedural macros, and build scripts remain unverified.

### TypeScript

The bundled rules included Node.js package and test support.
Those rules required TypeScript compilation before JavaScript packaging.
See [the bundled Node.js rules](https://github.com/facebook/buck2/blob/d3c14c494ad105f811729e1f25c9fced1dd7f50d/prelude/decls/nodejs_rules.bzl).

The prototype used TypeScript 5.9.3 and esbuild 0.25.10.
It declared the installed npm dependency tree as an input to each compiler or bundler action.
This gave correct local invalidation, but it treated that tree as one large dependency.

Package restoration ran outside Buck2 with `npm ci --ignore-scripts`.
The experiment did not test pnpm, browser applications, or package installation inside the build graph.

Buck2 stages source files through symlinks.
The bundle command needed esbuild's `--preserve-symlinks` option to resolve generated source beside staged source.
The npm directory also needed its conventional `node_modules` name for package resolution.

## Future language integration

1. Add reusable TypeScript rules for compiler, bundler, and test actions.
2. Generate npm dependency targets from a lockfile instead of declaring the whole installed tree.
3. Test Rust dependency generation from Cargo metadata.
4. Pin Rust, Node.js, bootstrap Python, and linker distributions as build inputs.
5. Test the required editor integrations and target platforms.
6. Add each project's build and test targets to the common CI checks.

The pilot used a downloaded Go distribution.
Rust, Node.js, the Python virtual environment, and the linker remained local tools.
Passing this pilot does not establish fully hermetic builds.

Go offers a packages driver for editor integration.
Tools that directly call `go list` or `go build` need separate compatibility checks.
See [Go tools integration](https://buck2.build/docs/users/languages/go/gopackagesdriver/).

Buck2 remote execution can follow after local builds work.
Build execution does not replace the factory's agent execution platform.

## Evaluation artifacts

The following paths contain temporary review artifacts:

- `/tmp/sir-robs-buck-pilot.tar.gz`: source snapshot without build outputs or npm dependencies.
- `/tmp/sir-robs-buck-fresh/`: fresh checkout used for the final checks.
- `/tmp/sir-robs-run-evaluation.cjs`: build, invalidation, and failure probes.
- `/tmp/sir-robs-evaluation-results.json`: command statuses and timings.
- `/tmp/sir-robs-*.log`: build, test, and action records.

Temporary files can disappear when the host clears `/tmp`.
This report preserves the findings in the repository.

The Buck2 snapshot came from the `latest` release on 2026-10-09.
The release published prelude revision `d3c14c494ad105f811729e1f25c9fced1dd7f50d`.
Its compressed Linux musl binary had SHA-256:

```text
dab735d6db37738f4ff0de3063d238ddac3d18d09955076931052d33454a8cec
```

The source snapshot archive had SHA-256:

```text
a8484f529b8e229ffe0527119a8b966d3edb3879dc06da60d50cbd13804d0b02
```
