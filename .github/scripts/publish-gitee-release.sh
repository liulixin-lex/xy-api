#!/usr/bin/env bash

set -euo pipefail

fail() {
  printf 'Gitee release publication failed: %s\n' "$1" >&2
  exit 1
}

owner=''
repository=''
tag=''
name_file=''
body_file=''
target_file=''
assets_dir=''

while [ "$#" -gt 0 ]; do
  case "$1" in
    --owner)
      [ "$#" -ge 2 ] || fail '--owner requires a value'
      owner=$2
      shift 2
      ;;
    --repository)
      [ "$#" -ge 2 ] || fail '--repository requires a value'
      repository=$2
      shift 2
      ;;
    --tag)
      [ "$#" -ge 2 ] || fail '--tag requires a value'
      tag=$2
      shift 2
      ;;
    --name-file)
      [ "$#" -ge 2 ] || fail '--name-file requires a value'
      name_file=$2
      shift 2
      ;;
    --body-file)
      [ "$#" -ge 2 ] || fail '--body-file requires a value'
      body_file=$2
      shift 2
      ;;
    --target-file)
      [ "$#" -ge 2 ] || fail '--target-file requires a value'
      target_file=$2
      shift 2
      ;;
    --assets-dir)
      [ "$#" -ge 2 ] || fail '--assets-dir requires a value'
      assets_dir=$2
      shift 2
      ;;
    --help|-h)
      printf '%s\n' 'Usage: publish-gitee-release.sh --owner OWNER --repository REPOSITORY --tag TAG --name-file FILE --body-file FILE --target-file FILE --assets-dir DIRECTORY'
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

command -v cmp >/dev/null 2>&1 || fail 'cmp is required'
command -v curl >/dev/null 2>&1 || fail 'curl is required'
command -v jq >/dev/null 2>&1 || fail 'jq is required'
if [[ ! "$owner" =~ ^[A-Za-z0-9_.-]+$ ]] || [[ ! "$repository" =~ ^[A-Za-z0-9_.-]+$ ]]; then
  fail 'owner and repository must use valid Gitee path components'
