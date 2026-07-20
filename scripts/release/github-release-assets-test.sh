#!/usr/bin/env bash

set -Eeuo pipefail

repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
helper="$repository_root/scripts/release/github-release-assets.sh"
test_directory=$(mktemp -d)
trap 'rm -rf -- "$test_directory"' EXIT

notes_file="$test_directory/v0.2.0.md"
request_file="$test_directory/request.json"
post_sentinel="$test_directory/unexpected-post"
printf '# v0.2.0\n' >"$notes_file"

export TEST_GH_MODE=patch
export TEST_GH_REQUEST_FILE="$request_file"
export TEST_GH_POST_SENTINEL="$post_sentinel"
export TEST_UPLOAD_STATE="$test_directory/uploaded"
export TEST_CURL_COUNT="$test_directory/curl-count"
export TEST_CURL_MODE=confirmed
export TEST_ASSET_FILE="$test_directory/example.bin"
export TEST_DOWNLOAD_COUNT="$test_directory/download-count"
export TEST_DOWNLOAD_MODE=stable
printf 'asset-bytes\n' >"$TEST_ASSET_FILE"
gh() {
  if [ "${1-}" != api ]; then
    echo 'unexpected gh command' >&2
    return 2
  fi
  shift

  local method=GET
  local input_file=
  local endpoint=
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --method)
        method=$2
        shift 2
        ;;
      --input)
        input_file=$2
        shift 2
        ;;
      -F|-f|-H|--jq)
        shift 2
        ;;
      *)
        endpoint=$1
        shift
        ;;
    esac
  done

  case "$method:$endpoint" in
    PATCH:/repos/acme/project/releases/356446756)
      cp -- "$input_file" "$TEST_GH_REQUEST_FILE"
      jq --argjson release_id 356446756 '
        . + {
          id: $release_id,
          immutable: (if .draft then false else true end),
          assets: []
        }' "$input_file"
      ;;
    GET:/repos/acme/project/releases)
      case "$TEST_GH_MODE" in
        canonical)
          jq --null-input '[{
            id: 356446756,
            tag_name: "v0.2.0",
            name: "v0.2.0",
            draft: true,
            immutable: false
          }]'
          ;;
        detached)
          jq --null-input '[{
            id: 356446756,
            tag_name: "untagged-39ecaac0fa083a1449f2",
            name: "v0.2.0",
            draft: true,
            immutable: false
          }]'
          ;;
        *)
          printf '[]\n'
          ;;
      esac
      ;;
    GET:/repos/acme/project/releases/123)
      assets='[]'
      if [ -e "$TEST_UPLOAD_STATE" ]; then
        asset_size=$(stat --format '%s' "$TEST_ASSET_FILE")
        asset_digest="sha256:$(sha256sum "$TEST_ASSET_FILE" | awk '{print $1}')"
        assets=$(jq --compact-output --null-input \
          --arg digest "$asset_digest" \
          --argjson size "$asset_size" '[{
            id: 700,
            name: "example.bin",
            size: $size,
            digest: $digest,
            state: "uploaded"
          }]')
        if [ "$TEST_GH_MODE" = upload-race ]; then
          assets=$(jq --compact-output --null-input \
            --argjson current "$assets" '
              $current + [{
                id: 701,
                name: "concurrent.bin",
                size: 1,
                digest: ("sha256:" + ("a" * 64)),
                state: "uploaded"
              }]')
        fi
      fi
      jq --null-input \
        --argjson assets "$assets" '{
          id: 123,
          tag_name: "v0.2.0",
          target_commitish: "main",
          name: "v0.2.0",
          body: "notes",
          draft: true,
          prerelease: false,
          immutable: false,
          upload_url: "https://uploads.github.com/repos/acme/project/releases/123/assets{?name,label}",
          assets: $assets
        }'
      ;;
    GET:/repos/acme/project/releases/124)
      download_count=0
      if [ -f "$TEST_DOWNLOAD_COUNT" ]; then
        download_count=$(<"$TEST_DOWNLOAD_COUNT")
      fi
      printf '%s\n' "$((download_count + 1))" >"$TEST_DOWNLOAD_COUNT"
      download_asset_id=700
      if [ "$TEST_DOWNLOAD_MODE" = race ] && [ "$download_count" -ge 1 ]; then
        download_asset_id=702
      fi
      asset_size=$(stat --format '%s' "$TEST_ASSET_FILE")
      asset_digest="sha256:$(sha256sum "$TEST_ASSET_FILE" | awk '{print $1}')"
      jq --null-input \
        --arg digest "$asset_digest" \
        --argjson asset_id "$download_asset_id" \
        --argjson size "$asset_size" '{
          id: 124,
          tag_name: "v0.2.0",
          target_commitish: "main",
          name: "v0.2.0",
          body: "notes",
          draft: true,
          prerelease: false,
          immutable: false,
          assets: [{
            id: $asset_id,
            name: "example.bin",
            size: $size,
            digest: $digest,
            state: "uploaded"
          }]
        }'
      ;;
    GET:/repos/acme/project/releases/assets/700)
      command cat -- "$TEST_ASSET_FILE"
      ;;
    POST:/repos/acme/project/releases)
      : >"$TEST_GH_POST_SENTINEL"
      echo 'unexpected release creation' >&2
      return 1
      ;;
    *)
      echo "unexpected gh api request: $method $endpoint" >&2
      return 2
      ;;
  esac
}
export -f gh
curl() {
  local data_argument=
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --data-binary)
        data_argument=$2
        shift 2
        ;;
      --header|--request)
        shift 2
        ;;
      --fail-with-body|--silent|--show-error)
        shift
        ;;
      *)
        shift
        ;;
    esac
  done
  upload_file=${data_argument#@}
  upload_size=$(stat --format '%s' "$upload_file")
  upload_digest="sha256:$(sha256sum "$upload_file" | awk '{print $1}')"
  upload_count=0
  if [ -f "$TEST_CURL_COUNT" ]; then
    upload_count=$(<"$TEST_CURL_COUNT")
  fi
  printf '%s\n' "$((upload_count + 1))" >"$TEST_CURL_COUNT"
  : >"$TEST_UPLOAD_STATE"
  if [ "$TEST_CURL_MODE" = unconfirmed ]; then
    return 28
  fi
  jq --null-input \
    --arg digest "$upload_digest" \
    --argjson size "$upload_size" '{
      id: 700,
      name: "example.bin",
      size: $size,
      digest: $digest,
      state: "uploaded"
    }'
}
export -f curl

