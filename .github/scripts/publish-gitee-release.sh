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
      printf '%s\n' 'Usage: publish-gitee-release.sh --owner OWNER --repository REPOSITORY --tag TAG --name-file FILE --body-file FILE --target-file FILE [--assets-dir DIRECTORY]'
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

command -v curl >/dev/null 2>&1 || fail 'curl is required'
command -v jq >/dev/null 2>&1 || fail 'jq is required'
command -v cmp >/dev/null 2>&1 || fail 'cmp is required'
if [[ ! "$owner" =~ ^[A-Za-z0-9_.-]+$ ]] || [[ ! "$repository" =~ ^[A-Za-z0-9_.-]+$ ]]; then
  fail 'owner and repository must use valid Gitee path components'
fi
if [[ ! "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  fail "tag must use stable lowercase v semver: $tag"
fi
[ -f "$name_file" ] || fail "release name file not found: $name_file"
[ -f "$body_file" ] || fail "release body file not found: $body_file"
[ -f "$target_file" ] || fail "target commit file not found: $target_file"
if [ -n "$assets_dir" ] && [ ! -d "$assets_dir" ]; then
  fail "asset directory not found: $assets_dir"
fi
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
token_file="$temp_dir/token"
printf '%s' "$GITEE_TOKEN" > "$token_file"
unset GITEE_TOKEN
api_base="https://gitee.com/api/v5/repos/${owner}/${repository}"
release_file="$temp_dir/release.json"

get_status=$(curl --silent --show-error \
  --output "$release_file" \
  --write-out '%{http_code}' \
  --get \
  --data-urlencode "access_token@${token_file}" \
  "${api_base}/releases/tags/${tag}") || fail 'could not query the existing Gitee release'

case "$get_status" in
  200)
    jq -e \
      --arg tag "$tag" \
      --arg name "$release_name" \
      --rawfile body "$body_file" \
      --arg target "$target_commit" '
      .tag_name == $tag and
      .name == $name and
      (.body // "") == $body and
      .target_commitish == $target and
      ((.id | tostring) | test("^[1-9][0-9]*$"))
    ' "$release_file" >/dev/null || fail 'existing Gitee release differs from the verified GitHub release'
    ;;
  404)
    create_file="$temp_dir/create.json"
    create_status=$(curl --silent --show-error \
      --output "$create_file" \
      --write-out '%{http_code}' \
      --request POST \
      --data-urlencode "access_token@${token_file}" \
      --data-urlencode "tag_name=${tag}" \
      --data-urlencode "name=${release_name}" \
      --data-urlencode "body@${body_file}" \
      --data-urlencode "target_commitish=${target_commit}" \
      "${api_base}/releases") || fail 'could not create the Gitee release'
    if [ "$create_status" != 200 ] && [ "$create_status" != 201 ]; then
      cat "$create_file" >&2
      fail "Gitee release creation returned HTTP $create_status"
    fi
    mv "$create_file" "$release_file"
    jq -e \
      --arg tag "$tag" \
      --arg target "$target_commit" '
      .tag_name == $tag and
      .target_commitish == $target and
      ((.id | tostring) | test("^[1-9][0-9]*$"))
    ' "$release_file" >/dev/null || fail 'Gitee returned an invalid created release'
    ;;
  *)
    cat "$release_file" >&2
    fail "Gitee release lookup returned HTTP $get_status"
    ;;
esac

release_id=$(jq -r '.id | tostring' "$release_file")
[ -n "$assets_dir" ] || {
  printf 'Gitee release %s is ready with no uploaded assets\n' "$tag"
  exit 0
}

attachments_file="$temp_dir/attachments.json"
attachments_status=$(curl --silent --show-error \
  --output "$attachments_file" \
  --write-out '%{http_code}' \
  --get \
  --data-urlencode "access_token@${token_file}" \
  --data-urlencode 'per_page=100' \
  "${api_base}/releases/${release_id}/attach_files") || fail 'could not query Gitee release attachments'
if [ "$attachments_status" != 200 ]; then
  cat "$attachments_file" >&2
  fail "Gitee attachment lookup returned HTTP $attachments_status"
fi
jq -e '
  type == "array" and
  all(.[];
    (.name | type) == "string" and
    (.name | length) > 0 and
    (.name | contains("\n") | not))
' "$attachments_file" >/dev/null || fail 'Gitee returned an invalid attachment inventory'

asset_count=0
while IFS= read -r -d '' asset_file; do
  asset_count=$((asset_count + 1))
  asset_name=$(basename "$asset_file")
  if [[ "$asset_name" == *$'\n'* ]] || [ -z "$asset_name" ]; then
    fail 'asset names must be non-empty single-line filenames'
  fi
  existing_count=$(jq --arg name "$asset_name" '[.[] | select(.name == $name)] | length' "$attachments_file")
  case "$existing_count" in
    0)
      upload_file="$temp_dir/upload-${asset_count}.json"
      upload_status=$(curl --silent --show-error \
        --output "$upload_file" \
        --write-out '%{http_code}' \
        --request POST \
        --form "access_token=<${token_file}" \
        --form "file=@${asset_file};type=application/octet-stream" \
        "${api_base}/releases/${release_id}/attach_files") || fail "could not upload Gitee asset $asset_name"
      if [[ ! "$upload_status" =~ ^20[01]$ ]]; then
        cat "$upload_file" >&2
        fail "Gitee asset upload returned HTTP $upload_status for $asset_name"
      fi
      jq -e --arg name "$asset_name" '
        .name == $name and
        (.browser_download_url | type == "string" and startswith("https://gitee.com/"))
      ' "$upload_file" >/dev/null || fail "Gitee returned invalid upload evidence for $asset_name"
      ;;
    1)
      download_url=$(jq -r --arg name "$asset_name" '.[] | select(.name == $name) | .browser_download_url' "$attachments_file")
      case "$download_url" in
        https://gitee.com/*) ;;
        *) fail "existing Gitee asset returned an unsafe download URL: $asset_name" ;;
      esac
      downloaded_file="$temp_dir/existing-${asset_count}"
      curl --fail --silent --show-error --location \
        --output "$downloaded_file" \
        --get \
        --data-urlencode "access_token@${token_file}" \
        "$download_url"
      if ! cmp -s "$asset_file" "$downloaded_file"; then
        fail "existing Gitee asset differs and is immutable: $asset_name"
      fi
      ;;
    *)
      fail "Gitee release contains duplicate assets named $asset_name"
      ;;
  esac
done < <(find "$assets_dir" -mindepth 1 -maxdepth 1 -type f -print0)

[ "$asset_count" -gt 0 ] || fail "no release assets found in $assets_dir"
printf 'Gitee release %s is synchronized with %s verified assets\n' "$tag" "$asset_count"
