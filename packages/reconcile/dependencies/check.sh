#!/usr/bin/env bash
set -euo pipefail
root=$(git rev-parse --show-toplevel)
cd "$root"
workspace=$(mktemp -d)
trap 'rm -rf "$workspace"' EXIT
cp -R packages/reconcile "$workspace/module"
rm -rf "$workspace/module/vendor"
mkdir "$workspace/bin"
ln -s "$root/buck2" "$workspace/bin/buck2"
export PATH="$workspace/bin:$root/.tools/bin:$PATH"
export CGO_ENABLED=0
./buck2 run 'toolchains//:go[go]' -- -C "$workspace/module" mod tidy
diff -u packages/reconcile/go.mod "$workspace/module/go.mod"
diff -u packages/reconcile/go.sum "$workspace/module/go.sum"
./buck2 run 'toolchains//:go[go]' -- -C "$workspace/module" mod vendor
./buck2 run --target-platforms toolchains//:go_platform prelude//go/tools/gobuckify:gobuckify -- "$workspace/module"
while IFS= read -r -d '' file; do
  ./tooling/bin/starlark-fmt --config .starlark-format.json fmt "$file"
done < <(find "$workspace/module/vendor" -name BUCK -print0)
diff -ru packages/reconcile/vendor "$workspace/module/vendor"
printf '%s\n' 'Go module metadata, vendored sources, and generated Buck targets match.'
