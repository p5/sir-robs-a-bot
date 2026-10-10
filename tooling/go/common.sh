#!/usr/bin/env bash
# Shared operations for the workspace dependency commands.
: "${root:?Resolve the repository root before sourcing common.sh}"

workspace_modules() {
  local source_root=$1 module physical
  local description paths module_path
  description=$(GOWORK="$source_root/go.work" "$root/buck2" run 'toolchains//:go[go]' -- -C "$source_root" work edit -json) || return
  paths=$(printf '%s' "$description" | "$root/tooling/bin/jq" -er '.Use | if length > 0 then .[].DiskPath else error("workspace has no modules") end') || return
  while IFS= read -r module; do
    case "$module" in
      ./*) ;;
      *) printf 'Workspace modules must use repository-relative paths: %s\n' "$module" >&2; return 1 ;;
    esac
    case "/$module/" in
      */../*|*/././*|*$'\n'*) printf 'Invalid workspace module path: %s\n' "$module" >&2; return 1 ;;
    esac
    physical=$(cd "$source_root/$module" && pwd -P) || return
    case "$physical/" in
      "$source_root/"*) ;;
      *) printf 'Workspace module escapes the repository: %s\n' "$module" >&2; return 1 ;;
    esac
    if [[ -e $physical/vendor ]]; then
      printf 'Use the root vendor tree, not %s/vendor\n' "$module" >&2
      return 1
    fi
    module_path=$(GOWORK=off "$root/buck2" run 'toolchains//:go[go]' -- -C "$physical" mod edit -json | "$root/tooling/bin/jq" -er '.Module.Path') || return
    case "$module_path" in
      github.com/p5/sir-robs-a-bot/*) ;;
      *) printf 'Workspace module must use the repository module prefix: %s\n' "$module_path" >&2; return 1 ;;
    esac
    printf '%s\n' "${module#./}"
  done <<<"$paths"
}

stage_workspace() {
  local destination=$1 module
  shift
  cp "$root/go.work" "$destination/go.work"
  if [[ -f $root/go.work.sum ]]; then
    cp "$root/go.work.sum" "$destination/go.work.sum"
  fi
  cp "$root/tooling/go/gobuckify.json" "$destination/gobuckify.json"
  for module in "$@"; do
    mkdir -p "$destination/$module"
    cp -R "$root/$module/." "$destination/$module/"
  done
}

check_workspace_membership() {
  local source_root=$1 listing=$2 discovered
  discovered=$(git -C "$source_root" ls-files --cached --others --exclude-standard -- go.mod '**/go.mod') || return
  diff -u <(printf '%s\n' "$listing" | sort) <(
    while IFS= read -r manifest; do
      [[ -f $source_root/$manifest ]] || continue
      case "$manifest" in vendor/*) continue ;; esac
      printf '%s\n' "${manifest%/go.mod}"
    done <<<"$discovered" | sort
  )
}

generate_workspace_vendor() {
  local destination=$1 file
  GOWORK="$destination/go.work" "$root/buck2" run 'toolchains//:go[go]' -- -C "$destination" work vendor
  # gobuckify uses this name only to exclude our packages. The temporary module
  # has no dependencies and is not a workspace member. Go resolves the workspace.
  printf 'module github.com/p5/sir-robs-a-bot\n' >"$destination/go.mod"
  GOWORK="$destination/go.work" GOFLAGS=-mod=vendor "$root/buck2" run --target-platforms toolchains//:go_platform \
    prelude//go/tools/gobuckify:gobuckify -- "$destination"
  while IFS= read -r -d '' file; do
    "$root/tooling/bin/starlark-fmt" --config "$root/.starlark-format.json" fmt "$file"
  done < <(find "$destination/vendor" -name BUCK -print0)
}

prepare_workspace_tools() {
  local destination=$1
  mkdir -p "$destination/bin"
  ln -s "$root/buck2" "$destination/bin/buck2"
  # gobuckify invokes buck2 by name. Keep it on the pinned launcher.
  export PATH="$destination/bin:$root/.tools/bin:$PATH"
  export CGO_ENABLED=0
}
