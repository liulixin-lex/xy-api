#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
verifier=$(cd -- "$script_dir/.." && pwd)/verify-release-attestations.sh
fixture_dir="$script_dir/fixtures"
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT

source_repository='liulixin-lex/xy-api'
source_sha='1111111111111111111111111111111111111111'
workflow_sha='3333333333333333333333333333333333333333'
run_id='123456789'
run_attempt='2'
workflow_ref='liulixin-lex/xy-api/.github/workflows/docker-build.yml@refs/tags/v0.1.11'
image_reference='ghcr.io/liulixin-lex/xy-api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'

verify() {
  local sbom=$1
  local provenance=$2
  local output=$3
  local selected_workflow_ref=${4:-$workflow_ref}
  local selected_image_reference=${5:-$image_reference}
  local selected_platform=${6:-}

  local args=(
    --sbom "$sbom"
    --provenance "$provenance"
    --source-repository "$source_repository"
    --source-sha "$source_sha"
    --run-id "$run_id"
    --run-attempt "$run_attempt"
    --workflow-ref "$selected_workflow_ref"
    --workflow-sha "$workflow_sha"
    --image-reference "$selected_image_reference"
    --output "$output"
  )
  if [ -n "$selected_platform" ]; then
    args+=(--platform "$selected_platform")
  fi
  "$verifier" "${args[@]}"
}

expect_failure() {
  local name=$1
  shift
  if "$@" >"$temp_dir/${name}.stdout" 2>"$temp_dir/${name}.stderr"; then
    printf 'expected failure for %s\n' "$name" >&2
    exit 1
  fi
  if [ ! -s "$temp_dir/${name}.stderr" ]; then
    printf 'expected diagnostic output for %s\n' "$name" >&2
    exit 1
  fi
}

valid_sbom="$fixture_dir/release-sbom.valid.json"
valid_provenance="$fixture_dir/release-provenance.valid.json"
summary="$temp_dir/summary.json"

verify "$valid_sbom" "$valid_provenance" "$summary"
jq -e \
  --arg image_reference "$image_reference" \
  --arg source_sha "$source_sha" '
    .schema_version == 1 and
    .image_reference == $image_reference and
    .source_sha == $source_sha and
    .workflow_sha == "3333333333333333333333333333333333333333" and
    (.raw_evidence_sha256.sbom | test("^[0-9a-f]{64}$")) and
    (.raw_evidence_sha256.provenance | test("^[0-9a-f]{64}$")) and
    .platforms["linux/amd64"].sbom.spdx_version == "SPDX-2.3" and
    .platforms["linux/amd64"].sbom.package_count == 1 and
    .platforms["linux/arm64"].sbom.package_count == 1 and
    .platforms["linux/arm64"].provenance.source_revision == $source_sha and
    .actions_run.platform_attempts["linux/amd64"] == "2" and
    .platforms["linux/arm64"].provenance.run_attempt == "2" and
    all(.checks[]; . == true)
  ' "$summary" >/dev/null

jq 'del(."linux/arm64")' "$valid_sbom" > "$temp_dir/amd64-sbom.json"
jq 'del(."linux/arm64")' "$valid_provenance" > "$temp_dir/amd64-provenance.json"
verify \
  "$temp_dir/amd64-sbom.json" \
  "$temp_dir/amd64-provenance.json" \
  "$temp_dir/amd64-summary.json" \
  "$workflow_ref" \
  "$image_reference" \
  'linux/amd64'
jq -e '
  (.platforms | keys) == ["linux/amd64"] and
  .actions_run.platform_attempts["linux/amd64"] == "2" and
  .platforms["linux/amd64"].provenance.run_attempt == "2"
' "$temp_dir/amd64-summary.json" >/dev/null

jq 'del(."linux/amd64")' "$valid_sbom" > "$temp_dir/arm64-sbom.json"
jq 'del(."linux/amd64")' "$valid_provenance" > "$temp_dir/arm64-provenance.json"
verify \
  "$temp_dir/arm64-sbom.json" \
  "$temp_dir/arm64-provenance.json" \
  "$temp_dir/arm64-summary.json" \
  "$workflow_ref" \
  "$image_reference" \
  'linux/arm64'
jq -e '
  (.platforms | keys) == ["linux/arm64"] and
  .platforms["linux/arm64"].provenance.runner_arch == "ARM64"
' "$temp_dir/arm64-summary.json" >/dev/null

jq '
  ."linux/amd64".SLSA.buildDefinition.internalParameters.github_run_attempt = "1" |
  ."linux/amd64".SLSA.runDetails.builder.id =
    "https://github.com/liulixin-lex/xy-api/actions/runs/123456789/attempts/1"
' "$valid_provenance" > "$temp_dir/mixed-attempts.json"
verify "$valid_sbom" "$temp_dir/mixed-attempts.json" "$temp_dir/mixed-attempts-summary.json"
jq -e '
  .actions_run.platform_attempts == {"linux/amd64": "1", "linux/arm64": "2"} and
  .platforms["linux/amd64"].provenance.builder_id ==
    "https://github.com/liulixin-lex/xy-api/actions/runs/123456789/attempts/1"
' "$temp_dir/mixed-attempts-summary.json" >/dev/null

