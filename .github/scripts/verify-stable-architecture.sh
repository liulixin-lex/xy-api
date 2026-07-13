#!/usr/bin/env bash

set -euo pipefail

fail() {
  printf 'stable architecture verification failed: %s\n' "$1" >&2
  exit 1
}

repository=''
digest=''
platform=''
version=''
source_repository=''
source_sha=''
run_id=''
run_attempt=''
workflow_ref=''
workflow_sha=''
output_dir=''

while [ "$#" -gt 0 ]; do
  case "$1" in
    --repository)
      [ "$#" -ge 2 ] || fail '--repository requires a value'
      repository=$2
      shift 2
      ;;
    --digest)
      [ "$#" -ge 2 ] || fail '--digest requires a value'
      digest=$2
      shift 2
      ;;
    --platform)
      [ "$#" -ge 2 ] || fail '--platform requires a value'
      platform=$2
      shift 2
      ;;
    --version)
      [ "$#" -ge 2 ] || fail '--version requires a value'
      version=$2
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
    --output-dir)
      [ "$#" -ge 2 ] || fail '--output-dir requires a value'
      output_dir=$2
      shift 2
      ;;
    --help|-h)
      printf '%s\n' 'Usage: verify-stable-architecture.sh --repository REPOSITORY --digest DIGEST --platform PLATFORM --version VERSION --source-repository OWNER/REPOSITORY --source-sha SHA --run-id ID --run-attempt NUMBER --workflow-ref REF --workflow-sha SHA --output-dir DIRECTORY'
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

command -v cosign >/dev/null 2>&1 || fail 'cosign is required'
command -v docker >/dev/null 2>&1 || fail 'docker is required'
command -v jq >/dev/null 2>&1 || fail 'jq is required'
command -v sha256sum >/dev/null 2>&1 || fail 'sha256sum is required'

if [[ ! "$repository" =~ ^[A-Za-z0-9._-]+(:[0-9]+)?(/[A-Za-z0-9._-]+)+$ ]]; then
  fail "invalid repository: $repository"
fi
if [[ ! "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  fail "invalid architecture digest: $digest"
fi
case "$platform" in
  linux/amd64)
    architecture=amd64
    ;;
  linux/arm64)
    architecture=arm64
    ;;
  *)
    fail "unsupported platform: $platform"
    ;;
