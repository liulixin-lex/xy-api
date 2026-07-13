#!/usr/bin/env bash

# The workflow assertions below intentionally match literal shell expressions.
# shellcheck disable=SC2016

set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../../.." && pwd)
resolver=$(cd -- "$script_dir/.." && pwd)/resolve-stable-latest.sh
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT

mkdir -p "$temp_dir/bin"
cat > "$temp_dir/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

format=${*: -1}
case "$format" in
  '{{json .Manifest}}')
    case "$MOCK_LATEST_STATE" in
      missing)
        echo 'manifest unknown' >&2
        exit 1
        ;;
      error)
        echo 'registry unavailable' >&2
        exit 1
        ;;
      existing)
        printf '{"digest":"%s"}\n' "$MOCK_LATEST_DIGEST"
        ;;
    esac
    ;;
  '{{json .Image}}')
    jq -n --arg version "$MOCK_LATEST_VERSION" '{
      "linux/amd64": {config: {Labels: {"org.opencontainers.image.version": $version}}},
      "linux/arm64": {config: {Labels: {"org.opencontainers.image.version": $version}}}
    }'
    ;;
  *)
    echo "unexpected docker invocation: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$temp_dir/bin/docker"

export PATH="$temp_dir/bin:$PATH"
candidate='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
other='sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'

run_resolver() {
  local name=$1
  "$resolver" \
    --repository ghcr.io/liulixin-lex/xy-api \
    --tag v0.1.11 \
    --candidate-digest "$candidate" \
    --output "$temp_dir/${name}.output"
}

export MOCK_LATEST_STATE=missing
run_resolver missing
grep -Fxq 'promote_latest=true' "$temp_dir/missing.output"
grep -Fxq "expected_latest_digest=$candidate" "$temp_dir/missing.output"

export MOCK_LATEST_STATE=existing
export MOCK_LATEST_DIGEST="$other"
export MOCK_LATEST_VERSION=v0.1.10
run_resolver older
grep -Fxq 'promote_latest=true' "$temp_dir/older.output"
grep -Fxq "expected_latest_digest=$candidate" "$temp_dir/older.output"

export MOCK_LATEST_DIGEST="$candidate"
export MOCK_LATEST_VERSION=v0.1.11
run_resolver same
grep -Fxq 'promote_latest=false' "$temp_dir/same.output"
grep -Fxq "expected_latest_digest=$candidate" "$temp_dir/same.output"

export MOCK_LATEST_DIGEST="$other"
export MOCK_LATEST_VERSION=v0.1.12
run_resolver newer
grep -Fxq 'promote_latest=false' "$temp_dir/newer.output"
grep -Fxq "expected_latest_digest=$other" "$temp_dir/newer.output"
workflow="$repo_root/.github/workflows/docker-build.yml"
grep -Fq -- '-t "${repository}:${TAG}-amd64"' "$workflow"
grep -Fq -- '-t "${repository}:${TAG}-arm64"' "$workflow"
[ "$(grep -Fc -- '-t "${repository}:${TAG}"' "$workflow")" -eq 2 ]
[ "$(grep -Fc -- '-t "${repository}:latest"' "$workflow")" -eq 1 ]
grep -Fq 'if [ "$promote_latest" = '\''true'\'' ]; then' "$workflow"
grep -Fq 'if [ "$latest_digest" != "$expected_latest_digest" ]; then' "$workflow"

export MOCK_LATEST_VERSION=v0.1.11
if run_resolver same-conflict > "$temp_dir/same-conflict.stdout" 2> "$temp_dir/same-conflict.stderr"; then
  echo 'expected same-version digest conflict to fail' >&2
  exit 1
fi
grep -Fq 'different immutable digest' "$temp_dir/same-conflict.stderr"

export MOCK_LATEST_STATE=error
if run_resolver error > "$temp_dir/error.stdout" 2> "$temp_dir/error.stderr"; then
  echo 'expected registry error to fail closed' >&2
  exit 1
fi
grep -Fq 'could not safely inspect' "$temp_dir/error.stderr"

printf 'stable latest resolution tests passed\n'
