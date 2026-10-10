#!/usr/bin/env bash
set -euo pipefail
root=$(git rev-parse --show-toplevel)
cd "$root"
# gobuckify invokes buck2 by name. Resolve that name to the pinned launcher.
shim=$(mktemp -d)
trap 'rm -rf "$shim"' EXIT
ln -s "$root/buck2" "$shim/buck2"
export PATH="$shim:$root/.tools/bin:$PATH"
export CGO_ENABLED=0
./buck2 run 'toolchains//:go[go]' -- -C packages/reconcile mod vendor
./buck2 run --target-platforms toolchains//:go_platform prelude//go/tools/gobuckify:gobuckify -- packages/reconcile
./buck2 run //tooling:fmt
