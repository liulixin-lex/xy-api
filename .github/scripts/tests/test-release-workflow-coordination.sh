#!/usr/bin/env bash

set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)
workflows=(
  .github/workflows/release.yml
  .github/workflows/electron-build.yml
)
ghcr_publish_workflows=(
  .github/workflows/docker-build.yml
  .github/workflows/docker-image-alpha.yml
  .github/workflows/docker-image-branch.yml
  .github/workflows/docker-image-nightly.yml
)
# These are literal GitHub Actions and shell expressions expected in the YAML.
# shellcheck disable=SC2016
shared_group='group: stable-github-release-${{ github.repository }}'
# shellcheck disable=SC2016
shared_finalizer='uses: ./.github/workflows/finalize-stable-release.yml'
# shellcheck disable=SC2016
ghcr_login_fallback='password: ${{ secrets.GHCR_TOKEN || secrets.GITHUB_TOKEN }}'
# shellcheck disable=SC2016
shared_ghcr_secret='ghcr_token: ${{ secrets.GHCR_TOKEN }}'
electron_package="$repo_root/electron/package.json"
stable_docker="$repo_root/.github/workflows/docker-build.yml"
finalizer_workflow="$repo_root/.github/workflows/finalize-stable-release.yml"

jq -e '
  .build.nsis.artifactName == "${productName}.Setup.${version}.${ext}" and
  .build.portable.artifactName == "${productName}.${version}.${ext}"
' "$electron_package" >/dev/null || {
  printf 'Electron Windows artifact names must match the immutable release inventory\n' >&2
  exit 1
}

for relative_file in "${workflows[@]}"; do
  file="$repo_root/$relative_file"
  [ "$(grep -Fc "$shared_group" "$file")" -eq 1 ] || {
    printf 'missing shared release finalization concurrency in %s\n' "$relative_file" >&2
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
  [ "$(grep -Fc "$shared_finalizer" "$file")" -eq 1 ] || {
    printf 'shared stable finalizer must be called exactly once in %s\n' "$relative_file" >&2
    exit 1
  }
  [ "$(grep -Fc "$shared_ghcr_secret" "$file")" -eq 1 ] || {
    printf 'shared stable finalizer must receive the GHCR package token in %s\n' "$relative_file" >&2
    exit 1
  }
  if grep -Fq 'finalize-release-assets.sh --tag' "$file"; then
    printf 'asset upload workflow must not publish the Release directly in %s\n' "$relative_file" >&2
    exit 1
  fi
  if grep -Fq '.github/scripts/ensure-release-latest.sh' "$file"; then
    printf 'Latest may only be updated through the complete-asset finalizer in %s\n' "$relative_file" >&2
    exit 1
  fi
done

for relative_file in "${ghcr_publish_workflows[@]}"; do
  file="$repo_root/$relative_file"
  [ "$(grep -Fc "$ghcr_login_fallback" "$file")" -eq 2 ] || {
    printf 'every GHCR publishing job must preserve package-token fallback in %s\n' "$relative_file" >&2
    exit 1
  }
done

[ "$(grep -Fc "$shared_finalizer" "$stable_docker")" -eq 1 ] || {
  echo 'stable Docker workflow must call the shared finalizer exactly once' >&2
  exit 1
}
[ "$(grep -Fc "$shared_ghcr_secret" "$stable_docker")" -eq 1 ] || {
  echo 'stable Docker finalizer must receive the GHCR package token' >&2
  exit 1
}
# shellcheck disable=SC2016
if grep -Fq -- '-t "${repository}:latest"' "$stable_docker"; then
  echo 'stable Docker build must defer latest to the shared finalizer' >&2
  exit 1
fi
grep -Fq 'latest_deferred_to_shared_finalizer: true' "$stable_docker"

# These are literal GitHub Actions expressions expected in the reusable workflow.
# shellcheck disable=SC2016
grep -Fq 'group: stable-publication-${{ github.repository }}' "$finalizer_workflow"
grep -Fq 'contents: write' "$finalizer_workflow"
grep -Fq 'packages: write' "$finalizer_workflow"
grep -Fq 'ghcr_token:' "$finalizer_workflow"
# shellcheck disable=SC2016
grep -Fq 'password: ${{ secrets.ghcr_token || secrets.GITHUB_TOKEN }}' "$finalizer_workflow"
grep -Fq 'finalize-stable-release.sh' "$finalizer_workflow"
grep -Fq 'finalize-release-assets.sh' "$finalizer_workflow"
grep -Fq 'resolve-stable-latest.sh' "$finalizer_workflow"
# shellcheck disable=SC2016
grep -Fq 'GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}' "$finalizer_workflow"
# shellcheck disable=SC2016
grep -Fq 'git ls-remote origin "refs/heads/${DEFAULT_BRANCH}"' "$finalizer_workflow"
# shellcheck disable=SC2016
grep -Fq 'git merge-base --is-ancestor "$SOURCE_SHA" "$trusted_sha"' "$finalizer_workflow"
# shellcheck disable=SC2016
if grep -Fq 'TRUSTED_WORKFLOW_SHA: ${{ github.workflow_sha }}' "$finalizer_workflow"; then
  printf 'shared finalizer must not trust the tag caller workflow SHA as its script root\n' >&2
  exit 1
fi

call_count=$(grep -RFc "$shared_finalizer" "$repo_root/.github/workflows" | awk -F: '{sum += $2} END {print sum + 0}')
[ "$call_count" -eq 3 ] || {
  printf 'expected three stable finalizer callers, found %s\n' "$call_count" >&2
  exit 1
}

printf 'release workflow coordination tests passed\n'
