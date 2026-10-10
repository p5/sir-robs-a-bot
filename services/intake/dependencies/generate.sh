#!/usr/bin/env bash
set -euo pipefail
root=$(git rev-parse --show-toplevel)
cd "$root"
module=${1:-services/intake}
shim=$(mktemp -d)
trap 'rm -rf "$shim"' EXIT
ln -s "$root/buck2" "$shim/buck2"
export PATH="$shim:$root/.tools/bin:$PATH"
export CGO_ENABLED=0
./buck2 run 'toolchains//:go[go]' -- -C "$module" mod vendor
./buck2 run --target-platforms toolchains//:go_platform prelude//go/tools/gobuckify:gobuckify -- "$module"
python3 packages/resources/dependencies/project.py "$module" packages/reconcile services/intake packages/resources
while IFS= read -r -d '' file; do
  ./tooling/bin/starlark-fmt --config .starlark-format.json fmt "$file"
done < <(find "$module/vendor" -name BUCK -print0)
