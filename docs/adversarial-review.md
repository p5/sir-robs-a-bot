# Scaffold adversarial review

Reviewed on 2026-10-09.
The review covered repository scripts, Buck targets, CI, and project onboarding.

## Findings and changes

| Priority | Finding | Resolution |
| --- | --- | --- |
| High | Git discovery failures produced successful repository checks | Replaced process substitution with pipelines under `pipefail` |
| Medium | Root filenames that start with a hyphen could become command options | Added option termination for Bash and explicit relative paths for the formatter |
| Medium | CI lacked one stable required result across platform jobs | Added the final `verify` job; it accepts only a successful matrix result |
| Medium | Merge-queue events did not trigger verification | Added `merge_group` support |
| Medium | Project setup required readers to assemble several documents | Added the linked new project guide |

The discovery failure was reproducible before the fix.
A Git wrapper returned status 42 for `ls-files`.
Both scripts still returned status zero, and the verifier printed its success message.
The pipeline now propagates the discovery failure.

The new `//tooling/tests:repository-tools-test` target covers these behaviors:

- A valid checkout passes.
- Invalid shell syntax fails.
- Untracked and staged whitespace errors fail.
- Unformatted BXL files fail, then pass after formatting.
- Invalid Starlark syntax fails.
- Spaces and leading hyphens in filenames do not become command options.
- Git discovery failures fail both scripts.

The tests create disposable Git fixtures outside the source tree.
They invoke the actual repository scripts and pinned formatter.
They do not require a language toolchain.

## Verification evidence

A fresh source checkout passed bootstrap, visibility audit, build, and both scaffold test targets.
Restoring the original verifier bug in a disposable checkout caused the regression target to fail.

A disposable project passed the new guide's project-specific build and test commands.
The full repository test command discovered its behavior test.
Changing its input to an incorrect value caused the full test run to fail.
The disposable project did not change the repository's application design.

Actionlint 1.7.7 accepted the workflow.
A local check of the final gate accepted success and rejected failure, cancellation, skipped, and empty results.
Local Markdown file references passed a link check.

## Remaining limits

Hosted GitHub Actions and macOS execution were not run during this review.
Local checks do not prove the hosted matrix works.
The final gate's Bash condition was tested locally, not through GitHub's scheduler.

The workflow does not configure repository protection rules.
A maintainer must require `verify` through GitHub settings.
See [required status checks](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches).

No application language toolchains exist yet.
The two scaffold test targets verify repository maintenance behavior.
They do not prove application behavior or hermetic application builds.

The initial review used Actionlint with ShellCheck integration disabled.
The repository now pins both linters and runs them through `//tooling/checks:lint`.
It also checks launcher consistency and tests rejection of invalid pins.
See [repository tooling](../tooling/README.md) for the current five test targets.
