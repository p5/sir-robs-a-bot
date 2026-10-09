#!/usr/bin/env bash
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

git diff --check
git diff --cached --check

# Check committed files and new files with the same rules.
git ls-files --cached --others --exclude-standard -z | while IFS= read -r -d '' file; do
  if [[ -f "$file" ]]; then
    case "$file" in
      *.sh)
        bash -n -- "$file"
        ;;
      BUCK|*/BUCK|PACKAGE|*/PACKAGE|*.bzl|*.bxl)
        formatting=$(./tooling/bin/starlark-fmt --config .starlark-format.json diff "./$file")
        if [[ -n "$formatting" ]]; then
          printf '%s\n' "$formatting" >&2
          printf '%s\n' 'Run ./buck2 run //tooling:fmt to format Starlark files.' >&2
          exit 1
        fi
        ;;
    esac
    if git diff --no-index --check -- /dev/null "$file"; then
      continue
    else
      status=$?
      # A difference alone returns 1. Whitespace errors also set bit 2.
      if (( status != 1 )); then
        exit "$status"
      fi
    fi
  fi
done

printf '%s\n' 'Repository shell syntax, whitespace, and Starlark formatting checks passed.'
