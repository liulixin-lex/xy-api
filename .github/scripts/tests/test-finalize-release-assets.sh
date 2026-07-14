#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
finalizer=$(cd -- "$script_dir/.." && pwd)/finalize-release-assets.sh
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT

mkdir -p "$temp_dir/bin" "$temp_dir/assets"
cat > "$temp_dir/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [ "$1" = release ] && [ "$2" = view ]; then
  cat "$MOCK_RELEASE_JSON"
  exit 0
fi
if [ "$1" = release ] && [ "$2" = download ]; then
  output_dir=''
  while [ "$#" -gt 0 ]; do
    if [ "$1" = --dir ]; then
      output_dir=$2
      break
    fi
    shift
  done
  [ -n "$output_dir" ]
  cp "$MOCK_ASSET_DIR"/* "$output_dir/"
  exit 0
fi
if [ "$1" = release ] && [ "$2" = edit ]; then
  printf '%s\n' "$*" >> "$MOCK_CALLS"
  exit 0
fi
if [ "$1" = api ]; then
  if printf '%s\n' "$*" | grep -Fq -- '--method PATCH'; then
    printf '%s\n' "$*" >> "$MOCK_CALLS"
    jq '.isDraft = false' "$MOCK_RELEASE_JSON" > "$MOCK_RELEASE_JSON.tmp"
    mv "$MOCK_RELEASE_JSON.tmp" "$MOCK_RELEASE_JSON"
    printf '{"tag_name":"v0.1.11","draft":false,"prerelease":false}\n'
    exit 0
  fi
  if [ "$MOCK_LATEST_STATE" = missing ]; then
    echo 'gh: Not Found (HTTP 404)' >&2
    exit 1
  fi
  printf '{"tag_name":"%s"}\n' "$MOCK_LATEST_TAG"
  exit 0
fi

echo "unexpected gh invocation: $*" >&2
exit 1
EOF
chmod +x "$temp_dir/bin/gh"

export PATH="$temp_dir/bin:$PATH"
export GITHUB_REPOSITORY='liulixin-lex/xy-api'
export GITHUB_API_URL='https://api.github.test'
export MOCK_ASSET_DIR="$temp_dir/assets"
export MOCK_RELEASE_JSON="$temp_dir/release.json"
export MOCK_CALLS="$temp_dir/calls"
export MOCK_LATEST_STATE=existing
export MOCK_LATEST_TAG=v0.1.10

write_release_json() {
  local draft=$1
  find "$MOCK_ASSET_DIR" -mindepth 1 -maxdepth 1 -type f -printf '%f\n' |
    LC_ALL=C sort |
    jq -Rsc \
      --argjson draft "$draft" '
      split("\n") |
      map(select(length > 0)) |
      {
        apiUrl: "https://api.github.test/repos/liulixin-lex/xy-api/releases/101",
        isDraft: $draft,
        isPrerelease: false,
        tagName: "v0.1.11",
        assets: map({name: .})
      }
    ' > "$MOCK_RELEASE_JSON"
}

create_core_assets() {
  printf 'linux-amd64\n' > "$MOCK_ASSET_DIR/new-api-v0.1.11"
  printf 'linux-arm64\n' > "$MOCK_ASSET_DIR/new-api-arm64-v0.1.11"
  printf 'macos\n' > "$MOCK_ASSET_DIR/new-api-macos-v0.1.11"
  printf 'windows\n' > "$MOCK_ASSET_DIR/new-api-v0.1.11.exe"
  (
    cd "$MOCK_ASSET_DIR"
    sha256sum new-api-arm64-v0.1.11 new-api-v0.1.11 > checksums-linux.txt
    sha256sum new-api-macos-v0.1.11 > checksums-macos.txt
    sha256sum -b new-api-v0.1.11.exe > checksums-windows.txt
  )
}

create_electron_assets() {
  printf 'electron-portable\n' > "$MOCK_ASSET_DIR/New-API-App.0.1.11.exe"
  printf 'electron-setup\n' > "$MOCK_ASSET_DIR/New-API-App.Setup.0.1.11.exe"
  (
    cd "$MOCK_ASSET_DIR"
    sha256sum -b ./New-API-App.0.1.11.exe ./New-API-App.Setup.0.1.11.exe > \
      checksums-electron-windows.txt
  )
}

create_core_assets
write_release_json true
: > "$MOCK_CALLS"
"$finalizer" --tag v0.1.11 > "$temp_dir/incomplete.stdout"
grep -Fq 'remains draft' "$temp_dir/incomplete.stdout"
[ ! -s "$MOCK_CALLS" ]
ready_status=0
"$finalizer" \
  --tag v0.1.11 \
  --verify-ready \
  --download-dir "$temp_dir/incomplete-ready" \
  > "$temp_dir/incomplete-ready.stdout" || ready_status=$?
[ "$ready_status" -eq 3 ]
grep -Fq 'remains draft' "$temp_dir/incomplete-ready.stdout"

write_release_json false
if "$finalizer" --tag v0.1.11 > "$temp_dir/published-incomplete.stdout" 2> "$temp_dir/published-incomplete.stderr"; then
  echo 'expected an incomplete published release to fail' >&2
  exit 1
fi
grep -Fq 'published release v0.1.11 is missing required assets' "$temp_dir/published-incomplete.stderr"

create_electron_assets
write_release_json true
if "$finalizer" \
  --tag v0.1.11 \
  --verify-only \
  --download-dir "$temp_dir/draft-download" \
  > "$temp_dir/draft-verify.stdout" 2> "$temp_dir/draft-verify.stderr"; then
  echo 'expected read-only verification of a draft release to fail' >&2
  exit 1
fi
grep -Fq 'must already be published' "$temp_dir/draft-verify.stderr"
: > "$MOCK_CALLS"
"$finalizer" \
  --tag v0.1.11 \
  --verify-ready \
  --download-dir "$temp_dir/ready-download" > "$temp_dir/ready.stdout"
grep -Fq 'complete verified 10-asset inventory (ready)' "$temp_dir/ready.stdout"
[ ! -s "$MOCK_CALLS" ]
find "$temp_dir/ready-download" -mindepth 1 -maxdepth 1 -type f -printf '%f\n' |
  LC_ALL=C sort > "$temp_dir/ready-assets.txt"
find "$MOCK_ASSET_DIR" -mindepth 1 -maxdepth 1 -type f -printf '%f\n' |
  LC_ALL=C sort > "$temp_dir/ready-source-assets.txt"
cmp "$temp_dir/ready-source-assets.txt" "$temp_dir/ready-assets.txt"
: > "$MOCK_CALLS"
"$finalizer" --tag v0.1.11 > "$temp_dir/complete.stdout"
grep -Fq 'complete verified 10-asset inventory' "$temp_dir/complete.stdout"
grep -Fq 'api --method PATCH repos/liulixin-lex/xy-api/releases/101' "$MOCK_CALLS"
grep -Fq 'release edit v0.1.11 --repo liulixin-lex/xy-api --latest' "$MOCK_CALLS"

: > "$MOCK_CALLS"
"$finalizer" \
  --tag v0.1.11 \
  --verify-only \
  --download-dir "$temp_dir/verified-download" > "$temp_dir/verify-only.stdout"
grep -Fq 'complete verified 10-asset inventory (read-only)' "$temp_dir/verify-only.stdout"
find "$temp_dir/verified-download" -mindepth 1 -maxdepth 1 -type f -printf '%f\n' |
  LC_ALL=C sort > "$temp_dir/verify-only-assets.txt"
find "$MOCK_ASSET_DIR" -mindepth 1 -maxdepth 1 -type f -printf '%f\n' |
  LC_ALL=C sort > "$temp_dir/source-assets.txt"
cmp "$temp_dir/source-assets.txt" "$temp_dir/verify-only-assets.txt"
[ ! -s "$MOCK_CALLS" ]

rm -f "$MOCK_ASSET_DIR"/*
create_electron_assets
write_release_json true
: > "$MOCK_CALLS"
"$finalizer" --tag v0.1.11 > "$temp_dir/electron-first-incomplete.stdout"
grep -Fq 'remains draft' "$temp_dir/electron-first-incomplete.stdout"
[ ! -s "$MOCK_CALLS" ]

create_core_assets
write_release_json true
: > "$MOCK_CALLS"
"$finalizer" --tag v0.1.11 > "$temp_dir/electron-first-complete.stdout"
grep -Fq 'complete verified 10-asset inventory' "$temp_dir/electron-first-complete.stdout"
grep -Fq 'api --method PATCH repos/liulixin-lex/xy-api/releases/101' "$MOCK_CALLS"

export MOCK_LATEST_TAG=v0.1.11
: > "$MOCK_CALLS"
"$finalizer" --tag v0.1.11 > "$temp_dir/repeated.stdout"
grep -Fq 'already latest' "$temp_dir/repeated.stdout"
[ ! -s "$MOCK_CALLS" ]

printf 'changed\n' > "$MOCK_ASSET_DIR/New-API-App.0.1.11.exe"
write_release_json true
if "$finalizer" --tag v0.1.11 > "$temp_dir/checksum.stdout" 2> "$temp_dir/checksum.stderr"; then
  echo 'expected checksum mismatch to fail' >&2
  exit 1
fi

create_electron_assets
write_release_json true
export MOCK_LATEST_TAG=v0.1.12
: > "$MOCK_CALLS"
"$finalizer" --tag v0.1.11 > "$temp_dir/older.stdout"
grep -Fq 'refusing to move latest backward' "$temp_dir/older.stdout"
grep -Fq 'api --method PATCH repos/liulixin-lex/xy-api/releases/101' "$MOCK_CALLS"
if grep -Fq 'release edit' "$MOCK_CALLS"; then
  echo 'older complete draft must publish without moving Latest backward' >&2
  exit 1
fi

printf 'unexpected\n' > "$MOCK_ASSET_DIR/unexpected.bin"
write_release_json true
if "$finalizer" --tag v0.1.11 > "$temp_dir/unexpected.stdout" 2> "$temp_dir/unexpected.stderr"; then
  echo 'expected unexpected release asset to fail' >&2
  exit 1
fi
grep -Fq 'contains unexpected assets' "$temp_dir/unexpected.stderr"

printf 'release asset finalization tests passed\n'
