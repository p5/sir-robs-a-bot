# Repository tooling

Run all repository verification from the repository root:

```sh
bash tooling/scripts/bootstrap.sh
export PATH="$PWD/.tools/bin:$PATH"
./buck2 run //tooling:verify
```

The verifier audits visibility, builds all root-cell targets, and runs all root-cell tests.
CI invokes this same command.
Set `BUCK2_REPORT_DIR` to retain build reports outside the source tree.

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
Do not reuse this rule for hermetic application tests.

The launcher validator checks local consistency, not upstream release freshness.
It does not download binaries for platforms other than the host.
See [the dependency audit](../docs/dependency-audit.md) for release sources and update instructions.

Actionlint's runner metadata predates Ubuntu 26.04.
The narrow entry in `.github/actionlint.yaml` permits that verified GitHub-hosted label.
Remove the entry when a future Actionlint release recognizes it.

## Change a check

Add its implementation under `checks/` and its Buck target in that directory.
Declare the implementation and launchers in the target resources.
For checkout-dependent checks, use the existing scaffold resource bundle.
Add a regression case when a failure could produce a false success.
Run the full verifier after changes to shared rules or tools.
