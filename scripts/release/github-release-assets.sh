#!/usr/bin/env bash

set -Eeuo pipefail

usage() {
  cat >&2 <<'EOF'
usage:
  github-release-assets.sh resolve <owner/repository> <tag>
  github-release-assets.sh resolve-detached <owner/repository> <tag>
  github-release-assets.sh create-draft <owner/repository> <tag> <commit> <notes-file>
  github-release-assets.sh update-draft <owner/repository> <release-id> <tag> <commit> <notes-file>
  github-release-assets.sh publish <owner/repository> <release-id> <tag> <commit> <notes-file>
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
  if [[ ! "$tag" =~ ^v0\.2\.(0|[1-9][0-9]*)$ ]]; then
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

release_metadata_fingerprint() {
  jq --compact-output '{
    id: .id,
    tag_name: .tag_name,
    target_commitish: .target_commitish,
    name: .name,
    body: .body,
    draft: .draft,
    prerelease: .prerelease,
    immutable: .immutable
  }' | sha256sum | awk '{print $1}'
}

release_asset_inventory() {
  jq --compact-output '[.assets[] | {
    id: .id,
    name: .name,
    size: .size,
    digest: .digest,
    state: .state
  }] | sort_by(.name)'
}

collect_release_matches() {
  local repository=$1
  local tag=$2
  local match_kind=$3
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
    case "$match_kind" in
      tag)
        page_matches=$(jq --compact-output --arg tag "$tag" \
          '[.[] | select(.tag_name == $tag)]' <<<"$page_json")
        ;;
      detached)
        page_matches=$(jq --compact-output --arg tag "$tag" '
          [.[] | select(
            .draft == true and
            .immutable != true and
            .name == $tag and
            (.tag_name | test("^untagged-[0-9a-f]{7,64}$"))
          )]' <<<"$page_json")
        ;;
      *)
        echo "invalid release match kind: $match_kind" >&2
        exit 2
        ;;
    esac
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

  jq --compact-output '.' <<<"$matches"
}

resolve_release() {
  local repository=$1
  local tag=$2
  local matches

  matches=$(collect_release_matches "$repository" "$tag" tag)
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

resolve_detached_release() {
  local repository=$1
  local tag=$2
  local matches

  matches=$(collect_release_matches "$repository" "$tag" detached)
  case $(jq 'length' <<<"$matches") in
    0)
      printf 'null\n'
      ;;
    1)
      jq --compact-output '.[0]' <<<"$matches"
      ;;
    *)
      echo "multiple detached GitHub drafts are named $tag; refusing an ambiguous mutation" >&2
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
  local detached_release

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
  detached_release=$(resolve_detached_release "$repository" "$tag")
  if [ "$detached_release" != null ]; then
    echo "a detached GitHub draft is already named $tag; refusing to create a duplicate release" >&2
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

