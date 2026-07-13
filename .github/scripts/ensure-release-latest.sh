#!/usr/bin/env bash

set -euo pipefail

fail() {
  printf 'release latest promotion failed: %s\n' "$1" >&2
  exit 1
}

tag=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --tag)
      [ "$#" -ge 2 ] || fail '--tag requires a value'
      tag=$2
      shift 2
      ;;
    --help|-h)
      printf 'Usage: ensure-release-latest.sh --tag TAG\n'
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

command -v gh >/dev/null 2>&1 || fail 'gh is required'
command -v jq >/dev/null 2>&1 || fail 'jq is required'

if [[ ! "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  fail "tag must use stable lowercase v semver: $tag"
fi
if [[ ! "${GITHUB_REPOSITORY:-}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  fail "invalid GITHUB_REPOSITORY: ${GITHUB_REPOSITORY:-}"
fi

release_json=$(gh release view "$tag" --repo "$GITHUB_REPOSITORY" --json isDraft,isPrerelease,tagName) ||
  fail "could not read release $tag"
jq -e --arg tag "$tag" '
  .tagName == $tag and .isDraft == false and .isPrerelease == false
' <<<"$release_json" >/dev/null || fail "release $tag is not a published stable release"

latest_error=$(mktemp)
trap 'rm -f "$latest_error"' EXIT
latest_tag=''
if latest_json=$(gh api "repos/${GITHUB_REPOSITORY}/releases/latest" 2>"$latest_error"); then
  latest_tag=$(jq -r '.tag_name // ""' <<<"$latest_json")
  if [[ ! "$latest_tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    fail "current latest release does not use stable lowercase v semver: $latest_tag"
  fi
elif ! grep -Eq '(Not Found|not found|HTTP 404)' "$latest_error"; then
  cat "$latest_error" >&2
  fail 'could not determine the current latest release'
fi

if [ "$latest_tag" = "$tag" ]; then
  printf 'release %s is already latest\n' "$tag"
  exit 0
fi
if [ -n "$latest_tag" ]; then
  highest=$(printf '%s\n%s\n' "$latest_tag" "$tag" | LC_ALL=C sort -V | tail -n 1)
  if [ "$highest" != "$tag" ]; then
    printf 'release %s remains latest; refusing to move latest backward to %s\n' "$latest_tag" "$tag"
    exit 0
  fi
fi

gh release edit "$tag" --repo "$GITHUB_REPOSITORY" --latest
printf 'release %s is now latest\n' "$tag"
