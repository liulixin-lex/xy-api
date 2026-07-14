#!/usr/bin/env bash

set -euo pipefail

fail() {
  printf 'stable latest resolution failed: %s\n' "$1" >&2
  exit 1
}

repository=''
tag=''
candidate_digest=''
trusted_current_version=''
trusted_current_digest=''
output_file=''

while [ "$#" -gt 0 ]; do
  case "$1" in
    --repository)
      [ "$#" -ge 2 ] || fail '--repository requires a value'
      repository=$2
      shift 2
      ;;
    --tag)
      [ "$#" -ge 2 ] || fail '--tag requires a value'
      tag=$2
      shift 2
      ;;
    --candidate-digest)
      [ "$#" -ge 2 ] || fail '--candidate-digest requires a value'
      candidate_digest=$2
      shift 2
      ;;
    --trusted-current-version)
      [ "$#" -ge 2 ] || fail '--trusted-current-version requires a value'
      trusted_current_version=$2
      shift 2
      ;;
    --trusted-current-digest)
      [ "$#" -ge 2 ] || fail '--trusted-current-digest requires a value'
      trusted_current_digest=$2
      shift 2
      ;;
    --output)
      [ "$#" -ge 2 ] || fail '--output requires a value'
      output_file=$2
      shift 2
      ;;
    --help|-h)
      printf '%s\n' 'Usage: resolve-stable-latest.sh --repository REPOSITORY --tag TAG --candidate-digest DIGEST [--trusted-current-version TAG --trusted-current-digest DIGEST] --output FILE'
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

command -v docker >/dev/null 2>&1 || fail 'docker is required'
command -v jq >/dev/null 2>&1 || fail 'jq is required'
if [[ ! "$repository" =~ ^[A-Za-z0-9._-]+(:[0-9]+)?(/[A-Za-z0-9._-]+)+$ ]]; then
  fail "invalid repository: $repository"
fi
if [[ ! "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  fail "invalid stable tag: $tag"
fi
if [[ ! "$candidate_digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  fail "invalid candidate digest: $candidate_digest"
fi
if [ -n "$trusted_current_version" ] || [ -n "$trusted_current_digest" ]; then
  if [[ ! "$trusted_current_version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    fail "invalid trusted current version: $trusted_current_version"
  fi
  if [[ ! "$trusted_current_digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    fail "invalid trusted current digest: $trusted_current_digest"
  fi
fi
[ -n "$output_file" ] || fail '--output is required'

temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT
reference="${repository}:latest"
manifest_file="$temp_dir/manifest.json"
error_file="$temp_dir/manifest.error"

if ! docker buildx imagetools inspect "$reference" \
  --format '{{json .Manifest}}' > "$manifest_file" 2> "$error_file"; then
  if grep -Eiq 'manifest unknown|not found|no such manifest|name unknown' "$error_file"; then
    if [ -n "$trusted_current_version" ] || [ -n "$trusted_current_digest" ]; then
      fail "$reference disappeared after trusted verification"
    fi
    mkdir -p "$(dirname "$output_file")"
    {
      echo 'promote_latest=true'
      echo "expected_latest_digest=$candidate_digest"
      echo 'current_latest_digest='
      echo 'current_latest_version='
    } > "$output_file"
    exit 0
  fi
  cat "$error_file" >&2
  fail "could not safely inspect $reference"
fi

current_digest=$(jq -r '.digest // ""' "$manifest_file")
if [[ ! "$current_digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  fail "$reference returned an invalid digest: $current_digest"
fi
image_file="$temp_dir/image.json"
docker buildx imagetools inspect "$reference" \
  --format '{{json .Image}}' > "$image_file"
mapfile -t versions < <(jq -r '[.[] | .config.Labels["org.opencontainers.image.version"] // ""] | unique[]' "$image_file")
if [ "${#versions[@]}" -ne 1 ] ||
  [[ ! "${versions[0]}" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  fail "$reference does not expose one trustworthy stable version label"
fi
current_version=${versions[0]}
if [ -z "$trusted_current_version" ] || [ -z "$trusted_current_digest" ]; then
  fail "$reference must be trusted before it can influence stable promotion"
fi
if [ "$current_version" != "$trusted_current_version" ] || [ "$current_digest" != "$trusted_current_digest" ]; then
  fail "$reference changed after trusted verification"
fi
highest=$(printf '%s\n%s\n' "$current_version" "$tag" | LC_ALL=C sort -V | tail -n 1)

promote_latest=true
expected_latest_digest=$candidate_digest
if [ "$highest" != "$tag" ]; then
  promote_latest=false
  expected_latest_digest=$current_digest
elif [ "$current_version" = "$tag" ]; then
  if [ "$current_digest" != "$candidate_digest" ]; then
    fail "$reference claims $tag but has a different immutable digest"
  fi
  promote_latest=false
  expected_latest_digest=$current_digest
fi

mkdir -p "$(dirname "$output_file")"
{
  echo "promote_latest=$promote_latest"
  echo "expected_latest_digest=$expected_latest_digest"
  echo "current_latest_digest=$current_digest"
  echo "current_latest_version=$current_version"
} > "$output_file"
