#!/usr/bin/env bash
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
fixture_root=$(mktemp -d "${TMPDIR:-/tmp}/sir-robs-repository-tools.XXXXXX")
trap 'rm -r -- "$fixture_root"' EXIT
checkout="$fixture_root/checkout with spaces"
mkdir -p "$checkout/tooling/checks" "$checkout/tooling/scripts" "$checkout/tooling/bin" "$fixture_root/bin"
cp "$repo_root/tooling/checks/repository.sh" "$repo_root/tooling/checks/lint.sh" "$checkout/tooling/checks/"
cp "$repo_root/tooling/scripts/format-starlark.sh" "$checkout/tooling/scripts/"
cp "$repo_root/tooling/bin/starlark-fmt" "$repo_root/tooling/bin/actionlint" \
  "$repo_root/tooling/bin/shellcheck" "$checkout/tooling/bin/"
cp "$repo_root/.starlark-format.json" "$checkout/"
git -C "$checkout" init --quiet
cd "$checkout"

expect_failure() {
  local expected=$1
  shift
  if "$@" >"$fixture_root/result.log" 2>&1; then
    printf 'Expected failure: %s\n' "$*" >&2
    exit 1
  fi
  local output
  output=$(cat "$fixture_root/result.log")
  if [[ "$output" != *"$expected"* ]]; then
    printf 'Expected diagnostic: %s\n%s\n' "$expected" "$output" >&2
    exit 1
  fi
}

bash tooling/checks/repository.sh >"$fixture_root/result.log" 2>&1
printf '#!/usr/bin/env bash\nexit 0\n' >'-valid shell.sh'
bash tooling/checks/repository.sh >"$fixture_root/result.log" 2>&1
rm -- '-valid shell.sh'
printf 'if then\n' >'invalid shell.sh'
expect_failure 'syntax error' bash tooling/checks/repository.sh
rm -- 'invalid shell.sh'

printf 'trailing space \n' >'untracked file.txt'
expect_failure 'trailing whitespace' bash tooling/checks/repository.sh
git add 'untracked file.txt'
expect_failure 'trailing whitespace' bash tooling/checks/repository.sh
git rm --cached --quiet 'untracked file.txt'
rm -- 'untracked file.txt'

printf 'value=[1,2]\n' >'unformatted file.bxl'
expect_failure 'format Starlark files' bash tooling/checks/repository.sh
bash tooling/scripts/format-starlark.sh >"$fixture_root/result.log" 2>&1
if [[ $(cat 'unformatted file.bxl') != 'value = [1, 2]' ]]; then
  printf '%s\n' 'The formatter did not correct the BXL file.' >&2
  exit 1
fi
bash tooling/checks/repository.sh >"$fixture_root/result.log" 2>&1

printf '%s\n' 'broken syntax [' >'unformatted file.bxl'
expect_failure 'failed to parse module' bash tooling/checks/repository.sh
rm -- 'unformatted file.bxl'

printf 'value=[1,2]\n' >'-unformatted file.bxl'
expect_failure 'format Starlark files' bash tooling/checks/repository.sh
bash tooling/scripts/format-starlark.sh >"$fixture_root/result.log" 2>&1
bash tooling/checks/repository.sh >"$fixture_root/result.log" 2>&1
rm -- '-unformatted file.bxl'

cat >'lint failure.sh' <<'SHELL'
#!/usr/bin/env bash
printf '%s\n' $HOME
SHELL
expect_failure 'SC2086' bash tooling/checks/lint.sh
rm -- 'lint failure.sh'
mkdir -p .github/workflows
cat >.github/workflows/invalid.yml <<'WORKFLOW'
on: push
jobs:
  invalid:
    runs-on: invalid-runner-label
    steps:
      - run: echo test
WORKFLOW
expect_failure 'is unknown' bash tooling/checks/lint.sh
rm -- .github/workflows/invalid.yml

export REPOSITORY_REAL_GIT
REPOSITORY_REAL_GIT=$(command -v git)
cat >"$fixture_root/bin/git" <<'GIT'
#!/usr/bin/env bash
if [[ ${1-} == ls-files ]]; then
  printf '%s\n' 'Simulated Git discovery failure.' >&2
  exit 42
fi
exec "$REPOSITORY_REAL_GIT" "$@"
GIT
chmod +x "$fixture_root/bin/git"
export PATH="$fixture_root/bin:$PATH"
expect_failure 'Simulated Git discovery failure.' bash tooling/checks/repository.sh
expect_failure 'Simulated Git discovery failure.' bash tooling/scripts/format-starlark.sh
expect_failure 'Simulated Git discovery failure.' bash tooling/checks/lint.sh

printf '%s\n' 'Repository tool regression tests passed.'
