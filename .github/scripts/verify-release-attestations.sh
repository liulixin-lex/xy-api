#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: verify-release-attestations.sh \
  --sbom FILE \
  --provenance FILE \
  --source-repository OWNER/REPOSITORY \
  --source-sha COMMIT_SHA \
  --run-id ID \
  --run-attempt NUMBER \
  --workflow-ref WORKFLOW_REF \
  --workflow-sha COMMIT_SHA \
  --image-reference REFERENCE \
  [--platform linux/amd64|linux/arm64] \
  --output FILE
EOF
}

fail() {
  printf 'release attestation verification failed: %s\n' "$1" >&2
  exit 1
}

sbom_file=''
provenance_file=''
source_repository=''
source_sha=''
run_id=''
run_attempt=''
workflow_ref=''
workflow_sha=''
image_reference=''
expected_platform=''
output_file=''

while [ "$#" -gt 0 ]; do
  case "$1" in
    --sbom)
      [ "$#" -ge 2 ] || fail '--sbom requires a value'
      sbom_file=$2
      shift 2
      ;;
    --provenance)
      [ "$#" -ge 2 ] || fail '--provenance requires a value'
      provenance_file=$2
      shift 2
      ;;
    --source-repository)
      [ "$#" -ge 2 ] || fail '--source-repository requires a value'
      source_repository=$2
      shift 2
      ;;
    --source-sha)
      [ "$#" -ge 2 ] || fail '--source-sha requires a value'
      source_sha=$2
      shift 2
      ;;
    --run-id)
      [ "$#" -ge 2 ] || fail '--run-id requires a value'
      run_id=$2
      shift 2
      ;;
    --run-attempt)
      [ "$#" -ge 2 ] || fail '--run-attempt requires a value'
      run_attempt=$2
      shift 2
      ;;
    --workflow-ref)
      [ "$#" -ge 2 ] || fail '--workflow-ref requires a value'
      workflow_ref=$2
      shift 2
      ;;
    --workflow-sha)
      [ "$#" -ge 2 ] || fail '--workflow-sha requires a value'
      workflow_sha=$2
      shift 2
      ;;
    --image-reference)
      [ "$#" -ge 2 ] || fail '--image-reference requires a value'
      image_reference=$2
      shift 2
      ;;
    --platform)
      [ "$#" -ge 2 ] || fail '--platform requires a value'
      expected_platform=$2
      shift 2
      ;;
    --output)
      [ "$#" -ge 2 ] || fail '--output requires a value'
      output_file=$2
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

command -v jq >/dev/null 2>&1 || fail 'jq is required'
command -v sha256sum >/dev/null 2>&1 || fail 'sha256sum is required'

[ -f "$sbom_file" ] || fail "SBOM file not found: $sbom_file"
[ -f "$provenance_file" ] || fail "provenance file not found: $provenance_file"
[ -n "$output_file" ] || fail '--output is required'
if [[ ! "$image_reference" =~ @sha256:[0-9a-f]{64}$ ]]; then
  fail "image reference must use an immutable sha256 digest: $image_reference"
fi

if [[ ! "$source_repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  fail "invalid source repository: $source_repository"
fi
if [[ ! "$source_sha" =~ ^[0-9a-f]{40}$ ]]; then
  fail "invalid source SHA: $source_sha"
fi
if [[ ! "$workflow_sha" =~ ^[0-9a-f]{40}$ ]]; then
  fail "invalid workflow SHA: $workflow_sha"
fi
if [[ ! "$run_id" =~ ^[1-9][0-9]*$ ]]; then
  fail "invalid GitHub Actions run ID: $run_id"
fi
if [[ ! "$run_attempt" =~ ^[1-9][0-9]*$ ]]; then
  fail "invalid GitHub Actions run attempt: $run_attempt"
fi
case "$expected_platform" in
  ''|linux/amd64|linux/arm64) ;;
  *) fail "unsupported expected platform: $expected_platform" ;;
esac
workflow_prefix="$source_repository/.github/workflows/docker-build.yml@refs/"
case "$workflow_ref" in
  "${workflow_prefix}heads/"?*|"${workflow_prefix}tags/"?*) ;;
  *) fail "workflow ref is outside the stable release workflow: $workflow_ref" ;;
esac

source_uri="https://github.com/${source_repository}"
build_type='https://github.com/moby/buildkit/blob/master/docs/attestations/slsa-definitions.md'
if [ -n "$expected_platform" ]; then
  expected_platforms=$(jq -cn --arg platform "$expected_platform" '[$platform]')
else
  expected_platforms='["linux/amd64","linux/arm64"]'
fi

