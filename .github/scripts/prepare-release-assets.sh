#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: prepare-release-assets.sh \
  --tag TAG \
  --source-dir DIRECTORY \
  --upload-dir DIRECTORY \
  --output FILE
EOF
}

fail() {
  printf 'release asset preparation failed: %s\n' "$1" >&2
  exit 1
}

tag=''
source_dir=''
upload_dir=''
output_file=''

while [ "$#" -gt 0 ]; do
  case "$1" in
    --tag)
      [ "$#" -ge 2 ] || fail '--tag requires a value'
      tag=$2
      shift 2
      ;;
    --source-dir)
      [ "$#" -ge 2 ] || fail '--source-dir requires a value'
      source_dir=$2
      shift 2
      ;;
    --upload-dir)
      [ "$#" -ge 2 ] || fail '--upload-dir requires a value'
      upload_dir=$2
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

command -v gh >/dev/null 2>&1 || fail 'gh is required'
command -v jq >/dev/null 2>&1 || fail 'jq is required'
command -v cmp >/dev/null 2>&1 || fail 'cmp is required'

if [[ ! "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  fail "tag must use stable lowercase v semver: $tag"
fi
if [[ ! "${GITHUB_REPOSITORY:-}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  fail "invalid GITHUB_REPOSITORY: ${GITHUB_REPOSITORY:-}"
fi
[ -d "$source_dir" ] || fail "source directory not found: $source_dir"
[ -n "$upload_dir" ] || fail '--upload-dir is required'
[ -n "$output_file" ] || fail '--output is required'

mkdir -p "$upload_dir" "$(dirname "$output_file")"
if find "$upload_dir" -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
  fail "upload directory must be empty: $upload_dir"
fi

temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT
release_file="$temp_dir/release.json"
release_error="$temp_dir/release.error"
release_exists=false

if gh release view "$tag" \
  --repo "$GITHUB_REPOSITORY" \
  --json isDraft,isPrerelease,tagName,assets > "$release_file" 2> "$release_error"; then
  release_exists=true
elif grep -Fxq 'release not found' "$release_error" || grep -Eq '\(HTTP 404\)$' "$release_error"; then
  printf '{"assets":[]}\n' > "$release_file"
else
  cat "$release_error" >&2
  fail "could not determine whether release $tag exists"
fi

if [ "$release_exists" = true ]; then
  jq -e --arg tag "$tag" '
    .tagName == $tag and
    .isDraft == false and
    .isPrerelease == false and
    (.assets | type) == "array"
  ' "$release_file" >/dev/null || fail "existing release $tag is not a published stable release"
fi

api_base=${GITHUB_API_URL:-https://api.github.com}
api_base=${api_base%/}
asset_count=0
upload_count=0

while IFS= read -r -d '' source_file; do
  asset_count=$((asset_count + 1))
  asset_name=$(basename "$source_file")
  if [[ "$asset_name" == *$'\n'* ]] || [ -z "$asset_name" ]; then
    fail 'release asset names must be non-empty single-line filenames'
  fi

  existing_count=$(jq --arg name "$asset_name" '[.assets[] | select(.name == $name)] | length' "$release_file")
  case "$existing_count" in
    0)
      cp -- "$source_file" "$upload_dir/$asset_name"
      upload_count=$((upload_count + 1))
      ;;
    1)
      api_url=$(jq -r --arg name "$asset_name" '.assets[] | select(.name == $name) | .apiUrl' "$release_file")
      case "$api_url" in
        "$api_base"/repos/*)
          api_endpoint=${api_url#"$api_base"/}
          ;;
        *)
          fail "existing asset $asset_name returned an unexpected API URL"
          ;;
      esac
      downloaded_file="$temp_dir/existing-${asset_count}"
      gh api \
        -H 'Accept: application/octet-stream' \
        "$api_endpoint" > "$downloaded_file"
      if ! cmp -s "$source_file" "$downloaded_file"; then
        fail "existing release asset differs and is immutable: $asset_name"
      fi
      ;;
    *)
      fail "existing release contains duplicate assets named $asset_name"
      ;;
  esac
done < <(find "$source_dir" -mindepth 1 -maxdepth 1 -type f -print0)

[ "$asset_count" -gt 0 ] || fail "no release assets found in $source_dir"

latest_file="$temp_dir/latest.json"
latest_error="$temp_dir/latest.error"
make_latest=true
if gh api "repos/${GITHUB_REPOSITORY}/releases/latest" > "$latest_file" 2> "$latest_error"; then
  latest_tag=$(jq -r '.tag_name // ""' "$latest_file")
  if [[ ! "$latest_tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    fail "current latest release does not use stable lowercase v semver: $latest_tag"
  fi
  highest=$(printf '%s\n%s\n' "$latest_tag" "$tag" | LC_ALL=C sort -V | tail -n 1)
  if [ "$highest" != "$tag" ]; then
    make_latest=false
  fi
elif ! grep -Eq '(Not Found|not found|HTTP 404)' "$latest_error"; then
  cat "$latest_error" >&2
  fail 'could not determine the current latest release'
fi

{
  if [ "$upload_count" -gt 0 ]; then
    echo 'upload_required=true'
  else
    echo 'upload_required=false'
  fi
  echo "make_latest=$make_latest"
  echo "asset_count=$asset_count"
  echo "upload_count=$upload_count"
} >> "$output_file"
