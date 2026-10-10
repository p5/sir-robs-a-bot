# Build toolchains

This cell pins Go 1.27.2 and CPython 3.14.8 bootstrap distributions.
Archive digests and platform selection live in `BUCK`.
Supported hosts are Linux and macOS on x86_64 and ARM64.
CGo is disabled for the reconciliation library, so no C/C++ toolchain is configured.
The test toolchains supply the bundled rules' test metadata; execution remains local.

Go distributions come from go.dev. CPython bootstrap distributions come from python-build-standalone release 20261009.
Go build actions declare both distributions as inputs. They do not use host compilers or Python.
Repository checks still use host Bash, Git, and DotSlash.

Use `./buck2 run toolchains//:gofmt -- -w packages/reconcile` to format Go files.
Add other language toolchains with their first concrete projects.
Follow the [build integration guide](../docs/build-integration.md) for acceptance checks.
