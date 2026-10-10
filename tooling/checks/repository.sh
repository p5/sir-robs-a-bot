#!/usr/bin/env bash
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

git diff --check -- . ':(exclude)packages/reconcile/vendor/**' ':(exclude)packages/resources/vendor/**' ':(exclude)services/intake/vendor/**'
git diff --cached --check -- . ':(exclude)packages/reconcile/vendor/**' ':(exclude)packages/resources/vendor/**' ':(exclude)services/intake/vendor/**'

# Check committed files and new files with the same rules.
git ls-files --cached --others --exclude-standard -z | while IFS= read -r -d '' file; do
  if [[ -f "$file" ]]; then
    # Keep generated build metadata formatted, but preserve upstream sources.
    case "$file" in
      packages/reconcile/vendor/*/BUCK|packages/resources/vendor/*/BUCK|services/intake/vendor/*/BUCK) ;;
      packages/reconcile/vendor/*|packages/resources/vendor/*|services/intake/vendor/*) continue ;;
    esac
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
