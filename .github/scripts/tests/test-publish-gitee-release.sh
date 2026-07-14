#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../../.." && pwd)
publisher=$(cd -- "$script_dir/.." && pwd)/publish-gitee-release.sh
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT

mkdir -p "$temp_dir/bin" "$temp_dir/assets" "$temp_dir/state/remote"
cat > "$temp_dir/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

output=''
write_out=false
method=GET
header_path=''
page=1
asset_file=''
asset_name=''
prerelease_value=''
args=("$@")
for arg in "${args[@]}"; do
  if [[ "$arg" == *"$MOCK_EXPECTED_TOKEN"* ]]; then
    echo 'token leaked into curl argv' >&2
    exit 90
  fi
done
for ((index = 0; index < ${#args[@]}; index++)); do
  case "${args[$index]}" in
    --output)
      output=${args[$((index + 1))]}
      index=$((index + 1))
      ;;
    --write-out)
      write_out=true
      index=$((index + 1))
      ;;
    --request)
      method=${args[$((index + 1))]}
      index=$((index + 1))
      ;;
    --header)
      header_path=${args[$((index + 1))]}
      header_path=${header_path#@}
      index=$((index + 1))
      ;;
    --data-urlencode)
      value=${args[$((index + 1))]}
      case "$value" in
        page=*) page=${value#page=} ;;
        prerelease=*) prerelease_value=${value#prerelease=} ;;
      esac
      index=$((index + 1))
      ;;
    --form)
      value=${args[$((index + 1))]}
      if [[ "$value" == file=@* ]]; then
        asset_file=${value#file=@}
        asset_file=${asset_file%%;filename=*}
        asset_name=${value#*;filename=}
        asset_name=${asset_name%%;type=*}
      fi
      index=$((index + 1))
      ;;
  esac
done
url=${args[$((${#args[@]} - 1))]}

if [[ "$url" == https://gitee.com/api/v5/* ]]; then
  [ -n "$header_path" ] || { echo 'missing API authorization header file' >&2; exit 91; }
  [ "$(stat -c '%a' "$header_path")" = 600 ] || { echo 'unsafe header mode' >&2; exit 92; }
  [ "$(<"$header_path")" = "Authorization: Bearer $MOCK_EXPECTED_TOKEN" ] || {
    echo 'wrong authorization header' >&2
    exit 93
  }
  printf 'API %s %s page=%s header_mode=600 prerelease=%s\n' \
    "$method" "$url" "$page" "$prerelease_value" >> "$MOCK_CALLS"
else
  [ -z "$header_path" ] || { echo 'public download received authorization header' >&2; exit 94; }
  printf 'PUBLIC %s %s no_auth=true\n' "$method" "$url" >> "$MOCK_CALLS"
fi

if [[ "$url" == https://gitee.com/download/* ]]; then
  cp "$MOCK_STATE_DIR/remote/${url##*/}" "$output"
  exit 0
fi

state=$(<"$MOCK_STATE_DIR/release-state")
higher=$(<"$MOCK_STATE_DIR/higher")
status=200

write_release() {
  local prerelease=$1
  jq -n \
    --argjson prerelease "$prerelease" '
    {
      id: 101,
      tag_name: "v0.1.11",
      name: "Release 0.1.11",
      body: "notes\nline",
      target_commitish: "1111111111111111111111111111111111111111",
      prerelease: $prerelease
    }
  ' > "$output"
}

write_attachment_page() {
  local page_size=4
  local start=$(((page - 1) * page_size))
  local end=$((start + page_size))
  local line_number=0
  local emitted=0
  local items_file="${output}.items"
  : > "$items_file"
  while IFS= read -r name; do
    [ -n "$name" ] || continue
    if [ "$line_number" -ge "$start" ] && [ "$line_number" -lt "$end" ]; then
      jq -n \
        --argjson id "$((201 + line_number))" \
        --arg name "$name" \
        --arg url "https://gitee.com/download/${name}" \
        '{id: $id, name: $name, browser_download_url: $url}' >> "$items_file"
      emitted=$((emitted + 1))
    fi
    line_number=$((line_number + 1))
  done < "$MOCK_STATE_DIR/attachments"
  if [ "$emitted" -eq 0 ]; then
    printf '[]\n' > "$output"
  else
    jq -s '.' "$items_file" > "$output"
  fi
  rm -f "$items_file"
}

case "$url" in
  */releases)
    if [ "$method" = GET ]; then
      if [ "${MOCK_API_ERROR:-false}" = true ]; then
        status=503
        printf '%s\n' 'SENSITIVE_EXTERNAL_ERROR_BODY' > "$output"
      elif [ "$page" -gt 1 ]; then
        printf '[]\n' > "$output"
      elif [ "$higher" = true ]; then
        jq -n '[{id: 999, tag_name: "v0.1.12", prerelease: false}]' > "$output"
      elif [ "$state" = missing ]; then
        printf '[]\n' > "$output"
      else
        jq -n --argjson prerelease "$([ "$state" = staging ] && echo true || echo false)" \
          '[{id: 101, tag_name: "v0.1.11", prerelease: $prerelease}]' > "$output"
      fi
    else
      [ "$method" = POST ] || exit 95
      [ "$prerelease_value" = true ] || { echo 'release was not created as staging' >&2; exit 96; }
      printf 'staging\n' > "$MOCK_STATE_DIR/release-state"
      status=201
      write_release true
    fi
    ;;
  */releases/tags/v0.1.11)
    case "$state" in
      missing)
        status=404
        printf '%s\n' 'SENSITIVE_NOT_FOUND_BODY' > "$output"
        ;;
      staging) write_release true ;;
      published) write_release false ;;
    esac
    ;;
  */releases/101/attach_files)
    if [ "$method" = POST ]; then
      [ -n "$asset_file" ] && [ -n "$asset_name" ] || exit 97
      cp "$asset_file" "$MOCK_STATE_DIR/remote/$asset_name"
      if [ "$asset_name" != "${MOCK_DROP_UPLOAD_NAME:-}" ]; then
        printf '%s\n' "$asset_name" >> "$MOCK_STATE_DIR/attachments"
      fi
      status=201
      jq -n \
        --arg name "$asset_name" \
        --arg url "https://gitee.com/download/${asset_name}" \
        '{id: 301, name: $name, browser_download_url: $url}' > "$output"
    else
      write_attachment_page
    fi
    ;;
  */releases/101)
    [ "$method" = PATCH ] || exit 98
    [ "$prerelease_value" = false ] || { echo 'release was not finalized' >&2; exit 99; }
    printf 'published\n' > "$MOCK_STATE_DIR/release-state"
    write_release false
    ;;
  */releases/latest)
    if [ "$higher" = true ]; then
      jq -n '{tag_name: "v0.1.12"}' > "$output"
    else
      jq -n '{tag_name: "v0.1.11"}' > "$output"
    fi
    ;;
  *)
    echo "unexpected curl URL: $url" >&2
    exit 100
    ;;
esac

if [ "$write_out" = true ]; then
  printf '%s' "$status"
fi
EOF
chmod +x "$temp_dir/bin/curl"

export PATH="$temp_dir/bin:$PATH"
export GITEE_TOKEN='test-token-value'
export MOCK_EXPECTED_TOKEN="$GITEE_TOKEN"
export MOCK_CALLS="$temp_dir/calls"
export MOCK_STATE_DIR="$temp_dir/state"

printf 'Release 0.1.11' > "$temp_dir/name.txt"
printf 'notes\nline' > "$temp_dir/body.txt"
printf '1111111111111111111111111111111111111111' > "$temp_dir/target.txt"

expected_names() {
  printf '%s\n' \
    checksums-electron-windows.txt \
    checksums-linux.txt \
    checksums-macos.txt \
    checksums-windows.txt \
    New-API-App.0.1.11.exe \
    New-API-App.Setup.0.1.11.exe \
    new-api-v0.1.11 \
    new-api-arm64-v0.1.11 \
    new-api-macos-v0.1.11 \
    new-api-v0.1.11.exe | LC_ALL=C sort
}

while IFS= read -r name; do
  printf 'verified bytes for %s\n' "$name" > "$temp_dir/assets/$name"
done < <(expected_names)

reset_state() {
  local release_state=$1
  printf '%s\n' "$release_state" > "$MOCK_STATE_DIR/release-state"
  printf 'false\n' > "$MOCK_STATE_DIR/higher"
  : > "$MOCK_STATE_DIR/attachments"
  rm -f "$MOCK_STATE_DIR/remote"/*
  : > "$MOCK_CALLS"
  unset MOCK_API_ERROR MOCK_DROP_UPLOAD_NAME
}

seed_remote_assets() {
  while IFS= read -r name; do
    printf '%s\n' "$name" >> "$MOCK_STATE_DIR/attachments"
    cp "$temp_dir/assets/$name" "$MOCK_STATE_DIR/remote/$name"
  done < <(expected_names)
}

run_publisher() {
  "$publisher" \
    --owner example \
    --repository xy-api \
    --tag v0.1.11 \
    --name-file "$temp_dir/name.txt" \
    --body-file "$temp_dir/body.txt" \
    --target-file "$temp_dir/target.txt" \
    --assets-dir "$temp_dir/assets"
}

expect_failure() {
  local name=$1
  shift
  if "$@" > "$temp_dir/${name}.stdout" 2> "$temp_dir/${name}.stderr"; then
    printf 'expected failure for %s\n' "$name" >&2
    exit 1
  fi
}

reset_state missing
run_publisher > "$temp_dir/create.stdout"
grep -Fq 'exact verified 10-asset inventory' "$temp_dir/create.stdout"
[ "$(<"$MOCK_STATE_DIR/release-state")" = published ]
[ "$(grep -c 'API POST .*/attach_files' "$MOCK_CALLS")" -eq 10 ]
grep -Fq 'API POST https://gitee.com/api/v5/repos/example/xy-api/releases page=1 header_mode=600 prerelease=true' "$MOCK_CALLS"
grep -Fq 'API PATCH https://gitee.com/api/v5/repos/example/xy-api/releases/101 page=1 header_mode=600 prerelease=false' "$MOCK_CALLS"
[ "$(grep -c 'API GET https://gitee.com/api/v5/repos/example/xy-api/releases/tags/v0.1.11' "$MOCK_CALLS")" -eq 3 ]
grep -Fq 'attach_files page=3 header_mode=600' "$MOCK_CALLS"
[ "$(grep -c '^PUBLIC .* no_auth=true$' "$MOCK_CALLS")" -eq 10 ]
last_public_line=$(grep -n '^PUBLIC ' "$MOCK_CALLS" | tail -n 1 | cut -d: -f1)
publish_line=$(grep -n '^API PATCH .*releases/101' "$MOCK_CALLS" | cut -d: -f1)
[ "$last_public_line" -lt "$publish_line" ]
if grep -Fq "$MOCK_EXPECTED_TOKEN" "$MOCK_CALLS"; then
  echo 'token leaked into recorded curl arguments' >&2
  exit 1
fi

reset_state published
seed_remote_assets
run_publisher > "$temp_dir/existing.stdout"
[ "$(grep -c 'API POST .*/attach_files' "$MOCK_CALLS" || true)" -eq 0 ]
[ "$(grep -c 'API PATCH .*releases/101' "$MOCK_CALLS" || true)" -eq 0 ]

reset_state staging
seed_remote_assets
printf '%s\n' 'new-api-v0.1.11' >> "$MOCK_STATE_DIR/attachments"
expect_failure duplicate run_publisher
grep -Fq 'duplicate attachment names' "$temp_dir/duplicate.stderr"
[ "$(<"$MOCK_STATE_DIR/release-state")" = staging ]

reset_state staging
seed_remote_assets
printf 'unexpected bytes\n' > "$MOCK_STATE_DIR/remote/unexpected.bin"
printf 'unexpected.bin\n' >> "$MOCK_STATE_DIR/attachments"
expect_failure unexpected run_publisher
grep -Fq 'unexpected attachments' "$temp_dir/unexpected.stderr"

reset_state staging
export MOCK_DROP_UPLOAD_NAME='new-api-v0.1.11.exe'
expect_failure post-upload-missing run_publisher
grep -Fq 'inventory is incomplete after upload' "$temp_dir/post-upload-missing.stderr"
[ "$(<"$MOCK_STATE_DIR/release-state")" = staging ]

reset_state published
seed_remote_assets
printf 'different remote bytes\n' > "$MOCK_STATE_DIR/remote/New-API-App.0.1.11.exe"
expect_failure byte-mismatch run_publisher
grep -Fq 'asset bytes differ' "$temp_dir/byte-mismatch.stderr"

reset_state missing
export MOCK_API_ERROR=true
expect_failure api-error run_publisher
grep -Fq 'release listing returned HTTP 503' "$temp_dir/api-error.stderr"
if grep -Fq 'SENSITIVE_EXTERNAL_ERROR_BODY' "$temp_dir/api-error.stderr"; then
  echo 'external API error body leaked to stderr' >&2
  exit 1
fi

reset_state missing
printf 'true\n' > "$MOCK_STATE_DIR/higher"
expect_failure newer-release run_publisher
grep -Fq 'newer published stable Gitee release already exists: v0.1.12' "$temp_dir/newer-release.stderr"
[ "$(<"$MOCK_STATE_DIR/release-state")" = missing ]

# These are literal GitHub Actions expressions expected in the workflow.
# shellcheck disable=SC2016
grep -Fxq '  group: gitee-release-sync-${{ github.repository }}' \
  "$repo_root/.github/workflows/sync-to-gitee.yml"
grep -Fq -- '--verify-only' "$repo_root/.github/workflows/sync-to-gitee.yml"
grep -Fq -- '--download-dir release_assets' "$repo_root/.github/workflows/sync-to-gitee.yml"

printf 'Gitee release publication tests passed\n'
