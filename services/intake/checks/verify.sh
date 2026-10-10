#!/usr/bin/env bash
set -euo pipefail
root=$(git rev-parse --show-toplevel)
cd "$root"
root=$(pwd -P)
export PATH="$root/.tools/bin:$PATH"
if [[ -n ${FACTORY_TEST_REPORT_DIR:-} ]]; then
  mkdir -p "$FACTORY_TEST_REPORT_DIR"
  FACTORY_TEST_REPORT_DIR=$(cd "$FACTORY_TEST_REPORT_DIR" && pwd -P)
  case "$FACTORY_TEST_REPORT_DIR/" in
    "$root/"*) printf '%s\n' 'Keep factory reports outside the source tree.' >&2; exit 1 ;;
  esac
  export FACTORY_TEST_REPORT_DIR
fi
workspace=$(mktemp -d "${TMPDIR:-/tmp}/intake-checks.XXXXXX")
finish() {
  result=$?
  if (( result == 0 )); then
    rm -rf "$workspace"
  else
    printf 'Intake verification workspace retained: %s\n' "$workspace" >&2
    if [[ -n ${FACTORY_TEST_REPORT_DIR:-} ]]; then
      # The root verifier validates this destination before calling project checks.
      mkdir -p "$FACTORY_TEST_REPORT_DIR/intake-fuzz"
      while IFS= read -r -d '' corpus; do
        relative=${corpus#"$workspace/services/intake/"}
        destination="$FACTORY_TEST_REPORT_DIR/intake-fuzz/$relative"
        mkdir -p "$(dirname "$destination")"
        cp -R "$corpus" "$destination"
      done < <(find "$workspace/services/intake" -type d -path '*/testdata/fuzz' -print0)
    fi
  fi
  exit "$result"
}
trap finish EXIT
mkdir -p "$workspace/services" "$workspace/packages"
cp -R services/intake "$workspace/services/intake"
cp -R packages/reconcile packages/resources "$workspace/packages/"
export GOWORK=off
run_go() {
  "$root/buck2" run 'toolchains//:go[go]' -- -C "$workspace/services/intake" "$@"
}
outputs=$(./buck2 build //services/intake/cmd:intake --show-full-json-output)
INTAKE_BINARY=$(printf '%s' "$outputs" | ./tooling/bin/jq -er 'values | .[]')
export INTAKE_BINARY
export CGO_ENABLED=1
run_go vet -mod=readonly ./...
run_go test -mod=readonly -race -count=1 -timeout=2m ./...
for package in ./internal/providers/github ./internal/providers/gitlab ./internal/intake ./internal/persistence; do
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
printf '%s\n' 'Intake vet, race, integration, and fuzz checks passed.'
