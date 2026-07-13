#!/usr/bin/env bash

set -euo pipefail

fail() {
  printf 'release asset finalization failed: %s\n' "$1" >&2
  exit 1
}

tag=''
verify_only=false
requested_download_dir=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --tag)
      [ "$#" -ge 2 ] || fail '--tag requires a value'
      tag=$2
      shift 2
      ;;
    --verify-only)
      verify_only=true
      shift
      ;;
    --download-dir)
      [ "$#" -ge 2 ] || fail '--download-dir requires a value'
      requested_download_dir=$2
      shift 2
      ;;
    --help|-h)
      printf 'Usage: finalize-release-assets.sh --tag TAG [--verify-only --download-dir DIRECTORY]\n'
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

command -v gh >/dev/null 2>&1 || fail 'gh is required'
command -v jq >/dev/null 2>&1 || fail 'jq is required'
command -v sha256sum >/dev/null 2>&1 || fail 'sha256sum is required'
if [[ ! "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  fail "tag must use stable lowercase v semver: $tag"
fi
if [[ ! "${GITHUB_REPOSITORY:-}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  fail "invalid GITHUB_REPOSITORY: ${GITHUB_REPOSITORY:-}"
fi
if [ "$verify_only" = true ]; then
  [ -n "$requested_download_dir" ] || fail '--verify-only requires --download-dir'
elif [ -n "$requested_download_dir" ]; then
  fail '--download-dir requires --verify-only'
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
version=${tag#v}
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT
release_file="$temp_dir/release.json"
expected_file="$temp_dir/expected-assets.txt"
actual_file="$temp_dir/actual-assets.txt"
missing_file="$temp_dir/missing-assets.txt"
unexpected_file="$temp_dir/unexpected-assets.txt"

printf '%s\n' \
  checksums-electron-windows.txt \
  checksums-linux.txt \
  checksums-macos.txt \
  checksums-windows.txt \
  "New-API-App.${version}.exe" \
  "New-API-App.Setup.${version}.exe" \
  "new-api-${tag}" \
  "new-api-arm64-${tag}" \
  "new-api-macos-${tag}" \
  "new-api-${tag}.exe" | LC_ALL=C sort > "$expected_file"

gh release view "$tag" \
  --repo "$GITHUB_REPOSITORY" \
  --json apiUrl,isDraft,isPrerelease,tagName,assets > "$release_file" ||
  fail "could not read release $tag"
jq -e --arg tag "$tag" '
  .tagName == $tag and
  (.isDraft | type) == "boolean" and
  .isPrerelease == false and
  (.apiUrl | type == "string" and length > 0) and
  (.assets | type) == "array" and
  all(.assets[];
    (.name | type) == "string" and
    (.name | length) > 0 and
    (.name | contains("\n") | not))
' "$release_file" >/dev/null || fail "release $tag is not a valid stable release or draft"
if [ "$verify_only" = true ] && ! jq -e '.isDraft == false' "$release_file" >/dev/null; then
  fail "release $tag must already be published before read-only verification"
fi

jq -r '.assets[].name' "$release_file" | LC_ALL=C sort > "$actual_file"
if [ -n "$(uniq -d "$actual_file")" ]; then
  fail "release $tag contains duplicate asset names"
fi
comm -23 "$expected_file" "$actual_file" > "$missing_file"
comm -13 "$expected_file" "$actual_file" > "$unexpected_file"
if [ -s "$unexpected_file" ]; then
  cat "$unexpected_file" >&2
  fail "release $tag contains unexpected assets"
fi
if [ -s "$missing_file" ]; then
  if jq -e '.isDraft == false' "$release_file" >/dev/null; then
    cat "$missing_file" >&2
    fail "published release $tag is missing required assets"
  fi
  printf 'release %s remains draft; waiting for required assets:\n' "$tag"
  sed 's/^/  - /' "$missing_file"
  exit 0
fi

verified_assets_dir="$temp_dir/assets"
mkdir -p "$verified_assets_dir"
gh release download "$tag" --repo "$GITHUB_REPOSITORY" --dir "$verified_assets_dir"
find "$verified_assets_dir" -mindepth 1 -maxdepth 1 -type f -printf '%f\n' |
  LC_ALL=C sort > "$temp_dir/downloaded-assets.txt"
if ! cmp -s "$expected_file" "$temp_dir/downloaded-assets.txt"; then
  fail 'downloaded release asset inventory changed during finalization'
fi

validate_checksum_inventory() {
  local checksum_file=$1
  shift
  local expected
  local actual
  expected="$temp_dir/$(basename "$checksum_file").expected"
  actual="$temp_dir/$(basename "$checksum_file").actual"
  printf '%s\n' "$@" | LC_ALL=C sort > "$expected"
  awk '
    length($0) >= 67 &&
    substr($0, 1, 64) ~ /^[0-9a-f]+$/ &&
    (substr($0, 65, 2) == "  " || substr($0, 65, 2) == " *") {
      name = substr($0, 67)
      sub(/^\.\//, "", name)
      print name
      next
    }
    { exit 1 }
  ' "$verified_assets_dir/$checksum_file" | LC_ALL=C sort > "$actual" ||
    fail "invalid checksum syntax in $checksum_file"
  if ! cmp -s "$expected" "$actual"; then
    fail "checksum inventory does not match required assets: $checksum_file"
  fi
}

validate_checksum_inventory checksums-linux.txt \
  "new-api-${tag}" \
  "new-api-arm64-${tag}"
validate_checksum_inventory checksums-macos.txt "new-api-macos-${tag}"
validate_checksum_inventory checksums-windows.txt "new-api-${tag}.exe"
validate_checksum_inventory checksums-electron-windows.txt \
  "New-API-App.${version}.exe" \
  "New-API-App.Setup.${version}.exe"

(
  cd "$verified_assets_dir"
  sha256sum --check --strict checksums-linux.txt
  sha256sum --check --strict checksums-macos.txt
  sha256sum --check --strict checksums-windows.txt
  sha256sum --check --strict checksums-electron-windows.txt
) >/dev/null

if [ "$verify_only" = true ]; then
  if [ -e "$requested_download_dir" ] &&
    [ -n "$(find "$requested_download_dir" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]; then
    fail "download directory must be empty: $requested_download_dir"
  fi
  mkdir -p "$requested_download_dir"
  while IFS= read -r asset_name; do
    cp -p -- "$verified_assets_dir/$asset_name" "$requested_download_dir/$asset_name"
  done < "$expected_file"
  printf 'release %s has the complete verified 10-asset inventory (read-only)\n' "$tag"
  exit 0
fi

if jq -e '.isDraft == true' "$release_file" >/dev/null; then
  api_base=${GITHUB_API_URL:-https://api.github.com}
  api_base=${api_base%/}
  api_url=$(jq -r '.apiUrl' "$release_file")
  case "$api_url" in
    "$api_base/repos/$GITHUB_REPOSITORY/releases/"*)
      api_endpoint=${api_url#"$api_base/"}
      ;;
    *)
      fail 'release API URL is outside the current repository'
      ;;
  esac
  gh api --method PATCH "$api_endpoint" \
    -F draft=false \
    -f make_latest=false > "$temp_dir/published-release.json"
  jq -e --arg tag "$tag" '
    .tag_name == $tag and .draft == false and .prerelease == false
  ' "$temp_dir/published-release.json" >/dev/null ||
    fail "GitHub did not publish stable release $tag"
fi

"$script_dir/ensure-release-latest.sh" --tag "$tag"
printf 'release %s has the complete verified 10-asset inventory\n' "$tag"
