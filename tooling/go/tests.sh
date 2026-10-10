#!/usr/bin/env bash
set -euo pipefail
root=$(git rev-parse --show-toplevel)
cd "$root"
root=$(pwd -P)
export PATH="$root/.tools/bin:$PATH"
export GOTOOLCHAIN=local
source tooling/go/common.sh
fixture=$(mktemp -d "${TMPDIR:-/tmp}/factory-go-tools.XXXXXX")
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/checkout with spaces/packages/one module"
checkout="$fixture/checkout with spaces"
git -C "$checkout" init --quiet
printf 'module github.com/p5/sir-robs-a-bot/packages/one\n\ngo 1.27.2\n' >"$checkout/packages/one module/go.mod"
printf 'go 1.27.2\nuse "./packages/one module"\n' >"$checkout/go.work"
listing=$(workspace_modules "$checkout")
[[ $listing == 'packages/one module' ]]
check_workspace_membership "$checkout" "$listing"

expect_failure() {
  local diagnostic=$1
  shift
  if "$@" >"$fixture/result.log" 2>&1; then
    printf 'Expected failure from %s\n' "$*" >&2
    exit 1
  fi
  if ! grep -Fq "$diagnostic" "$fixture/result.log"; then
    cat "$fixture/result.log" >&2
    exit 1
  fi
}

mkdir -p "$checkout/services/missing"
printf 'module github.com/p5/sir-robs-a-bot/services/missing\n' >"$checkout/services/missing/go.mod"
expect_failure 'services/missing' check_workspace_membership "$checkout" "$listing"
rm -r "$checkout/services"
mkdir "$checkout/packages/one module/vendor"
expect_failure 'Use the root vendor tree' workspace_modules "$checkout"
rmdir "$checkout/packages/one module/vendor"
printf 'go 1.27.2\n' >"$checkout/go.work"
expect_failure 'workspace has no modules' workspace_modules "$checkout"
printf 'go 1.27.2\nuse ../outside\n' >"$checkout/go.work"
expect_failure 'repository-relative paths' workspace_modules "$checkout"
mkdir "$fixture/outside"
ln -s "$fixture/outside" "$checkout/escape"
printf 'go 1.27.2\nuse ./escape\n' >"$checkout/go.work"
expect_failure 'escapes the repository' workspace_modules "$checkout"
printf 'go 1.27.2\nuse ./packages/../../outside\n' >"$checkout/go.work"
expect_failure 'Invalid workspace module path' workspace_modules "$checkout"

# Inject a vendor-generation failure through the actual command. It must leave
# the existing committed vendor tree intact and remove its temporary workspace.
mkdir -p "$checkout/tooling/go" "$checkout/tooling/bin" "$checkout/vendor" "$fixture/staging"
cp tooling/go/common.sh tooling/go/generate.sh tooling/go/gobuckify.json "$checkout/tooling/go/"
cp tooling/bin/jq "$checkout/tooling/bin/"
printf 'go 1.27.2\nuse "./packages/one module"\n' >"$checkout/go.work"
printf 'original\n' >"$checkout/vendor/sentinel"
cat >"$checkout/buck2" <<'BUCK'
#!/usr/bin/env bash
case "$*" in
  *'work edit -json') printf '%s\n' '{"Use":[{"DiskPath":"./packages/one module"}]}' ;;
  *'mod edit -json') printf '%s\n' '{"Module":{"Path":"github.com/p5/sir-robs-a-bot/packages/one"}}' ;;
  *'work vendor') printf '%s\n' 'injected generation failure' >&2; exit 42 ;;
  *) printf 'Unexpected invocation: %s\n' "$*" >&2; exit 1 ;;
esac
BUCK
chmod +x "$checkout/buck2"
(
  cd "$checkout"
  export TMPDIR="$fixture/staging"
  expect_failure 'injected generation failure' bash tooling/go/generate.sh
)
[[ $(cat "$checkout/vendor/sentinel") == original ]]
[[ -z $(ls -A "$fixture/staging") ]]
printf '%s\n' 'Go workspace tooling regressions passed.'
