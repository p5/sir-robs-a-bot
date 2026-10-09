#!/usr/bin/env bash
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
fixture_root=$(mktemp -d "${TMPDIR:-/tmp}/sir-robs-tool-pins.XXXXXX")
trap 'rm -r -- "$fixture_root"' EXIT
checkout="$fixture_root/checkout"
mkdir -p "$checkout/tooling/bin" "$checkout/tooling/checks"
cp "$repo_root/buck2" "$checkout/"
cp "$repo_root/tooling/bin/"{actionlint,jq,shellcheck,starlark-fmt} "$checkout/tooling/bin/"
cp "$repo_root/tooling/checks/"{tool-pins.sh,tool-pins.jq} "$checkout/tooling/checks/"
git -C "$checkout" init --quiet
cd "$checkout"
jq="$repo_root/tooling/bin/jq"

reject_mutation() {
  local launcher=$1
  local filter=$2
  cp "$launcher" "$fixture_root/original"
  tail -n +2 "$launcher" | "$jq" "$filter" >"$fixture_root/manifest.json"
  {
    printf '#!/usr/bin/env dotslash\n\n'
    cat "$fixture_root/manifest.json"
  } >"$launcher"
  if bash tooling/checks/tool-pins.sh >"$fixture_root/result.log" 2>&1; then
    printf 'Accepted invalid launcher: %s\n' "$filter" >&2
    exit 1
  fi
  cp "$fixture_root/original" "$launcher"
}

bash tooling/checks/tool-pins.sh >"$fixture_root/result.log" 2>&1
reject_mutation buck2 'del(.platforms["linux-aarch64"])'
reject_mutation buck2 '.platforms["linux-x86_64"].digest = "bad"'
reject_mutation buck2 '.platforms["linux-x86_64"].size = 0'
reject_mutation buck2 '.name = "unexpected"'
reject_mutation buck2 '.platforms["linux-x86_64"].providers = []'
reject_mutation buck2 '.platforms["linux-x86_64"].providers[0].url |= sub("2026-[0-9]{2}-[0-9]{2}"; "latest")'
reject_mutation buck2 '.platforms["linux-x86_64"].providers[0].url |= sub("facebook/buck2"; "unexpected/repository")'
reject_mutation tooling/bin/starlark-fmt '(.platforms[].providers[].url) |= sub("[0-9]{4}-[0-9]{2}-[0-9]{2}"; "2000-01-01")'
chmod -x buck2
if bash tooling/checks/tool-pins.sh >"$fixture_root/result.log" 2>&1; then
  printf '%s\n' 'Accepted a launcher without executable permission.' >&2
  exit 1
fi
chmod +x buck2
bash tooling/checks/tool-pins.sh >"$fixture_root/result.log" 2>&1
printf '%s\n' 'Tool pin regression tests passed.'
