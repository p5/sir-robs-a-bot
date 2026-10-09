#!/usr/bin/env bash
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

buck_release=
formatter_release=
for launcher in buck2 tooling/bin/starlark-fmt tooling/bin/actionlint tooling/bin/shellcheck tooling/bin/jq; do
  if [[ ! -x "$launcher" ]] || [[ $(head -n 1 "$launcher") != '#!/usr/bin/env dotslash' ]]; then
    printf 'Invalid executable launcher: %s\n' "$launcher" >&2
    exit 1
  fi
  case "$launcher" in
    buck2) name=buck2; repo=facebook/buck2 ;;
    tooling/bin/starlark-fmt) name=starlark_fmt; repo=facebook/buck2 ;;
    tooling/bin/actionlint) name=actionlint; repo=rhysd/actionlint ;;
    tooling/bin/shellcheck) name=shellcheck; repo=koalaman/shellcheck ;;
    tooling/bin/jq) name=jq; repo=jqlang/jq ;;
  esac
  dotslash -- parse "$launcher" >/dev/null
  release=$(tail -n +2 "$launcher" | ./tooling/bin/jq -er \
    --arg name "$name" --arg repo "$repo" -f tooling/checks/tool-pins.jq)
  case "$launcher" in
    buck2) buck_release=$release ;;
    tooling/bin/starlark-fmt) formatter_release=$release ;;
  esac
  printf '%s pins %s\n' "$launcher" "$release"
done
if [[ "$buck_release" != "$formatter_release" ]] || [[ ! "$buck_release" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
  printf '%s\n' 'Buck2 and its formatter must pin the same dated release.' >&2
  exit 1
fi
