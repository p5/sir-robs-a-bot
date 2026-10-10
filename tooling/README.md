# Repository tooling

Run all repository verification from the repository root:

```sh
bash tooling/scripts/bootstrap.sh
export PATH="$PWD/.tools/bin:$PATH"
./buck2 run //tooling:verify
```

The verifier audits visibility, builds all root-cell targets, and runs all root-cell tests.
CI invokes this same command.
Set `BUCK2_REPORT_DIR` to retain reports outside the source tree.
Each invocation creates a separate `verification.*` directory and prints its path.
The directory contains Buck build reports and `verification.json`.
The summary records the revision, Git worktree status, start time, commands, exit codes, and overall outcome.
A failed command stops verification and produces a failed outcome.
A running report is incomplete. Missing steps do not count as passes.
Git worktree status lists changed paths. It does not preserve their contents.
Follow [agent work](../docs/agent-work.md) when you need evidence for uncommitted changes.
Report setup failures may leave no summary. The command exit status remains authoritative.

## Layout

| Directory | Responsibility |
| --- | --- |
| `bin/` | Pinned DotSlash tool manifests |
| `checks/` | Repository validation and lint targets |
| `rules/` | Buck rules for checkout-dependent checks |
| `scripts/` | Bootstrap, formatting, and verification entry points |
| `tests/` | Regression tests using disposable Git checkouts |

Each directory owns its `BUCK` file.
The top-level `fmt` and `verify` aliases provide stable public commands.
Keep language-specific tooling with its owning project until other projects need it.

## Individual checks

| Command | Checks |
| --- | --- |
| `./buck2 test //:check` | Whitespace, Bash syntax, and Starlark formatting |
| `./buck2 test //tooling/checks:lint` | ShellCheck and Actionlint, including shell commands in workflows |
| `./buck2 test //tooling/checks:tool-pins` | Executable manifests, platform coverage, digests, pinned URLs, and paired Buck2 releases |
| `./buck2 test //tooling/tests/...` | Regression tests for repository scripts and launcher validation |
| `./buck2 run //tooling:fmt` | Apply Starlark formatting |

Checks use Bash and Git from the host.
DotSlash downloads and verifies pinned tool binaries on first use.
The JSON validator uses the pinned jq launcher. No system jq installation is required.
Python workflow linting is disabled because the repository has no Python workflow commands.
Repository checks run locally and disable test execution caching.
They read checkout contents and Git state.
Vendored Go sources retain upstream whitespace and scripts; repository lint does
not rewrite or lint them. Generated vendor Buck files still use repository
formatting. The dependency drift check verifies all vendored files against the
pinned module graph.
Do not reuse this rule for hermetic application tests.

The launcher validator checks local consistency, not upstream release freshness.
It does not download binaries for platforms other than the host.
See [Update tools](#update-tools) for release sources and update instructions.

Actionlint's runner metadata predates Ubuntu 26.04.
The entries in `.github/actionlint.yaml` permit the x86_64 and ARM64 GitHub-hosted runner labels.
Remove these entries when a future Actionlint release recognizes the labels.

## Change a check

Add its implementation under `checks/` and its Buck target in that directory.
Declare the implementation and launchers in the target resources.
For checkout-dependent checks, use the existing scaffold resource bundle.
Add a regression case when a failure could produce a false success.
Run the full verifier after changes to shared rules or tools.

## Update tools

Keep tool versions and digests in their executable manifests or bootstrap script.
Keep Action commit pins in `.github/workflows/`.
Dependabot proposes weekly Action updates. Tool manifests require a separate upstream release check.

| Tool | Release source | Pin location |
| --- | --- | --- |
| Buck2 and formatter | [Buck2 releases](https://github.com/facebook/buck2/releases) | `buck2`, `tooling/bin/starlark-fmt` |
| DotSlash | [DotSlash releases](https://github.com/facebook/dotslash/releases) | `tooling/scripts/bootstrap.sh` |
| Actionlint | [Actionlint releases](https://github.com/rhysd/actionlint/releases) | `tooling/bin/actionlint` |
| ShellCheck | [ShellCheck releases](https://github.com/koalaman/shellcheck/releases) | `tooling/bin/shellcheck` |
| jq | [jq releases](https://github.com/jqlang/jq/releases) | `tooling/bin/jq` |
| GitHub Actions | The action repository's release page | `.github/workflows/` |

1. Select the latest stable tool release, or a dated Buck2 release.
2. Resolve Action release tags to full commit SHAs.
3. Check runtime requirements and changed inputs.
4. Update manifests from upstream assets and verify their digests.
5. Update every supported platform entry in the same change.
6. Update Buck2 and its formatter from the same dated release.
7. For DotSlash, update the bootstrap version and every archive digest.
8. Run `./buck2 run //tooling:fmt` and `./buck2 run //tooling:verify`.
9. Test bootstrap and verification from a fresh checkout.

Use upstream checksums or release asset digest metadata.
Avoid moving snapshot URLs for pinned tools.

## Add maintenance tools when needed

Add dependency metadata checks with the first dependency generator.
Add ownership routing when multiple maintainers need it.
Create project templates only after an integration has repeated users.
Add upgrade automation, selective CI, sharding, or remote caches when measured work justifies them.
Keep repository settings management and maintenance reconcilers separate from the current scaffold.
