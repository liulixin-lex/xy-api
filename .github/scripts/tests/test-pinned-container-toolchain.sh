#!/usr/bin/env bash

# Workflow assertions intentionally match literal shell expressions.
# shellcheck disable=SC2016

set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)
buildx_action='docker/setup-buildx-action@8d2750c68a42422c14e847fe6c8ac0403b4cbd6f'
buildx_version='v0.35.0'
buildkit_image='moby/buildkit@sha256:0168606be2315b7c807a03b3d8aa79beefdb31c98740cebdffdfeebf31190c9f'
workflows=(
  .github/workflows/docker-build.yml
  .github/workflows/docker-image-alpha.yml
  .github/workflows/docker-image-branch.yml
  .github/workflows/docker-image-nightly.yml
)
setup_count=0

for relative_file in "${workflows[@]}"; do
  file="$repo_root/$relative_file"
  mapfile -t setup_lines < <(grep -nF "uses: $buildx_action" "$file" | cut -d: -f1)
  [ "${#setup_lines[@]}" -gt 0 ] || {
    printf 'missing pinned Buildx setup in %s\n' "$relative_file" >&2
    exit 1
  }
  for line in "${setup_lines[@]}"; do
    setup_count=$((setup_count + 1))
    block=$(sed -n "${line},$((line + 6))p" "$file")
    grep -Fq "version: $buildx_version" <<< "$block" || {
      printf 'Buildx version is not pinned near %s:%s\n' "$relative_file" "$line" >&2
      exit 1
    }
    if grep -Fq 'driver: docker' <<< "$block"; then
      continue
    fi
    grep -Fq "driver-opts: image=$buildkit_image" <<< "$block" || {
      printf 'BuildKit image digest is not pinned near %s:%s\n' "$relative_file" "$line" >&2
      exit 1
    }
  done
done

[ "$setup_count" -eq 8 ] || {
  printf 'expected 8 pinned Buildx setup steps, found %s\n' "$setup_count" >&2
  exit 1
}

if rg -n 'driver-opts:\s*image=moby/buildkit(?::|@)(?!sha256:0168606be2315b7c807a03b3d8aa79beefdb31c98740cebdffdfeebf31190c9f)' \
  "${workflows[@]/#/$repo_root/}" -P; then
  echo 'found a mutable or unexpected BuildKit image reference' >&2
  exit 1
fi

stable_workflow="$repo_root/.github/workflows/docker-build.yml"
finalizer_script="$repo_root/.github/scripts/finalize-stable-release.sh"
[ "$(grep -Fc 'TRUSTED_WORKFLOW_SHA: ${{ github.workflow_sha }}' "$stable_workflow")" -eq 3 ]
[ "$(grep -Fc 'git archive --format=tar "$TRUSTED_WORKFLOW_SHA" .github | tar -xf - -C "$trusted_root"' "$stable_workflow")" -eq 3 ]
[ "$(grep -Fc 'echo "RELEASE_CI_ROOT=$trusted_root" >> "$GITHUB_ENV"' "$stable_workflow")" -eq 3 ]
grep -Fq '"$RELEASE_CI_ROOT/.github/scripts/resolve-stable-architecture.sh"' "$stable_workflow"
grep -Fq '"$RELEASE_CI_ROOT/.github/scripts/verify-release-attestations.sh"' "$stable_workflow"
grep -Fq '"$script_dir/resolve-stable-latest.sh"' "$finalizer_script"
if rg -n '^\s+\.github/scripts/' "$stable_workflow"; then
  echo 'stable release workflow executes tooling from the checked-out release tag' >&2
  exit 1
fi
grep -Fq 'context: .' "$stable_workflow"

printf 'container publishing toolchain pin tests passed\n'
