#!/usr/bin/env bash
set -euo pipefail
root=$(git rev-parse --show-toplevel)
cd "$root"
root=$(pwd -P)
export PATH="$root/.tools/bin:$PATH"
export GOTOOLCHAIN=local
source tooling/go/common.sh
listing=$(workspace_modules "$root")
check_workspace_membership "$root" "$listing"
modules=()
while IFS= read -r module; do modules+=("$module"); done <<<"$listing"
workspace=$(mktemp -d "${TMPDIR:-/tmp}/factory-go-vendor.XXXXXX")
trap 'rm -rf "$workspace"' EXIT
prepare_workspace_tools "$workspace"
stage_workspace "$workspace" "${modules[@]}"
generate_workspace_vendor "$workspace"
diff -u go.work "$workspace/go.work"
# Publish only after all generation steps pass. Keep the committed tree intact
# on module-resolution or generator failure.
rm -rf vendor
mv "$workspace/vendor" vendor
if [[ -f $workspace/go.work.sum ]]; then
  cp "$workspace/go.work.sum" go.work.sum
fi
printf '%s\n' 'Workspace vendor sources and Buck targets regenerated.'
