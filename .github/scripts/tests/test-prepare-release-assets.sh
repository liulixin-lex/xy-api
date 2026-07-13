#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
preparer=$(cd -- "$script_dir/.." && pwd)/prepare-release-assets.sh
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT

mkdir -p "$temp_dir/bin" "$temp_dir/assets"
cat > "$temp_dir/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [ "$1" = release ] && [ "$2" = view ]; then
  case "$MOCK_RELEASE_STATE" in
    missing)
      echo 'release not found' >&2
      exit 1
      ;;
    error)
      echo 'temporary API failure' >&2
      exit 1
      ;;
    existing)
      cat "$MOCK_RELEASE_JSON"
      exit 0
      ;;
  esac
fi

if [ "$1" = api ]; then
  endpoint=${!#}
  if [[ "$endpoint" == */releases/latest ]]; then
    if [ "$MOCK_LATEST_STATE" = missing ]; then
      echo 'gh: Not Found (HTTP 404)' >&2
      exit 1
    fi
    printf '{"tag_name":"%s"}\n' "$MOCK_LATEST_TAG"
    exit 0
  fi
  asset_id=${endpoint##*/}
  cat "$MOCK_ASSET_DIR/$asset_id"
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

expect_failure() {
  local name=$1
  shift
  if "$@" > "$temp_dir/${name}.stdout" 2> "$temp_dir/${name}.stderr"; then
    printf 'expected failure for %s\n' "$name" >&2
    exit 1
  fi
  [ -s "$temp_dir/${name}.stderr" ] || {
    printf 'expected diagnostic output for %s\n' "$name" >&2
    exit 1
  }
}

run_prepare() {
  local name=$1
  local tag=${2:-v0.1.11}
  mkdir -p "$temp_dir/$name/source" "$temp_dir/$name/upload"
  "$preparer" \
    --tag "$tag" \
    --source-dir "$temp_dir/$name/source" \
    --upload-dir "$temp_dir/$name/upload" \
    --output "$temp_dir/$name/output"
}

export MOCK_RELEASE_STATE=missing
mkdir -p "$temp_dir/new/source"
printf 'binary-a\n' > "$temp_dir/new/source/a.bin"
printf 'binary-b\n' > "$temp_dir/new/source/b.bin"
run_prepare new
grep -Fxq 'upload_required=true' "$temp_dir/new/output"
grep -Fxq 'asset_count=2' "$temp_dir/new/output"
grep -Fxq 'upload_count=2' "$temp_dir/new/output"
cmp "$temp_dir/new/source/a.bin" "$temp_dir/new/upload/a.bin"
cmp "$temp_dir/new/source/b.bin" "$temp_dir/new/upload/b.bin"

export MOCK_RELEASE_STATE=existing
export MOCK_RELEASE_JSON="$temp_dir/partial-release.json"
printf 'binary-a\n' > "$temp_dir/assets/101"
cat > "$MOCK_RELEASE_JSON" <<'EOF'
{
  "isDraft": true,
  "isPrerelease": false,
  "tagName": "v0.1.11",
  "assets": [
    {
      "name": "a.bin",
      "apiUrl": "https://api.github.test/repos/liulixin-lex/xy-api/releases/assets/101"
    }
  ]
}
EOF
mkdir -p "$temp_dir/partial/source"
printf 'binary-a\n' > "$temp_dir/partial/source/a.bin"
printf 'binary-b\n' > "$temp_dir/partial/source/b.bin"
run_prepare partial
grep -Fxq 'upload_required=true' "$temp_dir/partial/output"
grep -Fxq 'upload_count=1' "$temp_dir/partial/output"
[ ! -e "$temp_dir/partial/upload/a.bin" ]
cmp "$temp_dir/partial/source/b.bin" "$temp_dir/partial/upload/b.bin"

jq '.isDraft = false' "$MOCK_RELEASE_JSON" > "$temp_dir/published-release.json"
export MOCK_RELEASE_JSON="$temp_dir/published-release.json"
mkdir -p "$temp_dir/complete/source"
printf 'binary-a\n' > "$temp_dir/complete/source/a.bin"
run_prepare complete
grep -Fxq 'upload_required=false' "$temp_dir/complete/output"
grep -Fxq 'upload_count=0' "$temp_dir/complete/output"
[ -z "$(find "$temp_dir/complete/upload" -mindepth 1 -maxdepth 1 -print -quit)" ]

mkdir -p "$temp_dir/conflict/source" "$temp_dir/conflict/upload"
printf 'different\n' > "$temp_dir/conflict/source/a.bin"
expect_failure immutable-conflict "$preparer" \
  --tag v0.1.11 \
  --source-dir "$temp_dir/conflict/source" \
  --upload-dir "$temp_dir/conflict/upload" \
  --output "$temp_dir/conflict/output"

export MOCK_RELEASE_STATE=error
mkdir -p "$temp_dir/api-error/source" "$temp_dir/api-error/upload"
printf 'asset\n' > "$temp_dir/api-error/source/asset.bin"
expect_failure api-error "$preparer" \
  --tag v0.1.11 \
  --source-dir "$temp_dir/api-error/source" \
  --upload-dir "$temp_dir/api-error/upload" \
  --output "$temp_dir/api-error/output"

printf 'release asset preparation tests passed\n'
