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
  local trusted=${2:-false}
  local -a args=(
    --repository ghcr.io/liulixin-lex/xy-api
    --tag v0.1.11
    --candidate-digest "$candidate"
    --output "$temp_dir/${name}.output"
  )
  if [ "$trusted" = true ]; then
    args+=(
      --trusted-current-version "$MOCK_LATEST_VERSION"
      --trusted-current-digest "$MOCK_LATEST_DIGEST"
    )
  fi
  "$resolver" "${args[@]}"
}

export MOCK_LATEST_STATE=missing
run_resolver missing
grep -Fxq 'promote_latest=true' "$temp_dir/missing.output"
grep -Fxq "expected_latest_digest=$candidate" "$temp_dir/missing.output"

export MOCK_LATEST_STATE=existing
export MOCK_LATEST_DIGEST="$other"
export MOCK_LATEST_VERSION=v0.1.10
if run_resolver untrusted-existing > "$temp_dir/untrusted-existing.stdout" 2> "$temp_dir/untrusted-existing.stderr"; then
  echo 'expected an untrusted existing latest to fail' >&2
  exit 1
fi
grep -Fq 'must be trusted' "$temp_dir/untrusted-existing.stderr"

run_resolver older true
grep -Fxq 'promote_latest=true' "$temp_dir/older.output"
grep -Fxq "expected_latest_digest=$candidate" "$temp_dir/older.output"

export MOCK_LATEST_DIGEST="$candidate"
export MOCK_LATEST_VERSION=v0.1.11
run_resolver same true
grep -Fxq 'promote_latest=false' "$temp_dir/same.output"
grep -Fxq "expected_latest_digest=$candidate" "$temp_dir/same.output"

export MOCK_LATEST_DIGEST="$other"
export MOCK_LATEST_VERSION=v0.1.12
run_resolver newer true
grep -Fxq 'promote_latest=false' "$temp_dir/newer.output"
grep -Fxq "expected_latest_digest=$other" "$temp_dir/newer.output"
finalizer="$repo_root/.github/scripts/finalize-stable-release.sh"
grep -Fq -- '-t "${image_repository}:latest"' "$finalizer"
grep -Fq -- '--trusted-current-version "$CURRENT_LATEST_VERSION"' "$finalizer"
grep -Fq -- '--trusted-current-digest "$CURRENT_LATEST_DIGEST"' "$finalizer"
grep -Fq 'if [ "$latest_digest" != "${expected_latest_digests[$repository_name]}" ]; then' "$finalizer"

export MOCK_LATEST_VERSION=v0.1.11
if run_resolver same-conflict true > "$temp_dir/same-conflict.stdout" 2> "$temp_dir/same-conflict.stderr"; then
  echo 'expected same-version digest conflict to fail' >&2
  exit 1
fi
grep -Fq 'different immutable digest' "$temp_dir/same-conflict.stderr"

export MOCK_LATEST_STATE=error
if run_resolver error true > "$temp_dir/error.stdout" 2> "$temp_dir/error.stderr"; then
  echo 'expected registry error to fail closed' >&2
  exit 1
fi
grep -Fq 'could not safely inspect' "$temp_dir/error.stderr"

export MOCK_LATEST_STATE=existing
export MOCK_LATEST_VERSION=v0.1.12
export MOCK_LATEST_DIGEST="$other"
if "$resolver" \
  --repository ghcr.io/liulixin-lex/xy-api \
  --tag v0.1.11 \
  --candidate-digest "$candidate" \
  --trusted-current-version v0.1.12 \
  --trusted-current-digest sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc \
  --output "$temp_dir/trusted-mismatch.output" \
  > "$temp_dir/trusted-mismatch.stdout" 2> "$temp_dir/trusted-mismatch.stderr"; then
  echo 'expected trusted latest mismatch to fail' >&2
  exit 1
fi
grep -Fq 'changed after trusted verification' "$temp_dir/trusted-mismatch.stderr"

printf 'stable latest resolution tests passed\n'
