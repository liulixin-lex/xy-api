#!/usr/bin/env bash

set -euo pipefail

fail() {
  printf 'release latest promotion failed: %s\n' "$1" >&2
  exit 1
}

tag=''
default_branch=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --tag)
      [ "$#" -ge 2 ] || fail '--tag requires a value'
      tag=$2
      shift 2
      ;;
    --default-branch)
      [ "$#" -ge 2 ] || fail '--default-branch requires a value'
      default_branch=$2
      shift 2
      ;;
    --help|-h)
      printf 'Usage: ensure-release-latest.sh --tag TAG [--default-branch BRANCH]\n'
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
if [ -n "$default_branch" ] && {
  [[ ! "$default_branch" =~ ^[A-Za-z0-9._/-]+$ ]] ||
  [[ "$default_branch" == .* ]] || [[ "$default_branch" == */.* ]];
}; then
  fail "invalid default branch: $default_branch"
fi

if [ -n "$default_branch" ]; then
  command -v git >/dev/null 2>&1 || fail 'git is required when validating the current latest release'
  command -v cmp >/dev/null 2>&1 || fail 'cmp is required when validating the current latest release'
  git fetch --no-tags origin "$default_branch" >/dev/null
fi

validate_release_tag() {
  local release_tag=$1
  local release_sha
  if [[ ! "$release_tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    fail "current latest release does not use stable lowercase v semver: $release_tag"
  fi
  git fetch --force origin "refs/tags/${release_tag}:refs/tags/${release_tag}" >/dev/null
  release_sha=$(git rev-list -n 1 "refs/tags/${release_tag}")
  if [[ ! "$release_sha" =~ ^[0-9a-f]{40}$ ]]; then
    fail "could not resolve the current latest tag: $release_tag"
  fi
  if ! git merge-base --is-ancestor "$release_sha" "origin/${default_branch}"; then
    fail "current latest release commit $release_sha is not an ancestor of origin/$default_branch"
  fi
  if ! git show "${release_sha}:VERSION" | cmp -s - <(printf '%s\n' "$release_tag"); then
    fail "VERSION at current latest release $release_tag does not exactly match the release tag"
  fi
}

release_json=$(gh release view "$tag" --repo "$GITHUB_REPOSITORY" --json isDraft,isPrerelease,tagName) ||
  fail "could not read release $tag"
jq -e --arg tag "$tag" '
  .tagName == $tag and .isDraft == false and .isPrerelease == false
' <<<"$release_json" >/dev/null || fail "release $tag is not a published stable release"
if [ -n "$default_branch" ]; then
  validate_release_tag "$tag"
fi

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
  if [ -n "$default_branch" ]; then
    validate_release_tag "$latest_tag"
    latest_release_json=$(gh release view "$latest_tag" \
      --repo "$GITHUB_REPOSITORY" \
      --json isDraft,isPrerelease,tagName) ||
      fail "could not read current latest release $latest_tag"
    jq -e --arg latest_tag "$latest_tag" '
      .tagName == $latest_tag and .isDraft == false and .isPrerelease == false
    ' <<<"$latest_release_json" >/dev/null ||
      fail "current latest release $latest_tag is not a published stable release"
  fi
  highest=$(printf '%s\n%s\n' "$latest_tag" "$tag" | LC_ALL=C sort -V | tail -n 1)
  if [ "$highest" != "$tag" ]; then
    printf 'release %s remains latest; refusing to move latest backward to %s\n' "$latest_tag" "$tag"
    exit 0
  fi
fi

gh release edit "$tag" --repo "$GITHUB_REPOSITORY" --latest
printf 'release %s is now latest\n' "$tag"
