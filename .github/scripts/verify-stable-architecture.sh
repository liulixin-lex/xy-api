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
[ -n "$output_dir" ] || fail '--output-dir is required'

immutable_reference="${repository}@${digest}"
certificate_identity="https://github.com/${workflow_ref}"
certificate_issuer='https://token.actions.githubusercontent.com'
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
mkdir -p "$output_dir"

# Verify the pre-existing trusted signature before consuming any registry metadata.
cosign verify \
  --certificate-identity "$certificate_identity" \
  --certificate-oidc-issuer "$certificate_issuer" \
  --output json \
  "$immutable_reference" > "$output_dir/cosign.json"
jq -e 'type == "array" and length > 0' "$output_dir/cosign.json" >/dev/null ||
  fail 'cosign returned no verified signatures'

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

"$script_dir/verify-release-attestations.sh" \
  --sbom "$output_dir/sbom.json" \
  --provenance "$output_dir/provenance.json" \
  --source-repository "$source_repository" \
  --source-sha "$source_sha" \
  --run-id "$run_id" \
  --run-attempt "$run_attempt" \
  --workflow-ref "$workflow_ref" \
  --workflow-sha "$workflow_sha" \
  --image-reference "$immutable_reference" \
  --platform "$platform" \
  --output "$output_dir/verification.json"