esac
if [[ ! "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  fail "invalid stable version: $version"
fi
if [[ ! "$source_repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  fail "invalid source repository: $source_repository"
fi
if [[ ! "$source_sha" =~ ^[0-9a-f]{40}$ ]]; then
  fail "invalid source SHA: $source_sha"
fi
if [[ ! "$run_id" =~ ^[1-9][0-9]*$ ]]; then
  fail "invalid verifier run ID: $run_id"
fi
if [[ ! "$run_attempt" =~ ^[1-9][0-9]*$ ]]; then
  fail "invalid verifier run attempt: $run_attempt"
fi
if [[ ! "$workflow_sha" =~ ^[0-9a-f]{40}$ ]]; then
  fail "invalid verifier workflow SHA: $workflow_sha"
fi
workflow_prefix="$source_repository/.github/workflows/docker-build.yml@refs/"
case "$workflow_ref" in
  "${workflow_prefix}heads/"?*|"${workflow_prefix}tags/"?*) ;;
  *) fail "workflow ref is outside the stable release workflow: $workflow_ref" ;;
esac
[ -n "$output_dir" ] || fail '--output-dir is required'

immutable_reference="${repository}@${digest}"
certificate_issuer='https://token.actions.githubusercontent.com'
current_certificate_identity="https://github.com/${workflow_ref}"
canonical_workflow_ref="${workflow_prefix}tags/${version}"
canonical_certificate_identity="https://github.com/${canonical_workflow_ref}"
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
mkdir -p "$output_dir"

# Verify the pre-existing trusted signature before consuming any registry metadata.
verify_signature_identity() {
  local identity=$1
  local output_file=$2
  local error_file="${output_file}.error"
  if cosign verify \
    --certificate-identity "$identity" \
    --certificate-oidc-issuer "$certificate_issuer" \
    --output json \
    "$immutable_reference" > "$output_file" 2> "$error_file"; then
    rm -f "$error_file"
    jq -e 'type == "array" and length > 0' "$output_file" >/dev/null ||
      fail "cosign returned no verified signatures for $identity"
    return 0
  fi
  rm -f "$output_file" "$error_file"
  return 1
}

current_signature_verified=false
canonical_signature_verified=false
if verify_signature_identity "$current_certificate_identity" "$output_dir/cosign-current.json"; then
  current_signature_verified=true
fi
if [ "$canonical_certificate_identity" = "$current_certificate_identity" ]; then
  canonical_signature_verified=$current_signature_verified
  if [ "$current_signature_verified" = true ]; then
    cp "$output_dir/cosign-current.json" "$output_dir/cosign-canonical.json"
  fi
elif verify_signature_identity "$canonical_certificate_identity" "$output_dir/cosign-canonical.json"; then
  canonical_signature_verified=true
fi
if [ "$current_signature_verified" != true ] && [ "$canonical_signature_verified" != true ]; then
  fail 'immutable image has no signature from the current or canonical stable workflow identity'
fi

docker buildx imagetools inspect "$immutable_reference" \
  --format '{{json .Manifest}}' > "$output_dir/manifest.json"
jq -e \
  --arg digest "$digest" \
  --arg architecture "$architecture" '
  .mediaType == "application/vnd.oci.image.index.v1+json" and
  .digest == $digest and
  (.manifests | type) == "array" and
  (.manifests | length) == 2 and
  all(.manifests[];
    (.digest | test("^sha256:[0-9a-f]{64}$")) and
    .size > 0) and
  ([.manifests[] |
    select(
      .mediaType == "application/vnd.oci.image.manifest.v1+json" and
      .platform.os == "linux" and
      .platform.architecture == $architecture and
      (.annotations["vnd.docker.reference.type"] // "") != "attestation-manifest"
    )] | length) == 1 and
  ([.manifests[] |
    select(
      .mediaType == "application/vnd.oci.image.manifest.v1+json" and
      .annotations["vnd.docker.reference.type"] == "attestation-manifest"
    )] | length) == 1 and
  ([.manifests[] | select(.platform.os == "linux") | .digest] ==
    [.manifests[] |
      select(.annotations["vnd.docker.reference.type"] == "attestation-manifest") |
      .annotations["vnd.docker.reference.digest"]])
' "$output_dir/manifest.json" >/dev/null ||
  fail 'immutable digest is not the expected signed single-platform OCI image with one attestation manifest'

docker buildx imagetools inspect "$immutable_reference" \
  --format '{{json .Image}}' > "$output_dir/image.json"
jq -e \
  --arg platform "$platform" \
  --arg architecture "$architecture" \
  --arg version "$version" \
  --arg revision "$source_sha" '
  (if has($platform) then .[$platform] else . end) |
  .os == "linux" and
  .architecture == $architecture and
  .config.Labels["org.opencontainers.image.version"] == $version and
  .config.Labels["org.opencontainers.image.revision"] == $revision
' "$output_dir/image.json" >/dev/null ||
  fail 'immutable image platform or OCI version/revision labels do not match the release'

attestation_digest=$(jq -r '
  .manifests[] |
  select(.annotations["vnd.docker.reference.type"] == "attestation-manifest") |
  .digest
' "$output_dir/manifest.json")
docker buildx imagetools inspect --raw \
  "${repository}@${attestation_digest}" > "$output_dir/attestation-manifest.json"
jq -e '
  (.layers | type) == "array" and
  [.layers[].annotations["in-toto.io/predicate-type"]] as $types |
  all(.layers[];
    .mediaType == "application/vnd.in-toto+json" and
    (.digest | test("^sha256:[0-9a-f]{64}$")) and
    .size > 0) and
  ($types | index("https://spdx.dev/Document")) != null and
  ($types | index("https://slsa.dev/provenance/v1")) != null
' "$output_dir/attestation-manifest.json" >/dev/null ||
  fail 'immutable image does not contain both SPDX and SLSA attestations'

docker buildx imagetools inspect "$immutable_reference" \
  --format '{{json .SBOM}}' > "$output_dir/sbom-raw.json"
docker buildx imagetools inspect "$immutable_reference" \
  --format '{{json .Provenance}}' > "$output_dir/provenance-raw.json"
jq -n \
  --arg platform "$platform" \
  --slurpfile evidence "$output_dir/sbom-raw.json" '
  if ($evidence[0] | has($platform)) then $evidence[0] else {($platform): $evidence[0]} end
' > "$output_dir/sbom.json"
jq -n \
  --arg platform "$platform" \
  --slurpfile evidence "$output_dir/provenance-raw.json" '
  if ($evidence[0] | has($platform)) then $evidence[0] else {($platform): $evidence[0]} end
' > "$output_dir/provenance.json"

jq -n \
  --arg platform "$platform" \
  --arg run_id "$run_id" \
  --arg workflow_ref "$workflow_ref" \
  --arg workflow_sha "$workflow_sha" \
  --arg canonical_workflow_ref "$canonical_workflow_ref" \
  --arg source_sha "$source_sha" \
  --slurpfile provenance "$output_dir/provenance.json" '
  $provenance[0][$platform] as $entry |
  $entry.SLSA.buildDefinition.internalParameters as $internal |
  {($platform): {
    run_id: $internal.github_run_id,
    run_attempt: $internal.github_run_attempt,
    workflow_ref: $internal.github_workflow_ref,
    workflow_sha: $internal.github_workflow_sha,
    mode: (
      if $internal.github_run_id == $run_id and
        $internal.github_workflow_ref == $workflow_ref and
        $internal.github_workflow_sha == $workflow_sha
      then "same_run"
      elif $internal.github_run_id != $run_id and
        $internal.github_workflow_ref == $canonical_workflow_ref and
        $internal.github_workflow_sha == $source_sha
      then "canonical_tag_run"
      else "untrusted"
      end
    )
  }}
' > "$output_dir/attested-runs.json"

"$script_dir/verify-release-attestations.sh" \
  --sbom "$output_dir/sbom.json" \
  --provenance "$output_dir/provenance.json" \
  --source-repository "$source_repository" \
  --source-sha "$source_sha" \
  --run-id "$run_id" \
  --run-attempt "$run_attempt" \
  --workflow-ref "$workflow_ref" \
  --workflow-sha "$workflow_sha" \
  --attested-runs "$output_dir/attested-runs.json" \
  --release-tag "$version" \
  --image-reference "$immutable_reference" \
  --platform "$platform" \
  --output "$output_dir/verification-base.json"

reuse_mode=$(jq -r --arg platform "$platform" '.[$platform].mode' "$output_dir/attested-runs.json")
case "$reuse_mode" in
  same_run)
    [ "$current_signature_verified" = true ] ||
      fail 'same-run architecture is missing the current workflow signature'
    certificate_identity=$current_certificate_identity
    cp "$output_dir/cosign-current.json" "$output_dir/cosign.json"
    ;;
  canonical_tag_run)
    [ "$canonical_signature_verified" = true ] ||
      fail 'historical architecture is missing the canonical tag workflow signature'
    certificate_identity=$canonical_certificate_identity
    cp "$output_dir/cosign-canonical.json" "$output_dir/cosign.json"
    ;;
  *)
    fail "unsupported architecture reuse mode: $reuse_mode"
    ;;
esac
cosign_sha256=$(sha256sum "$output_dir/cosign.json" | awk '{print $1}')
jq \
  --arg platform "$platform" \
  --arg mode "$reuse_mode" \
  --arg digest "$digest" \
  --arg certificate_identity "$certificate_identity" \
  --arg certificate_issuer "$certificate_issuer" \
  --arg cosign_sha256 "$cosign_sha256" '
  . + {
    signature: {
      verified: true,
      certificate_identity: $certificate_identity,
      certificate_issuer: $certificate_issuer,
      raw_evidence_sha256: $cosign_sha256
    },
    reuse_policy: {
      mode: $mode,
      attested_run: .attested_runs[$platform],
      verifier_run: .verifier_run
    },
    retrieval_binding: {
      immutable_reference: .image_reference,
      requested_digest: $digest,
      resolved_manifest_digest: $digest,
      sbom_and_provenance_retrieved_by_digest: true
    }
  } |
  .checks.immutable_retrieval_binding = true
' "$output_dir/verification-base.json" > "$output_dir/verification.json"
rm -f "$output_dir/verification-base.json"
