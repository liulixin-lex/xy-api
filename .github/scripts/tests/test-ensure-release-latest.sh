#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
promoter=$(cd -- "$script_dir/.." && pwd)/ensure-release-latest.sh
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT

mkdir -p "$temp_dir/bin"
cat > "$temp_dir/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [ "$1" = release ] && [ "$2" = view ]; then
  printf '{"isDraft":%s,"isPrerelease":false,"tagName":"%s"}\n' \
    "${MOCK_RELEASE_DRAFT:-false}" "${3:-$MOCK_TARGET_TAG}"
  exit 0
fi
if [ "$1" = release ] && [ "$2" = edit ]; then
  printf '%s\n' "$*" >> "$MOCK_CALLS"
  exit 0
fi
if [ "$1" = api ]; then
  if [ "$MOCK_LATEST_STATE" = missing ]; then
    echo 'gh: Not Found (HTTP 404)' >&2
    exit 1
  fi
  if [ "$MOCK_LATEST_STATE" = error ]; then
    echo 'gh: service unavailable (HTTP 503)' >&2
    exit 1
  fi
  printf '{"tag_name":"%s"}\n' "$MOCK_LATEST_TAG"
  exit 0
fi

echo "unexpected gh invocation: $*" >&2
exit 1
EOF
chmod +x "$temp_dir/bin/gh"

cat > "$temp_dir/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

case "$1" in
  fetch)
    exit 0
    ;;
  rev-list)
    case "${!#}" in
      *v0.1.11) printf '%s\n' '1111111111111111111111111111111111111111' ;;
      *v0.1.12) printf '%s\n' '2222222222222222222222222222222222222222' ;;
      *) exit 1 ;;
    esac
    ;;
  merge-base)
    if [ "${MOCK_GIT_UNTRUSTED:-false}" = true ]; then
      exit 1
    fi
    exit 0
    ;;
  show)
    tag=${2%%:*}
    case "$tag" in
      1111111111111111111111111111111111111111) printf '%s\n' 'v0.1.11' ;;
      2222222222222222222222222222222222222222) printf '%s\n' 'v0.1.12' ;;
      *) exit 1 ;;
    esac
    ;;
  *)
    echo "unexpected git invocation: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$temp_dir/bin/git"

export PATH="$temp_dir/bin:$PATH"
export GITHUB_REPOSITORY='liulixin-lex/xy-api'
export MOCK_CALLS="$temp_dir/calls"
export MOCK_TARGET_TAG=v0.1.11

run_case() {
  local name=$1
  : > "$MOCK_CALLS"
  "$promoter" --tag v0.1.11 > "$temp_dir/${name}.stdout"
}

export MOCK_LATEST_STATE=existing
export MOCK_LATEST_TAG=v0.1.10
run_case older
grep -Fxq 'release edit v0.1.11 --repo liulixin-lex/xy-api --latest' "$MOCK_CALLS"

export MOCK_LATEST_TAG=v0.1.11
run_case same
[ ! -s "$MOCK_CALLS" ]

export MOCK_LATEST_TAG=v0.1.12
run_case newer
[ ! -s "$MOCK_CALLS" ]
grep -Fq 'refusing to move latest backward' "$temp_dir/newer.stdout"

export MOCK_LATEST_STATE=missing
run_case missing
grep -Fxq 'release edit v0.1.11 --repo liulixin-lex/xy-api --latest' "$MOCK_CALLS"

export MOCK_LATEST_STATE=existing
export MOCK_LATEST_TAG=v0.1.10
export MOCK_RELEASE_DRAFT=true
if "$promoter" --tag v0.1.11 > "$temp_dir/draft.stdout" 2> "$temp_dir/draft.stderr"; then
  echo 'expected draft release promotion to fail' >&2
  exit 1
fi
grep -Fq 'not a published stable release' "$temp_dir/draft.stderr"

export MOCK_RELEASE_DRAFT=false
export MOCK_LATEST_STATE=error
if "$promoter" --tag v0.1.11 > "$temp_dir/api-error.stdout" 2> "$temp_dir/api-error.stderr"; then
  echo 'expected latest API error to fail closed' >&2
  exit 1
fi
grep -Fq 'could not determine the current latest release' "$temp_dir/api-error.stderr"

export MOCK_LATEST_STATE=existing
export MOCK_LATEST_TAG=v0.1.12
export MOCK_GIT_UNTRUSTED=false
: > "$MOCK_CALLS"
"$promoter" --tag v0.1.11 --default-branch main > "$temp_dir/trusted-newer.stdout"
grep -Fq 'refusing to move latest backward' "$temp_dir/trusted-newer.stdout"
[ ! -s "$MOCK_CALLS" ]

export MOCK_GIT_UNTRUSTED=true
if "$promoter" --tag v0.1.11 --default-branch main > "$temp_dir/untrusted-newer.stdout" 2> "$temp_dir/untrusted-newer.stderr"; then
  echo 'expected an untrusted higher GitHub latest release to fail closed' >&2
  exit 1
fi
grep -Fq 'not an ancestor of origin/main' "$temp_dir/untrusted-newer.stderr"

printf 'release latest promotion tests passed\n'