"$helper" update-draft \
  acme/project \
  356446756 \
  v0.2.0 \
  ea3c0ef6cccddcf252f14f03abb11b2b74366233 \
  "$notes_file" >/dev/null
if ! jq --exit-status \
  --rawfile body "$notes_file" '
    (keys | sort) == [
      "body",
      "draft",
      "make_latest",
      "name",
      "prerelease",
      "tag_name",
      "target_commitish"
    ] and
    .tag_name == "v0.2.0" and
    .target_commitish == "ea3c0ef6cccddcf252f14f03abb11b2b74366233" and
    .name == "v0.2.0" and
    .body == $body and
    .draft == true and
    .prerelease == false and
    .make_latest == "false"' "$request_file" >/dev/null; then
  echo 'update-draft did not send the complete release contract' >&2
  exit 1
fi

"$helper" publish \
  acme/project \
  356446756 \
  v0.2.0 \
  ea3c0ef6cccddcf252f14f03abb11b2b74366233 \
  "$notes_file" >/dev/null
if ! jq --exit-status '
  .tag_name == "v0.2.0" and
  .draft == false and
  .prerelease == false and
  .make_latest == "true"' "$request_file" >/dev/null; then
  echo 'publish did not preserve the tag in the complete release contract' >&2
  exit 1
