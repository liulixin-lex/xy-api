#!/usr/bin/env bash

set -Eeuo pipefail

repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
manifest_path="$repository_root/security/historical-assets/v0.1.6.json"
fixture_directory=

usage() {
  cat <<'EOF'
usage: verify-historical-assets.sh [--manifest <path>] [--fixture-directory <path>]

Without --fixture-directory, this command performs read-only checks against the
GitHub API and the public GHCR registry. Fixture mode is reserved for the local,
deterministic regression test and never falls back to the network.
EOF
}

fail() {
  echo "historical asset preservation check failed: $*" >&2
  exit 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --manifest)
      [ "$#" -ge 2 ] || fail '--manifest requires a path'
      manifest_path=$2
      shift 2
      ;;
    --fixture-directory)
      [ "$#" -ge 2 ] || fail '--fixture-directory requires a path'
      fixture_directory=$2
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      usage >&2
      fail "unknown argument: $1"
      ;;
  esac
done

for required_command in awk cmp diff jq mktemp sha256sum wc; do
  command -v "$required_command" >/dev/null 2>&1 || fail "missing command: $required_command"
done
if [ -z "$fixture_directory" ]; then
  command -v curl >/dev/null 2>&1 || fail 'missing command: curl'
fi

[ -f "$manifest_path" ] || fail "preservation manifest does not exist: $manifest_path"

if ! jq --exit-status '
  .schema_version == 1 and
  (.repository | type == "string" and test("^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")) and
  (.tag.name | type == "string" and test("^v[0-9]+\\.[0-9]+\\.[0-9]+$")) and
  .tag.object_type == "commit" and
  (.tag.commit | type == "string" and test("^[0-9a-f]{40}$")) and
  .github_release.tag_name == .tag.name and
  (.github_release.id | type == "number") and
  (.github_release.assets | type == "array" and length > 0) and
  ([.github_release.assets[].name] | length == (unique | length)) and
  ([.github_release.assets[] |
    (.id | type == "number") and
    (.name | type == "string" and length > 0) and
    (.size | type == "number" and . >= 0) and
    (.digest | type == "string" and test("^sha256:[0-9a-f]{64}$")) and
    .state == "uploaded"
  ] | all) and
  .container.registry == "ghcr.io" and
  .container.repository == .repository and
  .container.tag == .tag.name and
  .container.reference == (.container.registry + "/" + .container.repository + ":" + .container.tag) and
  .container.index.media_type == "application/vnd.oci.image.index.v1+json" and
  (.container.index.digest | type == "string" and test("^sha256:[0-9a-f]{64}$")) and
  (.container.index.size | type == "number" and . > 0) and
  (.container.index.manifests | type == "array" and length > 0) and
  ([.container.index.manifests[].digest] | length == (unique | length)) and
  ([.container.index.manifests[] |
    (.media_type | type == "string" and length > 0) and
    (.digest | type == "string" and test("^sha256:[0-9a-f]{64}$")) and
    (.size | type == "number" and . > 0) and
    (.platform.os | type == "string" and length > 0) and
    (.platform.architecture | type == "string" and length > 0) and
    (.annotations | type == "object")
  ] | all) and
  ([.container.index.manifests[] |
    select(.platform.os == "linux" and
      (.platform.architecture == "amd64" or .platform.architecture == "arm64")) |
    .platform.architecture
  ] | sort == ["amd64", "arm64"])
' "$manifest_path" >/dev/null; then
  fail "invalid preservation manifest: $manifest_path"
fi

working_directory=$(mktemp -d)
trap 'rm -rf -- "$working_directory"' EXIT

tag_response=
release_response=
index_body=
index_digest_file=
registry_token=

if [ -n "$fixture_directory" ]; then
  [ -d "$fixture_directory" ] || fail "fixture directory does not exist: $fixture_directory"
  tag_response="$fixture_directory/github-tag.json"
  release_response="$fixture_directory/github-release.json"
  index_body="$fixture_directory/oci-index.json"
  index_digest_file="$fixture_directory/oci-index.digest"
  for fixture_file in "$tag_response" "$release_response" "$index_body" "$index_digest_file"; do
    [ -f "$fixture_file" ] || fail "fixture file does not exist: $fixture_file"
  done
