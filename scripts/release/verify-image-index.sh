#!/usr/bin/env bash

set -Eeuo pipefail

if [ "$#" -ne 1 ] && [ "$#" -ne 3 ]; then
  echo "usage: $0 <multi-arch-image-reference> [<expected-version> <expected-commit>]" >&2
  exit 2
fi

image_reference=$1
expected_version=${2:-}
expected_commit=${3:-}

if [ -n "$expected_version" ]; then
  if [[ ! "$expected_version" =~ ^v0\.2\.(0|[1-9][0-9]*)$ ]] ||
     [[ ! "$expected_commit" =~ ^[0-9a-f]{40}$ ]]; then
    echo 'expected version/commit must use the protected v0.2.x line and a 40-character lowercase Git commit' >&2
    exit 2
  fi
fi

for command_name in docker jq; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "required command is not installed: $command_name" >&2
    exit 1
  fi
done

work_directory=$(mktemp -d)
manifest_file="$work_directory/index.json"
sbom_file="$work_directory/sbom.json"
provenance_file="$work_directory/provenance.json"
trap 'rm -rf "$work_directory"' EXIT

docker buildx imagetools inspect --raw "$image_reference" >"$manifest_file"

if ! jq --exit-status '
  .schemaVersion == 2 and
  (.mediaType == "application/vnd.oci.image.index.v1+json" or
   .mediaType == "application/vnd.docker.distribution.manifest.list.v2+json") and
  (.manifests | type == "array")
' "$manifest_file" >/dev/null; then
  echo "$image_reference is not a valid OCI/Docker multi-arch image index" >&2
  exit 1
fi

if ! jq --exit-status '
  (.manifests | length) == 4 and
  ([.manifests[] |
    select(
      .mediaType == "application/vnd.oci.image.manifest.v1+json" and
      .platform.os == "linux" and
      (.platform.architecture == "amd64" or .platform.architecture == "arm64") and
      (.digest | type == "string" and test("^sha256:[0-9a-f]{64}$")) and
      (.size | type == "number" and . > 0)
    )
   ] | length) == 2 and
  ([.manifests[] |
    select(
      .mediaType == "application/vnd.oci.image.manifest.v1+json" and
      .platform.os == "unknown" and
      .platform.architecture == "unknown" and
      .annotations["vnd.docker.reference.type"] == "attestation-manifest" and
      (.annotations["vnd.docker.reference.digest"] |
        type == "string" and test("^sha256:[0-9a-f]{64}$")) and
      (.digest | type == "string" and test("^sha256:[0-9a-f]{64}$")) and
      (.size | type == "number" and . > 0)
    )
   ] | length) == 2
' "$manifest_file" >/dev/null; then
  echo "$image_reference must contain exactly two runnable OCI images and two bound attestation manifests" >&2
  exit 1
fi

