#!/usr/bin/env bash
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

git ls-files --cached --others --exclude-standard -z | {
  files=()
  while IFS= read -r -d '' file; do
    case "$file" in
      BUCK|*/BUCK|PACKAGE|*/PACKAGE|*.bzl|*.bxl)
        if [[ -f "$file" ]]; then
          files+=("./$file")
        fi
        ;;
    esac
  done

  if (( ${#files[@]} > 0 )); then
    ./tooling/bin/starlark-fmt --config .starlark-format.json fmt "${files[@]}"
  fi
}
