#!/usr/bin/env bash

set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)
workflows=(
  .github/workflows/release.yml
  .github/workflows/electron-build.yml
)
# These are literal GitHub Actions and shell expressions expected in the YAML.
# shellcheck disable=SC2016
shared_group='group: stable-github-release-${{ github.repository }}'
# shellcheck disable=SC2016
finalizer_call='.github/scripts/finalize-release-assets.sh --tag "$TAG"'

for relative_file in "${workflows[@]}"; do
  file="$repo_root/$relative_file"
  [ "$(grep -Fc "$shared_group" "$file")" -eq 1 ] || {
    printf 'missing shared release finalization concurrency in %s\n' "$relative_file" >&2
    exit 1
  }
  [ "$(grep -Fc 'contents: write' "$file")" -eq 1 ] || {
    printf 'contents:write must be limited to one final release job in %s\n' "$relative_file" >&2
    exit 1
  }
  [ "$(grep -Fc 'draft: true' "$file")" -eq 1 ] || {
    printf 'release uploads must remain drafts in %s\n' "$relative_file" >&2
    exit 1
  }
  [ "$(grep -Fc 'make_latest: false' "$file")" -eq 1 ] || {
    printf 'release uploads must not update Latest in %s\n' "$relative_file" >&2
    exit 1
  }
  [ "$(grep -Fc "$finalizer_call" "$file")" -eq 1 ] || {
    printf 'release finalizer must run exactly once in %s\n' "$relative_file" >&2
    exit 1
  }
  if grep -Fq '.github/scripts/ensure-release-latest.sh' "$file"; then
    printf 'Latest may only be updated through the complete-asset finalizer in %s\n' "$relative_file" >&2
    exit 1
  fi
done

printf 'release workflow coordination tests passed\n'
