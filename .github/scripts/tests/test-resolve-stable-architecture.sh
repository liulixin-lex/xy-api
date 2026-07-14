#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
resolver=$(cd -- "$script_dir/.." && pwd)/resolve-stable-architecture.sh
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT

mkdir -p "$temp_dir/bin"
cat > "$temp_dir/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

[ "$1" = buildx ] && [ "$2" = imagetools ] && [ "$3" = inspect ] || {
  echo "unexpected docker invocation: $*" >&2
  exit 1
}
reference=$4
case "$reference" in
  ghcr.io/liulixin-lex/xy-api:v0.1.11-amd64)
    state=$MOCK_PRIMARY_STATE
    digest=$MOCK_PRIMARY_DIGEST
    ;;
  docker.io/liulixin-lex/xy-api:v0.1.11-amd64)
    state=$MOCK_SECONDARY_TAG_STATE
    digest=$MOCK_SECONDARY_TAG_DIGEST
    ;;
  docker.io/liulixin-lex/xy-api@*)
    state=$MOCK_SECONDARY_DIGEST_STATE
    digest=${reference##*@}
    ;;
  *)
    echo "unexpected reference: $reference" >&2
    exit 1
    ;;
esac
if [ "$state" = missing ]; then
  echo 'manifest unknown' >&2
  exit 1
fi
if [ "$state" = error ]; then
  echo 'registry authorization failed' >&2
  exit 1
fi
printf '%s\n' "$digest"
EOF
chmod +x "$temp_dir/bin/docker"

export PATH="$temp_dir/bin:$PATH"
digest_a='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
digest_b='sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'

run_resolver() {
  local name=$1
  "$resolver" \
    --repository ghcr.io/liulixin-lex/xy-api \
    --secondary-repository docker.io/liulixin-lex/xy-api \
    --tag v0.1.11 \
    --arch amd64 \
    --output "$temp_dir/${name}.output"
}

export MOCK_PRIMARY_STATE=missing
export MOCK_PRIMARY_DIGEST="$digest_a"
export MOCK_SECONDARY_TAG_STATE=missing
export MOCK_SECONDARY_TAG_DIGEST="$digest_a"
export MOCK_SECONDARY_DIGEST_STATE=missing
run_resolver absent
grep -Fxq 'reuse_existing=false' "$temp_dir/absent.output"
grep -Fxq 'digest=' "$temp_dir/absent.output"

export MOCK_PRIMARY_STATE=exists
export MOCK_SECONDARY_TAG_STATE=exists
export MOCK_SECONDARY_DIGEST_STATE=exists
run_resolver both
grep -Fxq 'reuse_existing=true' "$temp_dir/both.output"
grep -Fxq "digest=$digest_a" "$temp_dir/both.output"

export MOCK_SECONDARY_TAG_STATE=missing
run_resolver secondary-untagged
grep -Fxq 'reuse_existing=true' "$temp_dir/secondary-untagged.output"

export MOCK_PRIMARY_STATE=missing
export MOCK_SECONDARY_TAG_STATE=exists
if run_resolver secondary-ahead >"$temp_dir/secondary-ahead.stdout" 2>"$temp_dir/secondary-ahead.stderr"; then
  echo 'expected secondary-ahead state to fail' >&2
  exit 1
fi
grep -Fq 'secondary immutable tag exists' "$temp_dir/secondary-ahead.stderr"

export MOCK_PRIMARY_STATE=exists
export MOCK_SECONDARY_TAG_STATE=exists
export MOCK_SECONDARY_TAG_DIGEST="$digest_b"
if run_resolver mismatch >"$temp_dir/mismatch.stdout" 2>"$temp_dir/mismatch.stderr"; then
  echo 'expected registry digest mismatch to fail' >&2
  exit 1
fi
grep -Fq 'immutable architecture tags disagree' "$temp_dir/mismatch.stderr"

export MOCK_SECONDARY_TAG_STATE=missing
export MOCK_SECONDARY_TAG_DIGEST="$digest_a"
export MOCK_SECONDARY_DIGEST_STATE=missing
if run_resolver missing-content >"$temp_dir/missing-content.stdout" 2>"$temp_dir/missing-content.stderr"; then
  echo 'expected missing secondary content to fail' >&2
  exit 1
fi
grep -Fq 'no longer contains immutable digest' "$temp_dir/missing-content.stderr"

export MOCK_PRIMARY_STATE=error
export MOCK_SECONDARY_TAG_STATE=missing
if run_resolver registry-error >"$temp_dir/registry-error.stdout" 2>"$temp_dir/registry-error.stderr"; then
  echo 'expected registry inspection error to fail closed' >&2
  exit 1
fi
grep -Fq 'could not safely inspect' "$temp_dir/registry-error.stderr"

printf 'stable architecture resolution tests passed\n'
