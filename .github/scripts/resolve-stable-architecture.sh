#!/usr/bin/env bash

set -euo pipefail

fail() {
  printf 'stable architecture resolution failed: %s\n' "$1" >&2
  exit 1
}

repository=''
secondary_repository=''
tag=''
arch=''
output_file=''

while [ "$#" -gt 0 ]; do
  case "$1" in
    --repository)
      [ "$#" -ge 2 ] || fail '--repository requires a value'
      repository=$2
      shift 2
      ;;
    --secondary-repository)
      [ "$#" -ge 2 ] || fail '--secondary-repository requires a value'
      secondary_repository=$2
      shift 2
      ;;
    --tag)
      [ "$#" -ge 2 ] || fail '--tag requires a value'
      tag=$2
      shift 2
      ;;
    --arch)
      [ "$#" -ge 2 ] || fail '--arch requires a value'
      arch=$2
      shift 2
      ;;
    --output)
      [ "$#" -ge 2 ] || fail '--output requires a value'
      output_file=$2
      shift 2
      ;;
    --help|-h)
      printf 'Usage: resolve-stable-architecture.sh --repository REPOSITORY [--secondary-repository REPOSITORY] --tag TAG --arch ARCH --output FILE\n'
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

command -v docker >/dev/null 2>&1 || fail 'docker is required'
if [[ ! "$repository" =~ ^[A-Za-z0-9._-]+(:[0-9]+)?(/[A-Za-z0-9._-]+)+$ ]]; then
  fail "invalid repository: $repository"
fi
if [ -n "$secondary_repository" ] &&
  [[ ! "$secondary_repository" =~ ^[A-Za-z0-9._-]+(:[0-9]+)?(/[A-Za-z0-9._-]+)+$ ]]; then
  fail "invalid secondary repository: $secondary_repository"
fi
if [[ ! "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  fail "invalid stable tag: $tag"
fi
case "$arch" in
  amd64|arm64) ;;
  *) fail "unsupported architecture: $arch" ;;
esac
[ -n "$output_file" ] || fail '--output is required'

temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT
inspected_digest=''

inspect_optional_digest() {
  local reference=$1
  local name=$2
  local output="$temp_dir/${name}.digest"
  local error="$temp_dir/${name}.error"
  if docker buildx imagetools inspect "$reference" --format '{{.Manifest.Digest}}' >"$output" 2>"$error"; then
    inspected_digest=$(tr -d '\r\n' <"$output")
    if [[ ! "$inspected_digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
      fail "$reference returned an invalid manifest digest: $inspected_digest"
    fi
    return 0
  fi
  if grep -Eiq 'manifest unknown|not found|no such manifest|name unknown' "$error"; then
    inspected_digest=''
    return 1
  fi
  cat "$error" >&2
  fail "could not safely inspect $reference"
}

primary_reference="${repository}:${tag}-${arch}"
primary_digest=''
if inspect_optional_digest "$primary_reference" primary; then
  primary_digest=$inspected_digest
fi

secondary_tag_digest=''
if [ -n "$secondary_repository" ] &&
  inspect_optional_digest "${secondary_repository}:${tag}-${arch}" secondary-tag; then
  secondary_tag_digest=$inspected_digest
fi

mkdir -p "$(dirname "$output_file")"
if [ -z "$primary_digest" ]; then
  if [ -n "$secondary_tag_digest" ]; then
    fail "secondary immutable tag exists while $primary_reference is missing"
  fi
  {
    echo 'reuse_existing=false'
    echo 'digest='
  } >>"$output_file"
  exit 0
fi

if [ -n "$secondary_repository" ]; then
  if [ -n "$secondary_tag_digest" ]; then
    if [ "$secondary_tag_digest" != "$primary_digest" ]; then
      fail "immutable architecture tags disagree across registries: $primary_digest != $secondary_tag_digest"
    fi
  elif ! inspect_optional_digest "${secondary_repository}@${primary_digest}" secondary-digest; then
    fail "secondary registry no longer contains immutable digest $primary_digest"
  elif [ "$inspected_digest" != "$primary_digest" ]; then
    fail "secondary registry resolved an unexpected immutable digest"
  fi
fi

{
  echo 'reuse_existing=true'
  echo "digest=$primary_digest"
} >>"$output_file"
