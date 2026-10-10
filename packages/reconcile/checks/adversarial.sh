#!/usr/bin/env bash
set -euo pipefail
root=$(git rev-parse --show-toplevel)
cd "$root"
root=$(pwd -P)
seconds=${FACTORY_FUZZ_SECONDS:-10}
if [[ ! $seconds =~ ^[1-9][0-9]{0,3}$ ]]; then
  printf '%s\n' 'FACTORY_FUZZ_SECONDS must be an integer from 1 to 9999.' >&2
  exit 1
fi
report_dir=${FACTORY_TEST_REPORT_DIR:-}
if [[ -n $report_dir ]]; then
  mkdir -p "$report_dir"
  report_dir=$(cd "$report_dir" && pwd -P)
  case "$report_dir/" in
    "$root/"*) printf '%s\n' 'Keep adversarial reports outside the source tree.' >&2; exit 1 ;;
  esac
fi
workspace=$(mktemp -d "${TMPDIR:-/tmp}/sir-robs-adversarial.XXXXXX")
finish() {
  result=$?
  if (( result == 0 )); then
    rm -rf "$workspace"
  else
    printf 'Adversarial test workspace retained: %s\n' "$workspace" >&2
    if [[ -n $report_dir && -d $workspace/module/tests/testdata/fuzz ]]; then
      cp -R "$workspace/module/tests/testdata/fuzz" "$report_dir/fuzz-corpus"
    fi
  fi
  exit "$result"
}
trap finish EXIT
export PATH="$root/.tools/bin:$PATH"
# The consumer smoke test runs the actual Buck artifact under both backends.
publisher_outputs=$(./buck2 build //packages/reconcile/examples/publisher/cmd:publisher --show-full-json-output)
PUBLISHER_BINARY=$(printf '%s' "$publisher_outputs" | ./tooling/bin/jq -er 'values | .[]')
export PUBLISHER_BINARY
cp -R packages/reconcile "$workspace/module"
# The race runtime needs the host C compiler. Application Buck targets keep CGo off.
export CGO_ENABLED=1
export GOWORK=off
run_go() {
  "$root/buck2" run 'toolchains//:go[go]' -- -C "$workspace/module" "$@"
}
run_go vet ./...
run_go test -race -count=1 -timeout=2m ./...
listing=$(run_go test -list '^Fuzz' ./tests)
targets=()
while IFS= read -r line; do
  if [[ $line =~ ^Fuzz[A-Za-z0-9_]+$ ]]; then targets+=("$line"); fi
done <<< "$listing"
if (( ${#targets[@]} == 0 )); then
  printf '%s\n' 'No core fuzz targets were discovered.' >&2
  exit 1
fi
for target in "${targets[@]}"; do
  printf 'Fuzzing %s for %s seconds\n' "$target" "$seconds"
  run_go test ./tests -run='^$' -fuzz="^${target}$" \
    -fuzztime="${seconds}s" -parallel=2 -timeout="$((seconds + 120))s"
done
printf '%s\n' 'Go vet, race checks, and every discovered core fuzz target passed.'
