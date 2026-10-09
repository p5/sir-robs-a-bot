#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
bin_dir="$repo_root/.tools/bin"
download_dir="$repo_root/.tools/downloads"
version=0.5.9

if [[ -x "$bin_dir/dotslash" ]] && [[ $("$bin_dir/dotslash" --version) == "DotSlash $version" ]]; then
  printf 'DotSlash %s is ready in %s\n' "$version" "$bin_dir"
  exit 0
fi

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64)
    platform=ubuntu-22.04.x86_64
    digest=7400dd0dbdf05d3be4e604b62807eb1bcbaa0d1bced010d30746243759e0ac03
    ;;
  Linux/aarch64|Linux/arm64)
    platform=ubuntu-22.04.aarch64
    digest=66030e17dc45386e13cca37b72eef78522c4940605a4fb10c977e1ee5b6f1170
    ;;
  Darwin/x86_64|Darwin/arm64)
    platform=macos
    digest=cf8ff37fe7a9e36ed71fbd53d1febbca40bec34039707d8a2c0185c25bbf3c0a
    ;;
  *)
    printf '%s\n' 'The repository bootstrap supports Linux and macOS on x86_64 or ARM64.' >&2
    exit 1
    ;;
esac

mkdir -p "$bin_dir" "$download_dir"
archive="$download_dir/dotslash-$version-$platform.tar.gz"
curl --fail --location --silent --show-error --retry 3 \
  "https://github.com/facebook/dotslash/releases/download/v$version/dotslash-$platform.tar.gz" \
  --output "$archive"

if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$archive")
else
  actual=$(shasum -a 256 "$archive")
fi
actual=${actual%% *}
if [[ "$actual" != "$digest" ]]; then
  printf '%s\n' 'DotSlash archive checksum failed.' >&2
  exit 1
fi

tar -xzf "$archive" -C "$bin_dir" dotslash
"$bin_dir/dotslash" --version
printf 'DotSlash is ready in %s\n' "$bin_dir"