else
  repository=$(jq -er '.repository' "$manifest_path")
  tag=$(jq -er '.tag.name' "$manifest_path")
  image_repository=$(jq -er '.container.repository' "$manifest_path")

  github_headers=(
    -H 'Accept: application/vnd.github+json'
    -H 'X-GitHub-Api-Version: 2022-11-28'
  )
  if [ -n "${GITHUB_TOKEN-}" ]; then
    github_headers+=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
  fi
  curl_arguments=(
    --fail-with-body
    --silent
    --show-error
    --location
    --proto '=https'
    --tlsv1.2
    --retry 3
    --retry-all-errors
    --connect-timeout 15
    --max-time 90
  )

  tag_response="$working_directory/github-tag.json"
  if ! curl "${curl_arguments[@]}" "${github_headers[@]}" \
    "https://api.github.com/repos/$repository/git/ref/tags/$tag" \
    --output "$tag_response"; then
    fail "GitHub tag API is unavailable for $repository $tag"
  fi

  release_response="$working_directory/github-release.json"
  if ! curl "${curl_arguments[@]}" "${github_headers[@]}" \
    "https://api.github.com/repos/$repository/releases/tags/$tag" \
    --output "$release_response"; then
    fail "GitHub release API is unavailable for $repository $tag"
  fi

  registry_token_response="$working_directory/ghcr-token.json"
  if ! curl "${curl_arguments[@]}" --get \
    --data-urlencode 'service=ghcr.io' \
    --data-urlencode "scope=repository:$image_repository:pull" \
    'https://ghcr.io/token' \
    --output "$registry_token_response"; then
    fail "GHCR token service is unavailable for $image_repository"
  fi
  if ! registry_token=$(jq -er \
    '(.token // .access_token) | select(type == "string" and length > 0)' \
    "$registry_token_response"); then
    fail "GHCR did not issue a public pull token for $image_repository"
  fi

  fetch_registry_manifest() {
    local reference=$1
    local body_path=$2
    local digest_path=$3
    local headers_path=$4
    local remote_digest

    if ! curl "${curl_arguments[@]}" \
      -H "Authorization: Bearer $registry_token" \
      -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json' \
      "https://ghcr.io/v2/$image_repository/manifests/$reference" \
      --dump-header "$headers_path" \
      --output "$body_path"; then
      fail "GHCR manifest is unavailable: $image_repository@$reference"
    fi
    remote_digest=$(awk '
      tolower($1) == "docker-content-digest:" {
        gsub("\\r", "", $2)
        digest = $2
      }
      END { print digest }
    ' "$headers_path")
    if [[ ! "$remote_digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
      fail "GHCR response omitted a valid Docker-Content-Digest for $reference"
    fi
    printf '%s\n' "$remote_digest" >"$digest_path"
  }

  index_body="$working_directory/oci-index.json"
  index_digest_file="$working_directory/oci-index.digest"
  fetch_registry_manifest \
    "$tag" \
    "$index_body" \
    "$index_digest_file" \
    "$working_directory/oci-index.headers"
fi

for response_file in "$tag_response" "$release_response" "$index_body"; do
  jq empty "$response_file" >/dev/null 2>&1 || fail "remote response is not valid JSON: $response_file"
done

expected_tag="$working_directory/expected-tag.json"
actual_tag="$working_directory/actual-tag.json"
jq --sort-keys '{
  ref: ("refs/tags/" + .tag.name),
  object: {
    sha: .tag.commit,
    type: .tag.object_type
  }
}' "$manifest_path" >"$expected_tag"
jq --sort-keys '{
  ref,
  object: {
    sha: .object.sha,
    type: .object.type
  }
}' "$tag_response" >"$actual_tag"
if ! cmp -s "$expected_tag" "$actual_tag"; then
  diff -u "$expected_tag" "$actual_tag" >&2 || true
  fail 'the protected v0.1.6 tag moved or changed type'
fi

release_projection='{
  id,
  node_id,
  tag_name,
  target_commitish,
  name,
  body,
  draft,
  prerelease,
  immutable,
  created_at,
  updated_at,
  published_at,
  author: {
    id: .author.id,
    login: .author.login,
    type: .author.type
  },
  assets: ((.assets // []) | map({
    id,
    name,
    label,
    size,
    digest,
    state,
    content_type,
    created_at,
    updated_at,
    uploader: {
      id: .uploader.id,
      login: .uploader.login,
      type: .uploader.type
    }
  }) | sort_by(.name))
}'
expected_release_source="$working_directory/expected-release-source.json"
expected_release="$working_directory/expected-release.json"
actual_release="$working_directory/actual-release.json"
jq '.github_release' "$manifest_path" >"$expected_release_source"
jq --sort-keys "$release_projection" "$expected_release_source" >"$expected_release"
jq --sort-keys "$release_projection" "$release_response" >"$actual_release"
if ! cmp -s "$expected_release" "$actual_release"; then
  diff -u "$expected_release" "$actual_release" >&2 || true
  fail 'GitHub Release metadata or asset inventory drifted from the preservation manifest'