fi

export TEST_GH_MODE=canonical
canonical_release=$("$helper" resolve acme/project v0.2.0)
detached_release=$("$helper" resolve-detached acme/project v0.2.0)
if [ "$(jq --raw-output '.id' <<<"$canonical_release")" != 356446756 ] || \
   [ "$detached_release" != null ]; then
  echo 'canonical release resolution is inconsistent' >&2
  exit 1
fi

export TEST_GH_MODE=detached
canonical_release=$("$helper" resolve acme/project v0.2.0)
detached_release=$("$helper" resolve-detached acme/project v0.2.0)
if [ "$canonical_release" != null ] || \
   [ "$(jq --raw-output '.id' <<<"$detached_release")" != 356446756 ]; then
  echo 'detached release resolution is inconsistent' >&2
  exit 1
fi
if "$helper" create-draft \
  acme/project \
  v0.2.0 \
  ea3c0ef6cccddcf252f14f03abb11b2b74366233 \
  "$notes_file" >/dev/null 2>&1; then
  echo 'create-draft accepted a duplicate detached draft' >&2
  exit 1
fi
if [ -e "$post_sentinel" ]; then
  echo 'create-draft attempted to create a release despite the detached conflict' >&2
  exit 1
fi

export TEST_GH_MODE=upload
export TEST_CURL_MODE=confirmed
env GH_TOKEN=test "$helper" upload \
  acme/project 123 "$TEST_ASSET_FILE" >/dev/null
if [ "$(<"$TEST_CURL_COUNT")" != 1 ]; then
  echo 'a confirmed asset upload was not sent exactly once' >&2
  exit 1
fi

rm -f -- "$TEST_UPLOAD_STATE" "$TEST_CURL_COUNT"
export TEST_GH_MODE=upload-race
if env GH_TOKEN=test "$helper" upload \
  acme/project 123 "$TEST_ASSET_FILE" >/dev/null 2>&1; then
  echo 'asset upload accepted a concurrent inventory change' >&2
  exit 1
fi
if [ "$(<"$TEST_CURL_COUNT")" != 1 ]; then
  echo 'a raced asset upload was retried' >&2
  exit 1
fi

rm -f -- "$TEST_UPLOAD_STATE" "$TEST_CURL_COUNT"
export TEST_GH_MODE=upload
export TEST_CURL_MODE=unconfirmed
if env GH_TOKEN=test "$helper" upload \
  acme/project 123 "$TEST_ASSET_FILE" >/dev/null 2>&1; then
  echo 'asset upload accepted an unauthoritative response' >&2
  exit 1
fi
if [ "$(<"$TEST_CURL_COUNT")" != 1 ]; then
  echo 'an unauthoritative asset upload was retried' >&2
  exit 1
fi

export TEST_DOWNLOAD_MODE=stable
download_destination="$test_directory/downloaded.bin"
"$helper" download \
  acme/project 124 example.bin "$download_destination"
if ! cmp -s "$TEST_ASSET_FILE" "$download_destination" || \
   [ "$(<"$TEST_DOWNLOAD_COUNT")" != 2 ]; then
  echo 'stable asset download was not verified before and after transfer' >&2
  exit 1
fi

rm -f -- "$TEST_DOWNLOAD_COUNT"
export TEST_DOWNLOAD_MODE=race
raced_download_destination="$test_directory/raced-download.bin"
if "$helper" download \
  acme/project 124 example.bin "$raced_download_destination" \
  >/dev/null 2>&1; then
  echo 'asset download accepted a concurrent identity change' >&2
  exit 1
fi
if [ -e "$raced_download_destination" ]; then
  echo 'raced asset download exposed an unverified destination' >&2
  exit 1
fi

echo 'verified complete release metadata, detached drafts, and race-safe assets'