update_release_metadata() (
  local repository=$1
  local release_id=$2
  local tag=$3
  local commit=$4
  local notes_file=$5
  local draft=$6
  local make_latest=$7
  local request_file
  local response

  validate_repository "$repository"
  validate_release_id "$release_id"
  validate_tag "$tag"
  if [[ ! "$commit" =~ ^[0-9a-f]{40}$ ]]; then
    echo "invalid release commit: $commit" >&2
    exit 2
  fi
  if [ ! -f "$notes_file" ] || [ -L "$notes_file" ] || [ ! -s "$notes_file" ]; then
    echo "release notes must be a non-empty regular file: $notes_file" >&2
    exit 1
  fi
  if { [ "$draft" != true ] && [ "$draft" != false ]; } || \
     { [ "$make_latest" != true ] && [ "$make_latest" != false ]; } || \
     { [ "$draft" = true ] && [ "$make_latest" != false ]; } || \
     { [ "$draft" = false ] && [ "$make_latest" != true ]; }; then
    echo 'invalid release publication state' >&2
    exit 2
  fi

  request_file=$(mktemp)
  trap 'rm -f "$request_file"' EXIT
  jq --null-input \
    --arg tag "$tag" \
    --arg commit "$commit" \
    --rawfile body "$notes_file" \
    --argjson draft "$draft" \
    --arg make_latest "$make_latest" \
    '{
      tag_name: $tag,
      target_commitish: $commit,
      name: $tag,
      body: $body,
      draft: $draft,
      prerelease: false,
      make_latest: $make_latest
    }' >"$request_file"
  response=$(gh api \
    --method PATCH \
    "/repos/${repository}/releases/${release_id}" \
    --input "$request_file")
  if ! jq --exit-status \
    --argjson release_id "$release_id" \
    --arg tag "$tag" \
    --arg commit "$commit" \
    --rawfile body "$notes_file" \
    --argjson draft "$draft" '
      .id == $release_id and
      .tag_name == $tag and
      .name == $tag and
      .body == $body and
      .draft == $draft and
      .prerelease == false and
      (.target_commitish == "main" or
       .target_commitish == $tag or
       .target_commitish == $commit) and
      (($draft == false) or .immutable != true)' \
    <<<"$response" >/dev/null; then
    echo 'GitHub updated the release with unexpected metadata.' >&2
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
  local expected_state
  local destination_directory
  local temporary_file
  local actual_size
  local actual_digest
  local initial_metadata_fingerprint
  local initial_asset_inventory
  local final_release_json
  local final_metadata_fingerprint
  local final_asset_inventory

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
  initial_metadata_fingerprint=$(printf '%s' "$release_json" | \
    release_metadata_fingerprint)
  initial_asset_inventory=$(printf '%s' "$release_json" | \
    release_asset_inventory)
  asset_json=$(jq --compact-output --arg name "$asset_name" \
    '[.assets[] | select(.name == $name)]' <<<"$release_json")
  if [ "$(jq 'length' <<<"$asset_json")" -ne 1 ]; then
    echo "expected exactly one release asset named $asset_name" >&2
    exit 1
  fi
  asset_id=$(jq --raw-output '.[0].id' <<<"$asset_json")
  expected_size=$(jq --raw-output '.[0].size' <<<"$asset_json")
  expected_digest=$(jq --raw-output '.[0].digest' <<<"$asset_json")
  expected_state=$(jq --raw-output '.[0].state' <<<"$asset_json")
  if [[ ! "$asset_id" =~ ^[1-9][0-9]*$ ]] || \
     [[ ! "$expected_size" =~ ^[1-9][0-9]*$ ]] || \
     [[ ! "$expected_digest" =~ ^sha256:[0-9a-f]{64}$ ]] || \
     [ "$expected_state" != uploaded ]; then
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
  final_release_json=$(gh api "/repos/${repository}/releases/${release_id}")
  final_metadata_fingerprint=$(printf '%s' "$final_release_json" | \
    release_metadata_fingerprint)
  final_asset_inventory=$(printf '%s' "$final_release_json" | \
    release_asset_inventory)
  if [ "$initial_metadata_fingerprint" != "$final_metadata_fingerprint" ] || \
     [ "$initial_asset_inventory" != "$final_asset_inventory" ]; then
    echo "release metadata or assets changed while downloading: $asset_name" >&2
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
  local reconciled_asset
  local matching_assets
  local expected_size
  local expected_digest
  local initial_metadata_fingerprint
  local initial_asset_inventory
  local final_release_json
  local final_metadata_fingerprint
  local final_asset_inventory
  local expected_final_inventory
  local response_confirmed=false
  local confirmed_asset_identity=null
  local reconciled_asset_identity

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
    '.id == $release_id and
     .draft == true and
     .immutable != true and
     .prerelease == false and
     (.tag_name |
       test("^v0\\.2\\.(0|[1-9][0-9]*)$")) and
     .name == .tag_name and
     (.body | type == "string" and length > 0) and
     ([.assets[].name] | length) == ([.assets[].name] | unique | length) and
     all(.assets[];
       (.id | type == "number" and . > 0) and
       .state == "uploaded" and
       (.size | type == "number" and . > 0) and
       (.digest |
         type == "string" and test("^sha256:[0-9a-f]{64}$")))' \
    <<<"$release_json" >/dev/null; then
    echo 'release assets may only be uploaded to an exact canonical mutable draft.' >&2
    exit 1
  fi
  initial_metadata_fingerprint=$(printf '%s' "$release_json" | \
    release_metadata_fingerprint)
  initial_asset_inventory=$(printf '%s' "$release_json" | \
    release_asset_inventory)
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
    response_confirmed=true
    confirmed_asset_identity=$(jq --compact-output '{
      id: .id,
      name: .name,
      size: .size,
      digest: .digest,
      state: .state
    }' "$response_file")
  fi

  for _ in $(seq 1 15); do
    final_release_json=$(gh api "/repos/${repository}/releases/${release_id}")
    final_metadata_fingerprint=$(printf '%s' "$final_release_json" | \
      release_metadata_fingerprint)
    if [ "$initial_metadata_fingerprint" != "$final_metadata_fingerprint" ]; then
      echo "release metadata changed while uploading: $asset_name" >&2
      exit 1
    fi
    matching_assets=$(jq --compact-output --arg name "$asset_name" \
      '[.assets[] | select(.name == $name)]' <<<"$final_release_json")
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
          reconciled_asset_identity=$(jq --compact-output '{
            id: .id,
            name: .name,
            size: .size,
            digest: .digest,
            state: .state
          }' <<<"$reconciled_asset")
          final_asset_inventory=$(printf '%s' "$final_release_json" | \
            release_asset_inventory)
          expected_final_inventory=$(jq --compact-output --null-input \
            --argjson initial "$initial_asset_inventory" \
            --argjson asset "$reconciled_asset_identity" \
            '$initial + [$asset] | sort_by(.name)')
          if [ "$final_asset_inventory" != "$expected_final_inventory" ]; then
            echo "release assets changed concurrently while uploading: $asset_name" >&2
            exit 1
          fi
          if [ "$response_confirmed" = true ]; then
            if [ "$reconciled_asset_identity" != "$confirmed_asset_identity" ]; then
              echo "GitHub listed a different asset than the confirmed upload: $asset_name" >&2
              exit 1
            fi
            jq --compact-output '.' "$response_file"
            return
          fi
          echo "GitHub stored the expected asset but the upload response was not authoritative; rerun without retrying in this process: $asset_name" >&2
          exit 1
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
  if [ "$response_confirmed" = true ]; then
    echo "GitHub did not expose the confirmed upload as the only release delta: $asset_name" >&2
  else
    echo "GitHub did not confirm the uploaded release asset; the POST was not retried: $asset_name" >&2
  fi
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
  resolve-detached)
    [ "$#" -eq 2 ] || usage
    resolve_detached_release "$1" "$2"
    ;;
  create-draft)
    [ "$#" -eq 4 ] || usage
    create_draft "$1" "$2" "$3" "$4"
    ;;
  update-draft)
    [ "$#" -eq 5 ] || usage
    update_release_metadata "$1" "$2" "$3" "$4" "$5" true false
    ;;
  publish)
    [ "$#" -eq 5 ] || usage
    update_release_metadata "$1" "$2" "$3" "$4" "$5" false true
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