jq '
  ."linux/amd64".SLSA.buildDefinition.internalParameters.github_run_attempt = "3" |
  ."linux/amd64".SLSA.runDetails.builder.id =
    "https://github.com/liulixin-lex/xy-api/actions/runs/123456789/attempts/3"
' "$valid_provenance" > "$temp_dir/future-attempt.json"
expect_failure future-attempt verify \
  "$valid_sbom" "$temp_dir/future-attempt.json" "$temp_dir/future-attempt-output.json"

jq '."linux/amd64".SLSA.buildDefinition.internalParameters.github_run_attempt = "1"' \
  "$valid_provenance" > "$temp_dir/attempt-builder-mismatch.json"
expect_failure attempt-builder-mismatch verify \
  "$valid_sbom" "$temp_dir/attempt-builder-mismatch.json" "$temp_dir/attempt-builder-mismatch-output.json"

jq 'del(."linux/arm64")' "$valid_sbom" > "$temp_dir/missing-platform.json"
expect_failure missing-platform verify \
  "$temp_dir/missing-platform.json" "$valid_provenance" "$temp_dir/missing-platform-output.json"

jq '."linux/amd64".SPDX.packages = []' "$valid_sbom" > "$temp_dir/empty-packages.json"
expect_failure empty-packages verify \
  "$temp_dir/empty-packages.json" "$valid_provenance" "$temp_dir/empty-packages-output.json"

jq '."linux/arm64".SPDX.spdxVersion = "SPDX-2.2"' "$valid_sbom" > "$temp_dir/wrong-spdx-version.json"
expect_failure wrong-spdx-version verify \
  "$temp_dir/wrong-spdx-version.json" "$valid_provenance" "$temp_dir/wrong-spdx-version-output.json"

jq 'del(."linux/amd64")' "$valid_provenance" > "$temp_dir/missing-provenance-platform.json"
expect_failure missing-provenance-platform verify \
  "$valid_sbom" "$temp_dir/missing-provenance-platform.json" "$temp_dir/missing-provenance-platform-output.json"

jq '."linux/amd64".SLSA.buildDefinition.buildType = "https://example.invalid/build"' \
  "$valid_provenance" > "$temp_dir/wrong-build-type.json"
expect_failure wrong-build-type verify \
  "$valid_sbom" "$temp_dir/wrong-build-type.json" "$temp_dir/wrong-build-type-output.json"

jq '."linux/arm64".SLSA.buildDefinition.externalParameters.request.root.request.args["vcs:revision"] = "2222222222222222222222222222222222222222"' \
  "$valid_provenance" > "$temp_dir/conflicting-revision.json"
expect_failure conflicting-revision verify \
  "$valid_sbom" "$temp_dir/conflicting-revision.json" "$temp_dir/conflicting-revision-output.json"

jq '."linux/amd64".SLSA.runDetails.builder.id = "https://github.com/other/repository/actions/runs/123456789/attempts/2"' \
  "$valid_provenance" > "$temp_dir/wrong-builder.json"
expect_failure wrong-builder verify \
  "$valid_sbom" "$temp_dir/wrong-builder.json" "$temp_dir/wrong-builder-output.json"

jq '."linux/arm64".SLSA.buildDefinition.internalParameters.github_runner_arch = "X64"' \
  "$valid_provenance" > "$temp_dir/wrong-runner-arch.json"
expect_failure wrong-runner-arch verify \
  "$valid_sbom" "$temp_dir/wrong-runner-arch.json" "$temp_dir/wrong-runner-arch-output.json"

jq '."linux/arm64".SLSA.runDetails.metadata.buildkit_metadata.vcs.source = "https://github.com/other/repository"' \
  "$valid_provenance" > "$temp_dir/conflicting-source.json"
expect_failure conflicting-source verify \
  "$valid_sbom" "$temp_dir/conflicting-source.json" "$temp_dir/conflicting-source-output.json"

jq '."linux/amd64".SLSA.buildDefinition.internalParameters.github_workflow_sha = "2222222222222222222222222222222222222222"' \
  "$valid_provenance" > "$temp_dir/wrong-workflow-sha.json"
expect_failure wrong-workflow-sha verify \
  "$valid_sbom" "$temp_dir/wrong-workflow-sha.json" "$temp_dir/wrong-workflow-sha-output.json"

jq '."linux/arm64".SLSA.buildDefinition.internalParameters.github_workflow_ref = "other/repository/.github/workflows/docker-build.yml@refs/tags/v0.1.11"' \
  "$valid_provenance" > "$temp_dir/wrong-provenance-workflow.json"
expect_failure wrong-provenance-workflow verify \
  "$valid_sbom" "$temp_dir/wrong-provenance-workflow.json" "$temp_dir/wrong-provenance-workflow-output.json"

expect_failure wrong-workflow verify \
  "$valid_sbom" \
  "$valid_provenance" \
  "$temp_dir/wrong-workflow-output.json" \
  'other/repository/.github/workflows/docker-build.yml@refs/tags/v0.1.11'

expect_failure mutable-image-reference verify \
  "$valid_sbom" \
  "$valid_provenance" \
  "$temp_dir/mutable-image-reference-output.json" \
  "$workflow_ref" \
  'ghcr.io/liulixin-lex/xy-api:v0.1.11'

printf 'release attestation verifier tests passed\n'
