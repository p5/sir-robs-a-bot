#!/usr/bin/env bash
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

report_dir=
steps='[]'
outcome=running
if [[ -n ${BUCK2_REPORT_DIR:-} ]]; then
  mkdir -p "$BUCK2_REPORT_DIR"
  report_parent=$(cd "$BUCK2_REPORT_DIR" && pwd -P)
  case "$report_parent/" in
    "$PWD/"*) printf '%s\n' 'Keep verification reports outside the source tree.' >&2; exit 1 ;;
  esac
  report_dir=$(mktemp -d "$report_parent/verification.XXXXXX")
  revision=$(git rev-parse HEAD)
  worktree_status=$(git status --porcelain=v1 --untracked-files=all)
  started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)

  write_report() {
    ./tooling/bin/jq -n \
      --arg revision "$revision" --arg worktree_status "$worktree_status" \
      --arg started_at "$started_at" --arg outcome "$outcome" \
      --argjson exit_code "$1" --argjson steps "$steps" \
      '{schema_version: 1, revision: $revision, worktree_status: $worktree_status,
        started_at: $started_at, outcome: $outcome, exit_code: $exit_code, steps: $steps}' \
      >"$report_dir/verification.json.tmp"
    mv "$report_dir/verification.json.tmp" "$report_dir/verification.json"
  }
  finish_report() {
    local result=$?
    trap - EXIT
    if [[ $outcome != passed ]]; then outcome=failed; fi
    write_report "$result" || exit 1
    exit "$result"
  }
  trap finish_report EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  write_report null
  printf 'Verification reports: %s\n' "$report_dir"
fi

run_step() {
  local result=0 command_json
  "$@" || result=$?
  if [[ -n $report_dir ]]; then
    command_json=$(./tooling/bin/jq -n --args '$ARGS.positional' -- "$@")
    steps=$(./tooling/bin/jq -n --argjson steps "$steps" \
      --argjson command "$command_json" --argjson result "$result" \
      '$steps + [{command: $command, exit_code: $result}]')
    write_report null
  fi
  if (( result != 0 )); then exit "$result"; fi
}

run_step bash packages/reconcile/dependencies/check.sh
run_step bash packages/resources/dependencies/check.sh

# Buck2 has finished the run target's build before it starts this command.
run_step ./buck2 audit visibility //... toolchains//...
if [[ -n $report_dir ]]; then
  run_step ./buck2 build //... --build-report "$report_dir/buck2-build-report.json"
  run_step ./buck2 test //... --build-report "$report_dir/buck2-test-build-report.json"
else
  run_step ./buck2 build //...
  run_step ./buck2 test //...
fi
run_step env FACTORY_TEST_REPORT_DIR="$report_dir" bash packages/reconcile/checks/adversarial.sh
run_step env FACTORY_TEST_REPORT_DIR="$report_dir" bash services/intake/checks/verify.sh
run_step env FACTORY_TEST_REPORT_DIR="$report_dir" bash packages/resources/checks/verify.sh
outcome=passed
