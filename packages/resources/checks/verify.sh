#!/usr/bin/env bash
set -euo pipefail
root=$(git rev-parse --show-toplevel)
cd "$root"
workspace=$(mktemp -d "${TMPDIR:-/tmp}/sir-robs-resources.XXXXXX")
finish() {
  local result=$?
  if (( result == 0 )); then
    rm -rf "$workspace"
  else
    printf 'Resource verification failed; workspace retained: %s\n' "$workspace" >&2
    if [[ -n ${FACTORY_TEST_REPORT_DIR:-} ]]; then
      mkdir -p "$FACTORY_TEST_REPORT_DIR/resource-fuzz-corpus"
      find "$workspace/packages/resources" -type d -path '*/testdata/fuzz' -exec cp -R '{}' "$FACTORY_TEST_REPORT_DIR/resource-fuzz-corpus/" \;
    fi
  fi
  exit "$result"
}
trap finish EXIT
mkdir -p "$workspace/packages"
cp -R packages/resources packages/reconcile "$workspace/packages/"
export GOWORK=off
run_go() {
  "$root/buck2" run 'toolchains//:go[go]' -- -C "$workspace/packages/resources" "$@"
}
export CGO_ENABLED=1
outputs=$(./buck2 build //packages/resources/examples/request-run/cmd:request-run --show-full-json-output)
RESOURCE_PROTOTYPE_BINARY=$(printf '%s' "$outputs" | ./tooling/bin/jq -er 'values | .[]')
export RESOURCE_PROTOTYPE_BINARY
run_go vet -mod=readonly ./...
run_go test -mod=readonly -race -count=1 -timeout=2m ./...
for package in ./tests/unit ./content/s3 ./datastore/dynamodb; do
  listing=$(run_go test -mod=readonly -list '^Fuzz' "$package")
  targets=()
  while IFS= read -r line; do
    if [[ $line =~ ^Fuzz[A-Za-z0-9_]+$ ]]; then targets+=("$line"); fi
  done <<<"$listing"
  if (( ${#targets[@]} == 0 )); then
    printf 'No fuzz targets discovered in %s\n' "$package" >&2
    exit 1
  fi
  for target in "${targets[@]}"; do
    run_go test -mod=readonly "$package" -run='^$' -fuzz="^${target}$" -fuzztime=10s -parallel=2 -timeout=2m
  done
done
printf '%s\n' 'Resource vet, race, integration, prototype, and fuzz checks passed.'
