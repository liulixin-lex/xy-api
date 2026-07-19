#!/usr/bin/env bash

set -Eeuo pipefail

usage() {
  cat >&2 <<'EOF'
usage:
  github-release-assets.sh resolve <owner/repository> <tag>
  github-release-assets.sh create-draft <owner/repository> <tag> <commit> <notes-file>
  github-release-assets.sh download <owner/repository> <release-id> <asset-name> <destination>
  github-release-assets.sh upload <owner/repository> <release-id> <file>
EOF
  exit 2
}

validate_repository() {
  local repository=$1
  if [[ ! "$repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
    echo "invalid GitHub repository: $repository" >&2
    exit 2
  fi
}

validate_tag() {
  local tag=$1
  if [[ ! "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "invalid stable release tag: $tag" >&2
    exit 2
  fi
}

validate_release_id() {
  local release_id=$1
  if [[ ! "$release_id" =~ ^[1-9][0-9]*$ ]]; then
    echo "invalid GitHub release id: $release_id" >&2
    exit 2
  fi
}

resolve_release() {
  local repository=$1
  local tag=$2
  local matches='[]'
  local page

  validate_repository "$repository"
  validate_tag "$tag"

  for page in $(seq 1 1000); do
    local page_json
    local page_length
    local page_matches
    page_json=$(gh api \
      --method GET \
      "/repos/${repository}/releases" \
      -F per_page=100 \
      -F page="$page")
    if ! jq --exit-status 'type == "array"' <<<"$page_json" >/dev/null; then
      echo 'GitHub returned an invalid release listing.' >&2
      exit 1
    fi
    page_length=$(jq 'length' <<<"$page_json")
    page_matches=$(jq --compact-output --arg tag "$tag" \
      '[.[] | select(.tag_name == $tag)]' <<<"$page_json")
    matches=$(jq --compact-output --null-input \
      --argjson current "$matches" \
      --argjson additions "$page_matches" \
      '$current + $additions')

    if [ "$page_length" -lt 100 ]; then
      break
    fi
    if [ "$page" -eq 1000 ]; then
      echo 'GitHub release pagination exceeded the safety limit.' >&2
      exit 1
    fi
  done

  case $(jq 'length' <<<"$matches") in
    0)
      printf 'null\n'
      ;;
    1)
      jq --compact-output '.[0]' <<<"$matches"
      ;;
    *)
      echo "multiple GitHub releases use tag $tag; refusing an ambiguous mutation" >&2
      exit 1
      ;;
  esac
}

create_draft() (
  local repository=$1
  local tag=$2
  local commit=$3
  local notes_file=$4
  local request_file
  local response
  local existing_release

  validate_repository "$repository"
  validate_tag "$tag"
  if [[ ! "$commit" =~ ^[0-9a-f]{40}$ ]]; then
    echo "invalid release commit: $commit" >&2
    exit 2
  fi
  if [ ! -f "$notes_file" ] || [ -L "$notes_file" ] || [ ! -s "$notes_file" ]; then
    echo "release notes must be a non-empty regular file: $notes_file" >&2
    exit 1
  fi
  existing_release=$(resolve_release "$repository" "$tag")
  if [ "$existing_release" != null ]; then
    echo "a GitHub release already exists for $tag" >&2
    exit 1
  fi

  request_file=$(mktemp)
  trap 'rm -f "$request_file"' EXIT
  jq --null-input \
    --arg tag "$tag" \
    --arg commit "$commit" \
    --rawfile body "$notes_file" \
    '{
      tag_name: $tag,
      target_commitish: $commit,
      name: $tag,
      body: $body,
      draft: true,
      prerelease: false,
      make_latest: "false"
    }' >"$request_file"
  response=$(gh api \
    --method POST \
    "/repos/${repository}/releases" \
    --input "$request_file")
  if ! jq --exit-status \
    --arg tag "$tag" \
    --rawfile body "$notes_file" \
    '.tag_name == $tag and
     .name == $tag and
     .body == $body and
     .draft == true and
     .prerelease == false and
     .immutable != true and
     (.id | type == "number" and . > 0)' \
    <<<"$response" >/dev/null; then
    echo 'GitHub created a draft that does not match the requested release contract.' >&2
    exit 1
  fi
  jq --compact-output '.' <<<"$response"
)

