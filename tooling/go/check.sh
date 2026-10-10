#!/usr/bin/env bash
set -euo pipefail
root=$(git rev-parse --show-toplevel)
cd "$root"
root=$(pwd -P)
export PATH="$root/.tools/bin:$PATH"
export GOTOOLCHAIN=local
source tooling/go/common.sh
bash tooling/go/tests.sh
listing=$(workspace_modules "$root")
modules=()
while IFS= read -r module; do modules+=("$module"); done <<<"$listing"
workspace=$(mktemp -d "${TMPDIR:-/tmp}/factory-go-check.XXXXXX")
trap 'rm -rf "$workspace"' EXIT
check_workspace_membership "$root" "$listing"
prepare_workspace_tools "$workspace"
stage_workspace "$workspace" "${modules[@]}"
for module in "${modules[@]}"; do
  GOWORK=off "$root/buck2" run 'toolchains//:go[go]' -- -C "$workspace/$module" mod tidy
  diff -u "$root/$module/go.mod" "$workspace/$module/go.mod"
  diff -u "$root/$module/go.sum" "$workspace/$module/go.sum"
done
generate_workspace_vendor "$workspace"
diff -u go.work "$workspace/go.work"
if [[ -f $workspace/go.work.sum || -f $root/go.work.sum ]]; then
  diff -u "$root/go.work.sum" "$workspace/go.work.sum"
fi
diff -ru vendor "$workspace/vendor"
# Fail on package-loading errors. gobuckify itself uses go list -e.
GOWORK="$workspace/go.work" "$root/buck2" run 'toolchains//:go[go]' -- -C "$workspace" list -mod=vendor -deps -test all >/dev/null
printf '%s\n' 'Workspace membership, independent module metadata, vendored sources, and Buck targets match.'
