# Dependency release audit

Checked upstream releases on 2026-10-09.

| Dependency | Selected release | Source of truth |
| --- | --- | --- |
| actions/checkout | [v7.0.1](https://github.com/actions/checkout/releases/tag/v7.0.1) | Commit SHA in `.github/workflows/verify.yml` |
| actions/upload-artifact | [v7.0.2](https://github.com/actions/upload-artifact/releases/tag/v7.0.2) | Commit SHA in `.github/workflows/verify.yml` |
| Buck2 and Starlark formatter | [2026-10-01](https://github.com/facebook/buck2/releases/tag/2026-10-01) | `buck2` and `tooling/bin/starlark-fmt` |
| DotSlash | [v0.5.9](https://github.com/facebook/dotslash/releases/tag/v0.5.9) | Version and archive digests in `tooling/scripts/bootstrap.sh` |
| Actionlint | [v1.7.12](https://github.com/rhysd/actionlint/releases/tag/v1.7.12) | `tooling/bin/actionlint` |
| ShellCheck | [v0.11.0](https://github.com/koalaman/shellcheck/releases/tag/v0.11.0) | `tooling/bin/shellcheck` |
| jq | [jq-1.8.2](https://github.com/jqlang/jq/releases/tag/jq-1.8.2) | `tooling/bin/jq` |

The actions and standalone tools use their latest stable releases at the audit date.
Buck2 publishes dated prereleases and a moving `latest` snapshot.
The selected pair is the newest dated release.
The moving snapshot can contain newer commits, but its download URLs do not identify one immutable version.
The bundled prelude updates with Buck2.

CI uses the supported [Ubuntu 26.04 runner](https://github.blog/changelog/2026-09-17-ubuntu-26-generally-available-and-latest-migration/).
The current workflow has Linux coverage only.
The tool launchers also support macOS on x86_64 and ARM64.
Git, Bash, curl, tar, and SHA-256 tools remain host prerequisites.
The repository has no application package dependencies yet.
The earlier disposable language experiment has separate historical versions in its report.

## Keep dependencies current

Dependabot checks GitHub Actions weekly through `.github/dependabot.yml`.
It proposes changes for review rather than changing the default branch directly.
DotSlash tool manifests and bootstrap archives require a separate upstream release check.

1. Check the upstream release pages above.
2. Select the latest stable release, or the latest dated Buck2 release.
3. Resolve action release tags to full commit SHAs.
4. Inspect action runtime requirements and changed inputs.
5. Update tool manifests from upstream release assets and verify their digests.
6. Update every supported platform entry in the same change.
7. For Buck2, update its formatter from the same dated release.
8. For DotSlash, update the bootstrap version and every archive digest.
9. Run `./buck2 run //tooling:fmt` and `./buck2 run //tooling:verify`.
10. Test bootstrap and verification from a fresh checkout.

Use release asset checksums or digest metadata from the upstream repository.
The launcher check validates local consistency and pin structure.
It does not establish that upstream has no newer release.
Update this audit after a release check.
