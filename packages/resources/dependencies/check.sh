#!/usr/bin/env bash
set -euo pipefail
root=$(git rev-parse --show-toplevel)
cd "$root"
python3 packages/resources/dependencies/test_projection.py
workspace=$(mktemp -d)
trap 'rm -rf "$workspace"' EXIT
mkdir -p "$workspace/packages"
cp -R packages/resources "$workspace/packages/resources"
cp -R packages/reconcile "$workspace/packages/reconcile"
rm -rf "$workspace/packages/resources/vendor"
export PATH="$root/.tools/bin:$PATH"
./buck2 run 'toolchains//:go[go]' -- -C "$workspace/packages/resources" mod tidy
diff -u packages/resources/go.mod "$workspace/packages/resources/go.mod"
diff -u packages/resources/go.sum "$workspace/packages/resources/go.sum"
bash packages/resources/dependencies/generate.sh "$workspace/packages/resources"
diff -ru packages/resources/vendor "$workspace/packages/resources/vendor"
printf '%s\n' 'Resource module metadata and generated dependency projection match.'