download_asset() (
  local repository=$1
  local release_id=$2
  local asset_name=$3
  local destination=$4
  local release_json
  local asset_json
  local asset_id
  local expected_size
  local expected_digest
  local destination_directory
  local temporary_file
  local actual_size
  local actual_digest

  validate_repository "$repository"
  validate_release_id "$release_id"
  if [ -z "$asset_name" ] || [ "$asset_name" != "${asset_name##*/}" ]; then
    echo "invalid release asset name: $asset_name" >&2
    exit 2
  fi
  if [ -e "$destination" ] || [ -L "$destination" ]; then
    echo "release asset destination already exists: $destination" >&2
    exit 1
  fi
  destination_directory=$(dirname "$destination")
  if [ ! -d "$destination_directory" ]; then
    echo "release asset destination directory does not exist: $destination_directory" >&2
    exit 1
  fi

  release_json=$(gh api "/repos/${repository}/releases/${release_id}")
  asset_json=$(jq --compact-output --arg name "$asset_name" \
    '[.assets[] | select(.name == $name)]' <<<"$release_json")
  if [ "$(jq 'length' <<<"$asset_json")" -ne 1 ]; then
    echo "expected exactly one release asset named $asset_name" >&2
    exit 1
  fi
  asset_id=$(jq --raw-output '.[0].id' <<<"$asset_json")
  expected_size=$(jq --raw-output '.[0].size' <<<"$asset_json")
  expected_digest=$(jq --raw-output '.[0].digest' <<<"$asset_json")
  if [[ ! "$asset_id" =~ ^[1-9][0-9]*$ ]] || \
     [[ ! "$expected_size" =~ ^[1-9][0-9]*$ ]] || \
     [[ ! "$expected_digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "release asset metadata is incomplete for $asset_name" >&2
    exit 1
  fi

  temporary_file=$(mktemp "${destination}.tmp.XXXXXX")
  trap 'rm -f "$temporary_file"' EXIT
  gh api \
    -H 'Accept: application/octet-stream' \
    "/repos/${repository}/releases/assets/${asset_id}" >"$temporary_file"
  actual_size=$(stat --format '%s' "$temporary_file")
  actual_digest="sha256:$(sha256sum "$temporary_file" | awk '{print $1}')"
  if [ "$actual_size" != "$expected_size" ] || \
     [ "$actual_digest" != "$expected_digest" ]; then
    echo "downloaded release asset failed size or digest verification: $asset_name" >&2
    exit 1
  fi
  mv -- "$temporary_file" "$destination"
)

upload_asset() (
  local repository=$1
  local release_id=$2
  local file=$3
  local asset_name
  local release_json
  local upload_template
  local expected_upload_template
  local upload_url
  local encoded_name
  local token
  local response_file
  local curl_status
  local reconciliation_directory
  local reconciled_file
  local reconciled_asset
  local matching_assets
  local reconciled_asset_id
  local reconciled_size
  local reconciled_digest
  local expected_size
  local expected_digest

  validate_repository "$repository"
  validate_release_id "$release_id"
  if [ ! -f "$file" ] || [ -L "$file" ] || [ ! -s "$file" ]; then
    echo "release asset must be a non-empty regular file: $file" >&2
    exit 1
  fi
  asset_name=${file##*/}
  release_json=$(gh api "/repos/${repository}/releases/${release_id}")
  if ! jq --exit-status \
    --argjson release_id "$release_id" \
    '.id == $release_id and .draft == true and .immutable != true' \
    <<<"$release_json" >/dev/null; then
    echo 'release assets may only be uploaded to the exact mutable draft.' >&2
    exit 1
  fi
  if jq --exit-status --arg name "$asset_name" \
    '.assets | any(.name == $name)' <<<"$release_json" >/dev/null; then
    echo "refusing to overwrite existing release asset: $asset_name" >&2
    exit 1
  fi

  upload_template=$(jq --raw-output '.upload_url' <<<"$release_json")
  expected_upload_template="https://uploads.github.com/repos/${repository}/releases/${release_id}/assets{?name,label}"
  if [ "$upload_template" != "$expected_upload_template" ]; then
    echo 'GitHub returned an unexpected release upload URL.' >&2
    exit 1
  fi
  upload_url=${upload_template%%\{*}
  encoded_name=$(jq --null-input --raw-output --arg name "$asset_name" '$name | @uri')
  token=${GH_TOKEN:-${GITHUB_TOKEN:-}}
  if [ -z "$token" ]; then
    echo 'GH_TOKEN or GITHUB_TOKEN is required to upload a release asset.' >&2
    exit 1
  fi

  response_file=$(mktemp)
  trap 'rm -f "$response_file"' EXIT
  set +e
  curl \
    --fail-with-body \
    --silent \
    --show-error \
    --request POST \
    --header 'Accept: application/vnd.github+json' \
    --header "Authorization: Bearer $token" \
    --header 'X-GitHub-Api-Version: 2022-11-28' \
    --header 'Content-Type: application/octet-stream' \
    --data-binary "@$file" \
    "${upload_url}?name=${encoded_name}" >"$response_file"
  curl_status=$?
  set -e

  expected_size=$(stat --format '%s' "$file")
  expected_digest="sha256:$(sha256sum "$file" | awk '{print $1}')"
  if [ "$curl_status" -eq 0 ] && jq --exit-status \
    --arg name "$asset_name" \
    --arg digest "$expected_digest" \
    --argjson size "$expected_size" \
    '.name == $name and
     .size == $size and
     .digest == $digest and
     .state == "uploaded" and
     (.id | type == "number" and . > 0)' \
    "$response_file" >/dev/null; then
    jq --compact-output '.' "$response_file"
    return
  fi

  reconciliation_directory=$(mktemp -d)
  trap 'rm -f "$response_file"; rm -rf "$reconciliation_directory"' EXIT
  reconciled_file="$reconciliation_directory/$asset_name"
  for _ in $(seq 1 15); do
    release_json=$(gh api "/repos/${repository}/releases/${release_id}")
    matching_assets=$(jq --compact-output --arg name "$asset_name" \
      '[.assets[] | select(.name == $name)]' <<<"$release_json")
    case $(jq 'length' <<<"$matching_assets") in
      0)
        sleep 2
        continue
        ;;
      1)
        reconciled_asset=$(jq --compact-output '.[0]' <<<"$matching_assets")
        if jq --exit-status \
          --arg digest "$expected_digest" \
          --argjson size "$expected_size" \
          '.size == $size and
           .digest == $digest and
           .state == "uploaded" and
           (.id | type == "number" and . > 0)' \
          <<<"$reconciled_asset" >/dev/null; then
          reconciled_asset_id=$(jq --raw-output '.id' <<<"$reconciled_asset")
          gh api \
            -H 'Accept: application/octet-stream' \
            "/repos/${repository}/releases/assets/${reconciled_asset_id}" \
            >"$reconciled_file"
          reconciled_size=$(stat --format '%s' "$reconciled_file")
          reconciled_digest="sha256:$(sha256sum "$reconciled_file" | awk '{print $1}')"
          if [ "$reconciled_size" != "$expected_size" ] || \
             [ "$reconciled_digest" != "$expected_digest" ] || \
             ! cmp -s "$file" "$reconciled_file"; then
            echo "reconciled release asset differs from the uploaded file: $asset_name" >&2
            exit 1
          fi
          jq --compact-output '.' <<<"$reconciled_asset"
          return
        fi
        if jq --exit-status --argjson size "$expected_size" '
          (.digest == null or .digest == "") and
          (.state == "new" or .state == "uploaded") and
          .size == $size' <<<"$reconciled_asset" >/dev/null; then
          sleep 2
          continue
        fi
        echo "GitHub exposed conflicting metadata for release asset: $asset_name" >&2
        exit 1
        ;;
      *)
        echo "GitHub exposed duplicate release assets named: $asset_name" >&2
        exit 1
        ;;
    esac
  done
  echo "GitHub did not confirm the uploaded release asset; the POST was not retried: $asset_name" >&2
  exit 1
)

if [ "$#" -lt 1 ]; then
  usage
fi

command=$1
shift
case "$command" in
  resolve)
    [ "$#" -eq 2 ] || usage
    resolve_release "$1" "$2"
    ;;
  create-draft)
    [ "$#" -eq 4 ] || usage
    create_draft "$1" "$2" "$3" "$4"
    ;;
  download)
    [ "$#" -eq 4 ] || usage
    download_asset "$1" "$2" "$3" "$4"
    ;;
  upload)
    [ "$#" -eq 3 ] || usage
    upload_asset "$1" "$2" "$3"
    ;;
  *)
    usage
    ;;
esac