fi
if [[ ! "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  fail "tag must use stable lowercase v semver: $tag"
fi
[ -f "$name_file" ] || fail "release name file not found: $name_file"
[ -f "$body_file" ] || fail "release body file not found: $body_file"
[ -f "$target_file" ] || fail "target commit file not found: $target_file"
[ -d "$assets_dir" ] || fail "asset directory not found: $assets_dir"
if [ -z "${GITEE_TOKEN:-}" ]; then
  fail 'GITEE_TOKEN is required'
fi
if [[ "$GITEE_TOKEN" == *$'\n'* ]] || [[ "$GITEE_TOKEN" == *$'\r'* ]]; then
  fail 'GITEE_TOKEN must be a single-line value'
fi

release_name=$(<"$name_file")
[ -n "$release_name" ] || fail 'release name must not be empty'
if [[ "$release_name" == *$'\n'* ]] || [[ "$release_name" == *$'\r'* ]]; then
  fail 'release name must be a single-line value'
fi
target_commit=$(<"$target_file")
if [[ ! "$target_commit" =~ ^[0-9a-f]{40}$ ]]; then
  fail 'target commit must be one full Git SHA'
fi

umask 077
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT
header_file="$temp_dir/authorization.header"
printf 'Authorization: Bearer %s\n' "$GITEE_TOKEN" > "$header_file"
chmod 600 "$header_file"
unset GITEE_TOKEN
if [ "$(stat -c '%a' "$header_file")" != 600 ]; then
  fail 'authorization header file must use mode 600'
fi

api_base="https://gitee.com/api/v5/repos/${owner}/${repository}"
expected_assets="$temp_dir/expected-assets.txt"
actual_assets="$temp_dir/actual-assets.txt"
version=${tag#v}
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
  "new-api-${tag}.exe" | LC_ALL=C sort > "$expected_assets"
find "$assets_dir" -mindepth 1 -maxdepth 1 -type f -printf '%f\n' |
  LC_ALL=C sort > "$actual_assets"
if ! cmp -s "$expected_assets" "$actual_assets"; then
  fail 'local Gitee asset inventory must exactly match the verified 10-asset GitHub release'
fi
while IFS= read -r asset_name; do
  [ -s "$assets_dir/$asset_name" ] || fail "release asset must be non-empty: $asset_name"
done < "$expected_assets"

api_status=''
api_request() {
  local output_file=$1
  local operation=$2
  local method=$3
  local url=$4
  local retry_args=()
  shift 4
  if [ "$method" = GET ]; then
    retry_args=(--retry 3 --retry-all-errors --retry-delay 1)
  fi
  if ! api_status=$(curl \
    --silent \
    --show-error \
    --output "$output_file" \
    --write-out '%{http_code}' \
    --request "$method" \
    --header "@$header_file" \
    "${retry_args[@]}" \
    "$@" \
    "$url"); then
    fail "could not complete Gitee $operation"
  fi
  if [[ ! "$api_status" =~ ^[0-9]{3}$ ]]; then
    fail "Gitee $operation returned an invalid HTTP status"
  fi
}

fetch_pages() {
  local endpoint=$1
  local output_file=$2
  local operation=$3
  local page_file="$temp_dir/page.json"
  local merged_file="$temp_dir/pages-merged.json"
  local next_file="$temp_dir/pages-next.json"
  local count
  printf '[]\n' > "$merged_file"
  for page in $(seq 1 1000); do
    api_request "$page_file" "$operation" GET "${api_base}/${endpoint}" \
      --get \
      --data-urlencode "page=${page}" \
      --data-urlencode 'per_page=100'
    [ "$api_status" = 200 ] || fail "Gitee $operation returned HTTP $api_status"
    jq -e 'type == "array"' "$page_file" >/dev/null ||
      fail "Gitee $operation returned an invalid page"
    count=$(jq 'length' "$page_file")
    if [ "$count" -eq 0 ]; then
      cp "$merged_file" "$output_file"
      return 0
    fi
    jq -s '.[0] + .[1]' "$merged_file" "$page_file" > "$next_file"
    mv "$next_file" "$merged_file"
  done
  fail "Gitee $operation exceeded the pagination safety limit"
}

all_releases="$temp_dir/all-releases.json"
reject_higher_stable_release() {
  fetch_pages releases "$all_releases" 'release listing'
  jq -e '
    all(.[];
      (.tag_name | type) == "string" and
      (.prerelease | type) == "boolean")
  ' "$all_releases" >/dev/null || fail 'Gitee returned an invalid release listing'
  while IFS= read -r existing_tag; do
    if [[ "$existing_tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
      highest=$(printf '%s\n%s\n' "$tag" "$existing_tag" | LC_ALL=C sort -V | tail -n 1)
      if [ "$highest" = "$existing_tag" ] && [ "$existing_tag" != "$tag" ]; then
        fail "a newer published stable Gitee release already exists: $existing_tag"
      fi
    fi
  done < <(jq -r '.[] | select(.prerelease == false) | .tag_name' "$all_releases")
}

validate_release() {
  local release_file=$1
  local expected_prerelease=$2
  jq -e \
    --arg tag "$tag" \
    --arg name "$release_name" \
    --rawfile body "$body_file" \
    --arg target "$target_commit" \
    --argjson prerelease "$expected_prerelease" '
    .tag_name == $tag and
    .name == $name and
    (.body // "") == $body and
    .target_commitish == $target and
    .prerelease == $prerelease and
    ((.id | tostring) | test("^[1-9][0-9]*$"))
  ' "$release_file" >/dev/null ||
    fail 'Gitee release state differs from the verified GitHub release'
}

reject_higher_stable_release
release_file="$temp_dir/release.json"
api_request "$release_file" 'release lookup' GET "${api_base}/releases/tags/${tag}"
case "$api_status" in
  200)
    jq -e '(.prerelease | type) == "boolean"' "$release_file" >/dev/null ||
      fail 'Gitee release lookup returned an invalid state'
    release_prerelease=$(jq -r '.prerelease' "$release_file")
    validate_release "$release_file" "$release_prerelease"
    ;;
  404)
    create_file="$temp_dir/create.json"
    api_request "$create_file" 'release staging creation' POST "${api_base}/releases" \
      --data-urlencode "tag_name=${tag}" \
      --data-urlencode "name=${release_name}" \
      --data-urlencode "body@${body_file}" \
      --data-urlencode "target_commitish=${target_commit}" \
      --data-urlencode 'prerelease=true'
    case "$api_status" in
      200|201) ;;
      *) fail "Gitee release staging creation returned HTTP $api_status" ;;
    esac
    api_request "$release_file" 'created release lookup' GET "${api_base}/releases/tags/${tag}"
    [ "$api_status" = 200 ] || fail "Gitee created release lookup returned HTTP $api_status"
    release_prerelease=true
    validate_release "$release_file" true
    ;;
  *)
    fail "Gitee release lookup returned HTTP $api_status"
    ;;
esac
release_id=$(jq -r '.id | tostring' "$release_file")

validate_attachments() {
  local attachments_file=$1
  jq -e '
    type == "array" and
    all(.[];
      ((.id | tostring) | test("^[1-9][0-9]*$")) and
      (.name | type == "string" and length > 0 and (contains("\n") | not)) and
      (.browser_download_url | type == "string" and startswith("https://gitee.com/")))
  ' "$attachments_file" >/dev/null || fail 'Gitee returned an invalid attachment inventory'
  jq -r '.[].name' "$attachments_file" | LC_ALL=C sort > "$temp_dir/remote-assets.txt"
  if [ -n "$(uniq -d "$temp_dir/remote-assets.txt")" ]; then
    fail 'Gitee release contains duplicate attachment names'
  fi
  comm -13 "$expected_assets" "$temp_dir/remote-assets.txt" > "$temp_dir/unexpected-remote-assets.txt"
  [ ! -s "$temp_dir/unexpected-remote-assets.txt" ] ||
    fail 'Gitee release contains unexpected attachments'
}

attachments_file="$temp_dir/attachments.json"
fetch_pages "releases/${release_id}/attach_files" "$attachments_file" 'attachment listing'
validate_attachments "$attachments_file"
if [ "$release_prerelease" = false ] && ! cmp -s "$expected_assets" "$temp_dir/remote-assets.txt"; then
  fail 'published Gitee release has an incomplete immutable attachment inventory'
fi

while IFS= read -r asset_name; do
  if ! grep -Fxq "$asset_name" "$temp_dir/remote-assets.txt"; then
    upload_file="$temp_dir/upload.json"
    api_request "$upload_file" "asset upload for $asset_name" POST \
      "${api_base}/releases/${release_id}/attach_files" \
      --form "file=@${assets_dir}/${asset_name};filename=${asset_name};type=application/octet-stream"
    case "$api_status" in
      200|201) ;;
      *) fail "Gitee asset upload returned HTTP $api_status for $asset_name" ;;
    esac
    jq -e --arg name "$asset_name" '
      .name == $name and
      (.browser_download_url | type == "string" and startswith("https://gitee.com/"))
    ' "$upload_file" >/dev/null || fail "Gitee returned invalid upload evidence for $asset_name"
  fi
