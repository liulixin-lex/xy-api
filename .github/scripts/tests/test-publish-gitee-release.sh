#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
publisher=$(cd -- "$script_dir/.." && pwd)/publish-gitee-release.sh
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT

mkdir -p "$temp_dir/bin" "$temp_dir/assets" "$temp_dir/remote"
cat > "$temp_dir/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

output=''
write_out=false
method=GET
asset_file=''
args=("$@")
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
    --form)
      form_value=${args[$((index + 1))]}
      if [[ "$form_value" == file=@* ]]; then
        asset_file=${form_value#file=@}
        asset_file=${asset_file%;type=*}
      fi
      index=$((index + 1))
      ;;
    --data-urlencode)
      index=$((index + 1))
      ;;
  esac
done
url=${args[$((${#args[@]} - 1))]}
printf '%s\n' "$method $url" >> "$MOCK_CALLS"

if [[ "$url" == https://gitee.com/download/* ]]; then
  cp "$MOCK_REMOTE_DIR/${url##*/}" "$output"
  exit 0
fi

status=200
body='{}'
case "$url" in
  */releases/tags/v0.1.11)
    case "$MOCK_RELEASE_STATE" in
      missing)
        status=404
        body='{"message":"not found"}'
        ;;
      existing)
        body='{"id":101,"tag_name":"v0.1.11","name":"Release 0.1.11","body":"notes\nline","target_commitish":"1111111111111111111111111111111111111111"}'
        ;;
      error)
        status=503
        body='{"message":"unavailable"}'
        ;;
    esac
    ;;
  */releases)
    status=201
    body='{"id":101,"tag_name":"v0.1.11","name":"Release 0.1.11","body":"notes\nline","target_commitish":"1111111111111111111111111111111111111111"}'
    ;;
  */releases/101/attach_files)
    if [ "$method" = POST ]; then
      name=$(basename "$asset_file")
      body=$(printf '{"id":201,"name":"%s","browser_download_url":"https://gitee.com/download/%s"}' "$name" "$name")
    else
      body=$MOCK_ATTACHMENTS_JSON
    fi
    ;;
  *)
    echo "unexpected curl URL: $url" >&2
    exit 1
    ;;
esac

printf '%s' "$body" > "$output"
if [ "$write_out" = true ]; then
  printf '%s' "$status"
fi
EOF
chmod +x "$temp_dir/bin/curl"

export PATH="$temp_dir/bin:$PATH"
export GITEE_TOKEN='test-token-value'
export MOCK_CALLS="$temp_dir/calls"
export MOCK_REMOTE_DIR="$temp_dir/remote"
export MOCK_ATTACHMENTS_JSON='[]'

printf 'Release 0.1.11' > "$temp_dir/name.txt"
printf 'notes\nline' > "$temp_dir/body.txt"
printf '1111111111111111111111111111111111111111' > "$temp_dir/target.txt"
printf 'asset-a\n' > "$temp_dir/assets/a.bin"
printf 'asset-b\n' > "$temp_dir/assets/b.bin"

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

export MOCK_RELEASE_STATE=missing
: > "$MOCK_CALLS"
run_publisher > "$temp_dir/create.stdout"
grep -Fq 'synchronized with 2 verified assets' "$temp_dir/create.stdout"
[ "$(grep -c 'POST .*attach_files' "$MOCK_CALLS")" -eq 2 ]

cp "$temp_dir/assets/a.bin" "$temp_dir/remote/a.bin"
cp "$temp_dir/assets/b.bin" "$temp_dir/remote/b.bin"
export MOCK_RELEASE_STATE=existing
export MOCK_ATTACHMENTS_JSON='[
  {"name":"a.bin","browser_download_url":"https://gitee.com/download/a.bin"},
  {"name":"b.bin","browser_download_url":"https://gitee.com/download/b.bin"}
]'
: > "$MOCK_CALLS"
run_publisher > "$temp_dir/existing.stdout"
[ "$(grep -c 'POST .*attach_files' "$MOCK_CALLS" || true)" -eq 0 ]

printf 'different\n' > "$temp_dir/remote/a.bin"
if run_publisher > "$temp_dir/mismatch.stdout" 2> "$temp_dir/mismatch.stderr"; then
  echo 'expected an existing Gitee asset mismatch to fail' >&2
  exit 1
fi
grep -Fq 'existing Gitee asset differs and is immutable: a.bin' "$temp_dir/mismatch.stderr"

export MOCK_RELEASE_STATE=error
if run_publisher > "$temp_dir/error.stdout" 2> "$temp_dir/error.stderr"; then
  echo 'expected a Gitee API error to fail closed' >&2
  exit 1
fi
grep -Fq 'lookup returned HTTP 503' "$temp_dir/error.stderr"

printf 'Gitee release publication tests passed\n'