if ! jq empty "$sbom_file" >/dev/null 2>&1; then
  fail 'SBOM output is not valid JSON'
fi
if ! jq empty "$provenance_file" >/dev/null 2>&1; then
  fail 'provenance output is not valid JSON'
fi

if ! jq -e \
  --argjson expected_platforms "$expected_platforms" '
  def nonempty_string:
    type == "string" and length > 0;
  def valid_package:
    (.SPDXID? | nonempty_string) and
    (.name? | nonempty_string) and
    (.downloadLocation? | nonempty_string) and
    ((.filesAnalyzed? | type) == "boolean") and
    (.licenseConcluded? | nonempty_string) and
    (.licenseDeclared? | nonempty_string) and
    (.copyrightText? | nonempty_string);

  type == "object" and
  (keys | sort) == ($expected_platforms | sort) and
  all(.[];
    (.SPDX | type) == "object" and
    .SPDX.spdxVersion == "SPDX-2.3" and
    .SPDX.dataLicense == "CC0-1.0" and
    .SPDX.SPDXID == "SPDXRef-DOCUMENT" and
    (.SPDX.name | nonempty_string) and
    (.SPDX.documentNamespace | type == "string" and test("^https?://")) and
    (.SPDX.creationInfo.created | type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T")) and
    (.SPDX.creationInfo.creators | type) == "array" and
    (.SPDX.creationInfo.creators | length) > 0 and
    all(.SPDX.creationInfo.creators[]; nonempty_string) and
    (.SPDX.packages | type) == "array" and
    (.SPDX.packages | length) > 0 and
    all(.SPDX.packages[]; valid_package)
  )
' "$sbom_file" >/dev/null; then
  fail 'SBOM must contain valid, non-empty SPDX-2.3 documents for every expected platform'
fi

if ! jq -e \
  --arg build_type "$build_type" \
  --argjson expected_platforms "$expected_platforms" \
  --arg source_sha "$source_sha" \
  --arg source_uri "$source_uri" \
  --arg source_repository "$source_repository" \
  --arg run_id "$run_id" \
  --arg run_attempt "$run_attempt" \
  --arg workflow_ref "$workflow_ref" \
  --arg workflow_sha "$workflow_sha" '
  def nonempty_string:
    type == "string" and length > 0;
  def revision_claims:
    [
      .SLSA.buildDefinition.externalParameters.request.args["label:org.opencontainers.image.revision"],
      .SLSA.buildDefinition.externalParameters.request.args["vcs:revision"],
      .SLSA.buildDefinition.externalParameters.request.root.request.args["label:org.opencontainers.image.revision"],
      .SLSA.buildDefinition.externalParameters.request.root.request.args["vcs:revision"],
      .SLSA.runDetails.metadata.buildkit_metadata.vcs.revision
    ] | map(select(type == "string" and length > 0));
  def source_claims:
    [
      .SLSA.buildDefinition.externalParameters.request.args["label:org.opencontainers.image.source"],
      .SLSA.buildDefinition.externalParameters.request.args["vcs:source"],
      .SLSA.buildDefinition.externalParameters.request.root.request.args["vcs:source"],
      .SLSA.runDetails.metadata.buildkit_metadata.vcs.source
    ] | map(select(type == "string" and length > 0));
  def valid_run_attempt($maximum):
    type == "string" and
    test("^[1-9][0-9]*$") and
    (tonumber <= ($maximum | tonumber));

  type == "object" and
  (keys | sort) == ($expected_platforms | sort) and
  all(to_entries[];
    .key as $platform |
    .value |
    .SLSA.buildDefinition.internalParameters.github_run_attempt as $build_attempt |
    (.SLSA | type) == "object" and
    .SLSA.buildDefinition.buildType == $build_type and
    .SLSA.buildDefinition.internalParameters.github_repository == $source_repository and
    .SLSA.buildDefinition.internalParameters.github_run_id == $run_id and
    ($build_attempt | valid_run_attempt($run_attempt)) and
    .SLSA.buildDefinition.internalParameters.github_job == "build_single_arch" and
    .SLSA.buildDefinition.internalParameters.github_workflow_ref == $workflow_ref and
    .SLSA.buildDefinition.internalParameters.github_workflow_sha == $workflow_sha and
    .SLSA.buildDefinition.internalParameters.github_runner_environment == "github-hosted" and
    .SLSA.buildDefinition.internalParameters.github_runner_os == "Linux" and
    .SLSA.buildDefinition.internalParameters.github_runner_arch ==
      (if $platform == "linux/amd64" then "X64" else "ARM64" end) and
    .SLSA.runDetails.builder.id ==
      ($source_uri + "/actions/runs/" + $run_id + "/attempts/" + $build_attempt) and
    .SLSA.runDetails.metadata.buildkit_completeness.request == true and
    (.SLSA.runDetails.metadata.invocationId | nonempty_string) and
    (.SLSA.runDetails.metadata.startedOn | nonempty_string) and
    (.SLSA.runDetails.metadata.finishedOn | nonempty_string) and
    (revision_claims | length) >= 2 and
    all(revision_claims[]; . == $source_sha) and
    (source_claims | length) >= 2 and
    all(source_claims[]; . == $source_uri)
  )
' "$provenance_file" >/dev/null; then
  fail 'provenance must bind every expected platform to the release SHA and an allowed attempt of this repository GitHub Actions run using BuildKit SLSA'
fi

sbom_sha256=$(sha256sum "$sbom_file" | awk '{print $1}')
provenance_sha256=$(sha256sum "$provenance_file" | awk '{print $1}')
mkdir -p "$(dirname "$output_file")"

jq -n \
  --slurpfile sbom "$sbom_file" \
  --slurpfile provenance "$provenance_file" \
  --arg image_reference "$image_reference" \
  --arg source_repository "$source_repository" \
  --arg source_sha "$source_sha" \
  --arg run_id "$run_id" \
  --arg run_attempt "$run_attempt" \
  --arg workflow_ref "$workflow_ref" \
  --arg workflow_sha "$workflow_sha" \
  --argjson expected_platforms "$expected_platforms" \
  --arg sbom_sha256 "$sbom_sha256" \
  --arg provenance_sha256 "$provenance_sha256" '
  def platform_evidence($platform):
    {
      sbom: {
        spdx_version: $sbom[0][$platform].SPDX.spdxVersion,
        document_namespace: $sbom[0][$platform].SPDX.documentNamespace,
        package_count: ($sbom[0][$platform].SPDX.packages | length),
        created_at: $sbom[0][$platform].SPDX.creationInfo.created,
        creators: $sbom[0][$platform].SPDX.creationInfo.creators
      },
      provenance: {
        build_type: $provenance[0][$platform].SLSA.buildDefinition.buildType,
        source_uri: $provenance[0][$platform].SLSA.runDetails.metadata.buildkit_metadata.vcs.source,
        source_revision: $provenance[0][$platform].SLSA.runDetails.metadata.buildkit_metadata.vcs.revision,
        workflow_ref: $provenance[0][$platform].SLSA.buildDefinition.internalParameters.github_workflow_ref,
        workflow_sha: $provenance[0][$platform].SLSA.buildDefinition.internalParameters.github_workflow_sha,
        run_attempt: $provenance[0][$platform].SLSA.buildDefinition.internalParameters.github_run_attempt,
        builder_id: $provenance[0][$platform].SLSA.runDetails.builder.id,
        runner_arch: $provenance[0][$platform].SLSA.buildDefinition.internalParameters.github_runner_arch,
        runner_environment: $provenance[0][$platform].SLSA.buildDefinition.internalParameters.github_runner_environment,
        invocation_id: $provenance[0][$platform].SLSA.runDetails.metadata.invocationId,
        started_at: $provenance[0][$platform].SLSA.runDetails.metadata.startedOn,
        finished_at: $provenance[0][$platform].SLSA.runDetails.metadata.finishedOn
      }
    };

  {
    schema_version: 1,
    image_reference: $image_reference,
    source_repository: $source_repository,
    source_sha: $source_sha,
    workflow_ref: $workflow_ref,
    workflow_sha: $workflow_sha,
    actions_run: {
      run_id: $run_id,
      run_attempt: $run_attempt,
      platform_attempts: (reduce $expected_platforms[] as $platform ({};
        .[$platform] = $provenance[0][$platform].SLSA.buildDefinition.internalParameters.github_run_attempt)),
      builder_ids: (reduce $expected_platforms[] as $platform ({};
        .[$platform] = $provenance[0][$platform].SLSA.runDetails.builder.id))
    },
    raw_evidence_sha256: {
      sbom: $sbom_sha256,
      provenance: $provenance_sha256
    },
    platforms: (reduce $expected_platforms[] as $platform ({};
      .[$platform] = platform_evidence($platform))),
    checks: {
      exact_platforms: true,
      spdx_2_3: true,
      packages_nonempty: true,
      buildkit_slsa: true,
      source_revision_matches: true,
      source_repository_matches: true,
      github_actions_builder_matches: true,
      build_attempt_not_newer_than_verifier: true,
      stable_workflow_ref_matches: true,
      workflow_sha_matches: true
    }
  }
' > "$output_file"