fi

expected_index_digest=$(jq -er '.container.index.digest' "$manifest_path")
expected_index_size=$(jq -er '.container.index.size' "$manifest_path")
expected_index_media_type=$(jq -er '.container.index.media_type' "$manifest_path")
actual_index_registry_digest=$(tr -d '\r\n' <"$index_digest_file")
actual_index_body_digest="sha256:$(sha256sum "$index_body" | awk '{print $1}')"
actual_index_size=$(wc -c <"$index_body")
actual_index_media_type=$(jq -er '.mediaType' "$index_body")

[ "$actual_index_registry_digest" = "$expected_index_digest" ] || \
  fail "GHCR index header digest drifted: expected $expected_index_digest, got $actual_index_registry_digest"
[ "$actual_index_body_digest" = "$expected_index_digest" ] || \
  fail "GHCR index body digest drifted: expected $expected_index_digest, got $actual_index_body_digest"
[ "$actual_index_size" = "$expected_index_size" ] || \
  fail "GHCR index size drifted: expected $expected_index_size, got $actual_index_size"
[ "$actual_index_media_type" = "$expected_index_media_type" ] || \
  fail "GHCR index media type drifted: expected $expected_index_media_type, got $actual_index_media_type"

expected_descriptors="$working_directory/expected-descriptors.json"
actual_descriptors="$working_directory/actual-descriptors.json"
jq --sort-keys '.container.index.manifests' "$manifest_path" >"$expected_descriptors"
jq --sort-keys '[.manifests[] | {
  media_type: .mediaType,
  digest,
  size,
  platform: {
    os: (.platform.os // null),
    architecture: (.platform.architecture // null),
    variant: (.platform.variant // null)
  },
  annotations: (.annotations // {})
}]' "$index_body" >"$actual_descriptors"
if ! cmp -s "$expected_descriptors" "$actual_descriptors"; then
  diff -u "$expected_descriptors" "$actual_descriptors" >&2 || true
  fail 'GHCR platform or attestation descriptors drifted from the preservation manifest'
fi

while IFS=$'\t' read -r child_digest child_size child_media_type; do
  child_hex=${child_digest#sha256:}
  if [ -n "$fixture_directory" ]; then
    child_body="$fixture_directory/oci-manifests/$child_hex.json"
    child_digest_file="$fixture_directory/oci-manifests/$child_hex.digest"
    [ -f "$child_body" ] || fail "fixture child manifest does not exist: $child_body"
    [ -f "$child_digest_file" ] || fail "fixture child digest does not exist: $child_digest_file"
  else
    child_body="$working_directory/oci-manifest-$child_hex.json"
    child_digest_file="$working_directory/oci-manifest-$child_hex.digest"
    fetch_registry_manifest \
      "$child_digest" \
      "$child_body" \
      "$child_digest_file" \
      "$working_directory/oci-manifest-$child_hex.headers"
  fi

  jq empty "$child_body" >/dev/null 2>&1 || fail "GHCR child manifest is not valid JSON: $child_digest"
  actual_child_registry_digest=$(tr -d '\r\n' <"$child_digest_file")
  actual_child_body_digest="sha256:$(sha256sum "$child_body" | awk '{print $1}')"
  actual_child_size=$(wc -c <"$child_body")
  actual_child_media_type=$(jq -er '.mediaType' "$child_body")
  [ "$actual_child_registry_digest" = "$child_digest" ] || \
    fail "GHCR child header digest drifted: expected $child_digest, got $actual_child_registry_digest"
  [ "$actual_child_body_digest" = "$child_digest" ] || \
    fail "GHCR child body digest drifted: expected $child_digest, got $actual_child_body_digest"
  [ "$actual_child_size" = "$child_size" ] || \
    fail "GHCR child size drifted for $child_digest: expected $child_size, got $actual_child_size"
  [ "$actual_child_media_type" = "$child_media_type" ] || \
    fail "GHCR child media type drifted for $child_digest"
done < <(jq -r '.container.index.manifests[] | [.digest, (.size | tostring), .media_type] | @tsv' "$manifest_path")

echo "verified preserved historical tag, Release assets, and GHCR manifests for $(jq -r '.tag.name' "$manifest_path")"
