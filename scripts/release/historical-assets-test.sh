#!/usr/bin/env bash

set -Eeuo pipefail

repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
verifier="$repository_root/scripts/release/verify-historical-assets.sh"
test_directory=$(mktemp -d)
trap 'rm -rf -- "$test_directory"' EXIT

fixture_directory="$test_directory/fixtures"
child_directory="$fixture_directory/oci-manifests"
manifest_path="$test_directory/preservation-manifest.json"
mkdir -p "$child_directory"

amd64_source="$test_directory/amd64.json"
arm64_source="$test_directory/arm64.json"
printf '%s' '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","annotations":{"fixture":"amd64"}}' >"$amd64_source"
printf '%s' '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","annotations":{"fixture":"arm64"}}' >"$arm64_source"

amd64_digest="sha256:$(sha256sum "$amd64_source" | awk '{print $1}')"
arm64_digest="sha256:$(sha256sum "$arm64_source" | awk '{print $1}')"
amd64_size=$(wc -c <"$amd64_source")
arm64_size=$(wc -c <"$arm64_source")
amd64_hex=${amd64_digest#sha256:}
arm64_hex=${arm64_digest#sha256:}
cp "$amd64_source" "$child_directory/$amd64_hex.json"
cp "$arm64_source" "$child_directory/$arm64_hex.json"
printf '%s\n' "$amd64_digest" >"$child_directory/$amd64_hex.digest"
printf '%s\n' "$arm64_digest" >"$child_directory/$arm64_hex.digest"

jq --compact-output --null-input \
  --arg amd64_digest "$amd64_digest" \
  --arg arm64_digest "$arm64_digest" \
  --argjson amd64_size "$amd64_size" \
  --argjson arm64_size "$arm64_size" '{
    schemaVersion: 2,
    mediaType: "application/vnd.oci.image.index.v1+json",
    manifests: [
      {
        mediaType: "application/vnd.oci.image.manifest.v1+json",
        digest: $amd64_digest,
        size: $amd64_size,
        platform: {os: "linux", architecture: "amd64"}
      },
      {
        mediaType: "application/vnd.oci.image.manifest.v1+json",
        digest: $arm64_digest,
        size: $arm64_size,
        platform: {os: "linux", architecture: "arm64"}
      }
    ]
  }' >"$fixture_directory/oci-index.json"
index_digest="sha256:$(sha256sum "$fixture_directory/oci-index.json" | awk '{print $1}')"
index_size=$(wc -c <"$fixture_directory/oci-index.json")
printf '%s\n' "$index_digest" >"$fixture_directory/oci-index.digest"

jq --null-input '{
  ref: "refs/tags/v0.1.6",
  object: {
    type: "commit",
    sha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  }
}' >"$fixture_directory/github-tag.json"

jq --null-input '{
  id: 101,
  node_id: "fixture-release",
  tag_name: "v0.1.6",
  target_commitish: "main",
  name: "v0.1.6",
  body: null,
  draft: false,
  prerelease: false,
  immutable: false,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:01:00Z",
  published_at: "2026-01-01T00:01:00Z",
  author: {id: 1, login: "fixture-bot", type: "Bot"},
  assets: [{
    id: 201,
    name: "fixture.bin",
    label: "",
    size: 7,
    digest: ("sha256:" + ("c" * 64)),
    state: "uploaded",
    content_type: "application/octet-stream",
    created_at: "2026-01-01T00:00:30Z",
    updated_at: "2026-01-01T00:00:30Z",
    uploader: {id: 1, login: "fixture-bot", type: "Bot"}
  }]
}' >"$fixture_directory/github-release.json"

jq --null-input \
  --slurpfile release "$fixture_directory/github-release.json" \
  --arg amd64_digest "$amd64_digest" \
  --arg arm64_digest "$arm64_digest" \
  --arg index_digest "$index_digest" \
  --argjson amd64_size "$amd64_size" \
  --argjson arm64_size "$arm64_size" \
  --argjson index_size "$index_size" '{
    schema_version: 1,
    captured_at: "2026-01-01T00:02:00Z",
    repository: "example/project",
    tag: {
      name: "v0.1.6",
      object_type: "commit",
      commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    },
    github_release: $release[0],
    container: {
      registry: "ghcr.io",
      repository: "example/project",
      tag: "v0.1.6",
      reference: "ghcr.io/example/project:v0.1.6",
      index: {
        media_type: "application/vnd.oci.image.index.v1+json",
        digest: $index_digest,
        size: $index_size,
        manifests: [
          {
            media_type: "application/vnd.oci.image.manifest.v1+json",
            digest: $amd64_digest,
            size: $amd64_size,
            platform: {os: "linux", architecture: "amd64", variant: null},
            annotations: {}
          },
          {
            media_type: "application/vnd.oci.image.manifest.v1+json",
            digest: $arm64_digest,
            size: $arm64_size,
            platform: {os: "linux", architecture: "arm64", variant: null},
            annotations: {}
          }
        ]
      }
    }
  }' >"$manifest_path"

curl() {
  echo 'simulated network unavailable' >&2
  return 99
}
export -f curl

"$verifier" \
  --manifest "$manifest_path" \
  --fixture-directory "$fixture_directory" >/dev/null

expect_failure() {
  local label=$1
  shift
  local output_file="$test_directory/$label.log"
  if "$@" >"$output_file" 2>&1; then
    echo "fixture drift was not rejected: $label" >&2
    exit 1
  fi
  if ! grep -q 'historical asset preservation check failed:' "$output_file"; then
    echo "fixture failure was not explicit: $label" >&2
    cat "$output_file" >&2
    exit 1
  fi
}

expect_failure network-api-unavailable \
  "$verifier" --manifest "$manifest_path"
if ! grep -q 'GitHub tag API is unavailable' "$test_directory/network-api-unavailable.log"; then
  echo 'network failure did not identify the unavailable GitHub API' >&2
  cat "$test_directory/network-api-unavailable.log" >&2
  exit 1
fi

cp "$fixture_directory/github-tag.json" "$test_directory/github-tag.original.json"
jq '.object.sha = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"' \
  "$test_directory/github-tag.original.json" >"$fixture_directory/github-tag.json"
expect_failure tag-drift \
  "$verifier" --manifest "$manifest_path" --fixture-directory "$fixture_directory"
cp "$test_directory/github-tag.original.json" "$fixture_directory/github-tag.json"

cp "$fixture_directory/github-release.json" "$test_directory/github-release.original.json"
jq '.assets[0].digest = ("sha256:" + ("d" * 64))' \
  "$test_directory/github-release.original.json" >"$fixture_directory/github-release.json"
expect_failure release-asset-drift \
  "$verifier" --manifest "$manifest_path" --fixture-directory "$fixture_directory"
cp "$test_directory/github-release.original.json" "$fixture_directory/github-release.json"

printf '\n' >>"$child_directory/$amd64_hex.json"
expect_failure child-manifest-drift \
  "$verifier" --manifest "$manifest_path" --fixture-directory "$fixture_directory"
cp "$amd64_source" "$child_directory/$amd64_hex.json"

mv "$fixture_directory/oci-index.digest" "$test_directory/oci-index.digest.missing"
expect_failure missing-index-evidence \
  "$verifier" --manifest "$manifest_path" --fixture-directory "$fixture_directory"
mv "$test_directory/oci-index.digest.missing" "$fixture_directory/oci-index.digest"

"$verifier" \
  --manifest "$manifest_path" \
  --fixture-directory "$fixture_directory" >/dev/null

echo 'historical asset fixture verification passed'
