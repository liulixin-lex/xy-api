#!/usr/bin/env bash

set -Eeuo pipefail

buildx_version=v0.30.1
# SHA-256 values below are the digest fields published on the official
# docker/buildx GitHub Release assets for this exact version.

for command_name in curl docker sha256sum; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "required command is not installed: $command_name" >&2
    exit 1
  fi
done

case "${RUNNER_OS:-$(uname -s)}" in
  Linux | linux) ;;
  *)
    echo 'the pinned Buildx installer currently supports Linux runners only' >&2
    exit 1
    ;;
esac

case "${RUNNER_ARCH:-$(uname -m)}" in
  X64 | x64 | amd64 | x86_64)
    asset_arch=amd64
    expected_sha256=c37114fcd034025ec68e224657c8a5a850df472ded3ddcbca75ad3a7ebb9710d
    ;;
  ARM64 | arm64 | aarch64)
    asset_arch=arm64
    expected_sha256=31d012d52d6df68aef4b55db62330967b562811f0de30cdfaa4505f314797c76
    ;;
  *)
    echo "unsupported Buildx runner architecture: ${RUNNER_ARCH:-$(uname -m)}" >&2
    exit 1
    ;;
esac

asset_name="buildx-${buildx_version}.linux-${asset_arch}"
asset_url="https://github.com/docker/buildx/releases/download/${buildx_version}/${asset_name}"
work_directory=$(mktemp -d)
download_path="$work_directory/$asset_name"
docker_config=${DOCKER_CONFIG:-$HOME/.docker}
plugin_directory="$docker_config/cli-plugins"
plugin_path="$plugin_directory/docker-buildx"
staged_plugin="$plugin_directory/.docker-buildx-${buildx_version}-$$"

cleanup() {
  rm -rf "$work_directory"
  rm -f "$staged_plugin"
}
trap cleanup EXIT

curl \
  --fail \
  --silent \
  --show-error \
  --location \
  --proto '=https' \
  --tlsv1.2 \
  --connect-timeout 15 \
  --max-time 300 \
  --retry 3 \
  --retry-all-errors \
  "$asset_url" \
  --output "$download_path"

printf '%s  %s\n' "$expected_sha256" "$download_path" |
  sha256sum --check --strict

install -d -m 0755 "$plugin_directory"
install -m 0755 "$download_path" "$staged_plugin"
mv -f "$staged_plugin" "$plugin_path"

printf '%s  %s\n' "$expected_sha256" "$plugin_path" |
  sha256sum --check --strict

reported_version=$(
  "$plugin_path" version |
    tr -d '\r' |
    head -n 1
)
if ! grep --extended-regexp --quiet \
  '(^|[[:space:]])v?0\.30\.1([[:space:]]|$)' <<<"$reported_version"; then
  echo "verified Buildx binary reported an unexpected version: $reported_version" >&2
  exit 1
fi

docker_reported_version=$(docker buildx version | tr -d '\r' | head -n 1)
if ! grep --extended-regexp --quiet \
  '(^|[[:space:]])v?0\.30\.1([[:space:]]|$)' <<<"$docker_reported_version"; then
  echo "Docker did not resolve the verified Buildx plugin: $docker_reported_version" >&2
  exit 1
fi

echo "installed and verified Buildx $buildx_version for linux/$asset_arch"
