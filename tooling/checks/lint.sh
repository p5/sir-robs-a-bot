#!/usr/bin/env bash
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

# A pipeline propagates Git discovery failures through pipefail.
git ls-files --cached --others --exclude-standard -z | {
  scripts=()
  workflows=()
  while IFS= read -r -d '' file; do
    [[ -f "$file" ]] || continue
    case "$file" in
      *.sh) scripts+=("./$file") ;;
      .github/workflows/*.yml|.github/workflows/*.yaml) workflows+=("./$file") ;;
    esac
  done
  if (( ${#scripts[@]} > 0 )); then
    ./tooling/bin/shellcheck -- "${scripts[@]}"
  fi
  if (( ${#workflows[@]} > 0 )); then
    ./tooling/bin/actionlint -shellcheck "$PWD/tooling/bin/shellcheck" -pyflakes= "${workflows[@]}"
  fi
}