repository_reference=${image_reference%@*}
last_component=${repository_reference##*/}
if [[ "$last_component" == *:* ]]; then
  repository_reference=${repository_reference%:*}
fi
if [ -z "$repository_reference" ]; then
  echo "unable to derive the image repository from: $image_reference" >&2
  exit 1
fi

for expected_arch in amd64 arm64; do
  runnable_digest=$(jq --raw-output --arg arch "$expected_arch" '
    .manifests[] |
    select(.platform.os == "linux" and .platform.architecture == $arch) |
    .digest
  ' "$manifest_file")
  if ! [[ "$runnable_digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "$image_reference has an invalid linux/$expected_arch image digest" >&2
    exit 1
  fi

  attestation_count=$(jq --arg digest "$runnable_digest" '[
    .manifests[] |
    select(
      .platform.os == "unknown" and
      .platform.architecture == "unknown" and
      .annotations["vnd.docker.reference.type"] == "attestation-manifest" and
      .annotations["vnd.docker.reference.digest"] == $digest
    )
  ] | length' "$manifest_file")
  if [ "$attestation_count" -ne 1 ]; then
    echo "$image_reference must bind exactly one attestation manifest to linux/$expected_arch; found $attestation_count" >&2
    exit 1
  fi

  attestation_digest=$(jq --raw-output --arg digest "$runnable_digest" '
    .manifests[] |
    select(
      .platform.os == "unknown" and
      .platform.architecture == "unknown" and
      .annotations["vnd.docker.reference.type"] == "attestation-manifest" and
      .annotations["vnd.docker.reference.digest"] == $digest
    ) |
    .digest
  ' "$manifest_file")
  if ! [[ "$attestation_digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "$image_reference has an invalid linux/$expected_arch attestation digest" >&2
    exit 1
  fi

  attestation_file="$work_directory/attestation-$expected_arch.json"
  docker buildx imagetools inspect --raw \
    "$repository_reference@$attestation_digest" >"$attestation_file"
  if ! jq --exit-status '
    .schemaVersion == 2 and
    .mediaType == "application/vnd.oci.image.manifest.v1+json" and
    .config.mediaType == "application/vnd.oci.image.config.v1+json" and
    (.config.digest | type == "string" and test("^sha256:[0-9a-f]{64}$")) and
    (.config.size | type == "number" and . > 0) and
    (.layers | type == "array" and length == 2) and
    all(.layers[];
      .mediaType == "application/vnd.in-toto+json" and
      (.digest | type == "string" and test("^sha256:[0-9a-f]{64}$")) and
      (.size | type == "number" and . > 0) and
      (.annotations["in-toto.io/predicate-type"] == "https://spdx.dev/Document" or
       .annotations["in-toto.io/predicate-type"] == "https://slsa.dev/provenance/v1")
    ) and
    ([.layers[] |
      select(.annotations["in-toto.io/predicate-type"] == "https://spdx.dev/Document")
     ] | length) == 1 and
    ([.layers[] |
      select(.annotations["in-toto.io/predicate-type"] == "https://slsa.dev/provenance/v1")
     ] | length) == 1
  ' "$attestation_file" >/dev/null; then
    echo "$image_reference linux/$expected_arch attestation must contain exactly one SPDX SBOM and one SLSA v1 provenance statement" >&2
    exit 1
  fi
done

docker buildx imagetools inspect "$image_reference" \
  --format '{{json .SBOM}}' >"$sbom_file"
if ! jq --exit-status '
  (keys | sort) == ["linux/amd64", "linux/arm64"] and
  all(.[];
    (keys == ["SPDX"]) and
    .SPDX.spdxVersion == "SPDX-2.3" and
    .SPDX.dataLicense == "CC0-1.0" and
    (.SPDX.SPDXID | type == "string" and test("^SPDXRef-")) and
    (.SPDX.name | type == "string" and length > 0) and
    (.SPDX.documentNamespace | type == "string" and test("^https://")) and
    (.SPDX.creationInfo.created |
      type == "string" and
      test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T")) and
    (.SPDX.creationInfo.creators | type == "array" and length > 0) and
    (.SPDX.packages | type == "array" and length > 0)
  )
' "$sbom_file" >/dev/null; then
  echo "$image_reference must contain a non-empty SPDX 2.3 SBOM for linux/amd64 and linux/arm64" >&2
  exit 1
fi

docker buildx imagetools inspect "$image_reference" \
  --format '{{json .Provenance}}' >"$provenance_file"
if ! jq --exit-status '
  (keys | sort) == ["linux/amd64", "linux/arm64"] and
  all(.[];
    (keys == ["SLSA"]) and
    .SLSA.buildDefinition.buildType ==
      "https://github.com/moby/buildkit/blob/master/docs/attestations/slsa-definitions.md" and
    .SLSA.buildDefinition.externalParameters.configSource.path == "Dockerfile" and
    (.SLSA.runDetails.builder.id |
      type == "string" and
      test("^https://github\\.com/[^/]+/[^/]+/actions/runs/[0-9]+/attempts/[0-9]+$")) and
    (.SLSA.runDetails.metadata.startedOn |
      type == "string" and
      test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T")) and
    (.SLSA.runDetails.metadata.finishedOn |
      type == "string" and
      test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T"))
  )
' "$provenance_file" >/dev/null; then
  echo "$image_reference must contain readable SLSA v1 BuildKit provenance for linux/amd64 and linux/arm64" >&2
  exit 1
fi

if [ -n "$expected_version" ] && ! jq --exit-status \
  --arg version "$expected_version" \
  --arg commit "$expected_commit" '
  all(.[].SLSA;
    .buildDefinition.externalParameters.request.args[
      "label:org.opencontainers.image.version"
    ] == $version and
    .buildDefinition.externalParameters.request.args[
      "label:org.opencontainers.image.revision"
    ] == $commit and
    .buildDefinition.externalParameters.request.root.request.args[
      "vcs:revision"
    ] == $commit and
    .runDetails.metadata.buildkit_metadata.vcs.revision == $commit
  )
' "$provenance_file" >/dev/null; then
  echo "$image_reference provenance does not bind every platform to $expected_version at $expected_commit" >&2
  exit 1
fi

echo "verified multi-arch index and attestation content: $image_reference (linux/amd64, linux/arm64; SPDX 2.3, SLSA v1)"
