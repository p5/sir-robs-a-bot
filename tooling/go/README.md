# Go workspace tooling

Each project owns its Go module. The root `go.work` lists the repository's Go
projects. Go workspace vendoring produces one root `vendor/` directory. Buck
builds use generated targets in that directory.

The repository maintainer owns these tools. They use the pinned Go toolchain,
bundled gobuckify, and repository formatter. They need no host Go installation
or Python projection script.

## Add a Go project

1. Create its module under the project's purpose-based directory. Use a module
   path below `github.com/p5/sir-robs-a-bot/`.
2. Declare dependencies in its `go.mod`. For unpublished repository libraries,
   add local replacements so independent module checks can resolve them.
3. Enroll it from the repository root:

   ```sh
   ./buck2 run 'toolchains//:go[go]' -- work use ./services/PROJECT
   ```

4. Tidy the module independently, then regenerate dependencies:

   ```sh
   GOWORK=off ./buck2 run 'toolchains//:go[go]' -- -C services/PROJECT mod tidy
   bash tooling/go/generate.sh
   ```

5. Declare project Buck targets. Reference external packages through
   `//vendor/IMPORT_PATH:PACKAGE_NAME`. Add meaningful project tests and its
   verification procedure, including independent module checks, to the verifier.
6. Run `bash tooling/go/check.sh` and `./buck2 run //tooling:verify`.

Commit module metadata, `go.work`, `go.work.sum` when Go creates it, and the
regenerated vendor tree together. Do not add another project-level vendor tree.
The membership check compares `go.work` with tracked and untracked module files.
Remove a project's workspace entry when removing its module.

## Update dependencies

Use the pinned Go command in the owning project, then tidy with `GOWORK=off`.
Run `bash tooling/go/generate.sh` once for the whole workspace. Review module
metadata and generated target changes. Vendor code remains byte-for-byte upstream.
GitHub collapses vendor diffs through `.gitattributes`.

Workspace builds select one version of each external module. A project can
therefore build with a newer dependency than its own manifest selects alone.
Independent project checks expose hidden dependencies and version assumptions.
Keep local replacements until the internal libraries have usable published
versions. Do not run `go work sync` as routine generation: it can change project
requirements to match the workspace selection.

## Generation and verification

`generate.sh` stages project sources outside the checkout. It runs `go work
vendor`, then invokes the pinned bundled gobuckify once for all external packages.
Only after generation succeeds does it replace the root vendor directory.
A failure before publication leaves the current vendor tree intact.

Gobuckify requires a `go.mod` and excludes packages by module-name prefix. The
staging directory contains a temporary, dependency-free manifest with this
repository's module prefix. The workspace excludes that manifest. Go selects
dependencies from the real project manifests. No root `go.mod` or second list of
dependencies is committed. This adapter assumes repository modules use that
prefix. The generator covers Linux and macOS on amd64 and arm64.

`check.sh` verifies project enrollment, independently tidied manifests, workspace
checksums, exact vendor regeneration, and native workspace package loading.
Project race, integration, and fuzz checks also run outside the workspace.
Generation and verification use temporary directories and remove them on exit.
They do not use cloud credentials or provision infrastructure.

Run `./buck2 run //tooling/go:test` for tooling regressions. They cover path
isolation, missing enrollment, duplicate vendor trees, empty workspaces, and
preservation of committed sources when generation fails.