done < "$expected_assets"

fetch_pages "releases/${release_id}/attach_files" "$attachments_file" 'post-upload attachment listing'
validate_attachments "$attachments_file"
if ! cmp -s "$expected_assets" "$temp_dir/remote-assets.txt"; then
  fail 'Gitee attachment inventory is incomplete after upload'
fi

asset_number=0
while IFS= read -r asset_name; do
  asset_number=$((asset_number + 1))
  download_url=$(jq -r --arg name "$asset_name" '.[] | select(.name == $name) | .browser_download_url' "$attachments_file")
  case "$download_url" in
    https://gitee.com/*) ;;
    *) fail "Gitee asset returned an unsafe public download URL: $asset_name" ;;
  esac
  downloaded_file="$temp_dir/downloaded-${asset_number}"
  if ! curl \
    --fail \
    --silent \
    --show-error \
    --location \
    --retry 3 \
    --retry-all-errors \
    --retry-delay 1 \
    --proto '=https' \
    --proto-redir '=https' \
    --output "$downloaded_file" \
    "$download_url"; then
    fail "could not publicly download Gitee asset $asset_name"
  fi
  if ! cmp -s "$assets_dir/$asset_name" "$downloaded_file"; then
    fail "Gitee asset bytes differ from the verified GitHub asset: $asset_name"
  fi
done < "$expected_assets"

reject_higher_stable_release
if [ "$release_prerelease" = true ]; then
  publish_file="$temp_dir/publish.json"
  api_request "$publish_file" 'release publication' PATCH "${api_base}/releases/${release_id}" \
    --data-urlencode "tag_name=${tag}" \
    --data-urlencode "name=${release_name}" \
    --data-urlencode "body@${body_file}" \
    --data-urlencode "target_commitish=${target_commit}" \
    --data-urlencode 'prerelease=false'
  [ "$api_status" = 200 ] || fail "Gitee release publication returned HTTP $api_status"
fi

api_request "$release_file" 'published release lookup' GET "${api_base}/releases/tags/${tag}"
[ "$api_status" = 200 ] || fail "Gitee published release lookup returned HTTP $api_status"
validate_release "$release_file" false

latest_file="$temp_dir/latest.json"
api_request "$latest_file" 'latest release lookup' GET "${api_base}/releases/latest"
case "$api_status" in
  200)
    latest_tag=$(jq -r '.tag_name // ""' "$latest_file")
    if [[ ! "$latest_tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
      fail 'Gitee latest release does not expose a stable version tag'
    fi
    highest=$(printf '%s\n%s\n' "$tag" "$latest_tag" | LC_ALL=C sort -V | tail -n 1)
    [ "$highest" = "$latest_tag" ] || fail 'Gitee latest release regressed after publication'
    ;;
  404) ;;
  *) fail "Gitee latest release lookup returned HTTP $api_status" ;;
esac

printf 'Gitee release %s is published with the exact verified 10-asset inventory\n' "$tag"
