#!/usr/bin/env bash

set -Eeuo pipefail

bun_version=1.3.14
# SHA-256 values below are the digest fields published on the official
# oven-sh/bun GitHub Release assets for this exact version.

for command_name in curl unzip; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "required command is not installed: $command_name" >&2
    exit 1
  fi
done

runner_os=${RUNNER_OS:-$(uname -s)}
runner_arch=${RUNNER_ARCH:-$(uname -m)}
binary_name=bun
bunx_name=bunx

case "$runner_os" in
  Linux | linux)
    case "$runner_arch" in
      X64 | x64 | amd64 | x86_64)
        asset_name=bun-linux-x64.zip
        expected_sha256=951ee2aee855f08595aeec6225226a298d3fea83a3dcd6465c09cbccdf7e848f
        ;;
      ARM64 | arm64 | aarch64)
        asset_name=bun-linux-aarch64.zip
        expected_sha256=a27ffb63a8310375836e0d6f668ae17fa8d8d18b88c37c821c65331973a19a3b
        ;;
      *)
        echo "unsupported Bun Linux architecture: $runner_arch" >&2
        exit 1
        ;;
    esac
    ;;
  macOS | macos | Darwin)
    case "$runner_arch" in
      X64 | x64 | amd64 | x86_64)
        asset_name=bun-darwin-x64.zip
        expected_sha256=4183df3374623e5bab315c547cfa0974533cd457d86b73b639f7a87974cd6633
        ;;
      ARM64 | arm64 | aarch64)
        asset_name=bun-darwin-aarch64.zip
        expected_sha256=d8b96221828ad6f97ac7ac0ab7e95872341af763001e8803e8267652c2652620
        ;;
      *)
        echo "unsupported Bun macOS architecture: $runner_arch" >&2
        exit 1
        ;;
    esac
    ;;
  Windows | windows | MINGW* | MSYS* | CYGWIN*)
    case "$runner_arch" in
      X64 | x64 | amd64 | x86_64)
        asset_name=bun-windows-x64.zip
        expected_sha256=0a0620930b6675d7ba440e81f4e0e00d3cfbe096c4b140d3fff02205e9e18922
        binary_name=bun.exe
        bunx_name=bunx.exe
        ;;
      *)
        echo "unsupported Bun Windows architecture: $runner_arch" >&2
        exit 1
        ;;
    esac
    ;;
  *)
    echo "unsupported Bun runner OS: $runner_os" >&2
    exit 1
    ;;
esac

if command -v sha256sum >/dev/null 2>&1; then
  sha256_file() {
    sha256sum "$1" | awk '{print $1}'
  }
elif command -v shasum >/dev/null 2>&1; then
  sha256_file() {
    shasum -a 256 "$1" | awk '{print $1}'
  }
else
  echo 'required SHA-256 utility is not installed' >&2
  exit 1
fi

asset_url="https://github.com/oven-sh/bun/releases/download/bun-v${bun_version}/${asset_name}"
work_directory=$(mktemp -d)
archive_path="$work_directory/$asset_name"
extract_directory="$work_directory/extracted"
asset_directory=${asset_name%.zip}
extracted_binary="$extract_directory/$asset_directory/$binary_name"
install_directory="$HOME/.bun/bin"
installed_binary="$install_directory/$binary_name"
staged_binary="$install_directory/.bun-${bun_version}-$$"
installed_bunx="$install_directory/$bunx_name"
staged_bunx="$install_directory/.bunx-${bun_version}-$$"

cleanup() {
  rm -rf "$work_directory"
  rm -f "$staged_binary"
  rm -f "$staged_bunx"
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
  --output "$archive_path"

actual_sha256=$(sha256_file "$archive_path")
if [ "$actual_sha256" != "$expected_sha256" ]; then
  echo "Bun archive checksum mismatch: expected $expected_sha256, got $actual_sha256" >&2
  exit 1
fi

mkdir -p "$extract_directory"
unzip -q "$archive_path" -d "$extract_directory"
if [ ! -f "$extracted_binary" ] || [ -L "$extracted_binary" ]; then
  echo "verified Bun archive does not contain the expected regular binary: $extracted_binary" >&2
  exit 1
fi

mkdir -p "$install_directory"
extracted_binary_sha256=$(sha256_file "$extracted_binary")
cp "$extracted_binary" "$staged_binary"
chmod 0755 "$staged_binary"
mv -f "$staged_binary" "$installed_binary"

installed_binary_sha256=$(sha256_file "$installed_binary")
if [ "$installed_binary_sha256" != "$extracted_binary_sha256" ]; then
  echo 'installed Bun binary differs from the verified archive payload' >&2
  exit 1
fi

case "$runner_os" in
  Windows | windows | MINGW* | MSYS* | CYGWIN*)
    cp "$installed_binary" "$staged_bunx"
    chmod 0755 "$staged_bunx"
    ;;
  *)
    ln -s "$binary_name" "$staged_bunx"
    ;;
esac
mv -f "$staged_bunx" "$installed_bunx"

PATH="$install_directory:$PATH"
export PATH
hash -r
resolved_binary=$(command -v bun)
resolved_bunx=$(command -v bunx)
resolved_binary_sha256=$(sha256_file "$resolved_binary")
resolved_bunx_sha256=$(sha256_file "$resolved_bunx")
if [ "$resolved_binary_sha256" != "$installed_binary_sha256" ] || \
   [ "$resolved_bunx_sha256" != "$installed_binary_sha256" ]; then
  echo 'the Bun executable path does not resolve to the verified binary' >&2
  exit 1
fi

reported_version=$(bun --version | tr -d '\r')
if [ "$reported_version" != "$bun_version" ]; then
  echo "verified Bun binary reported $reported_version, expected $bun_version" >&2
  exit 1
fi
reported_bunx_version=$(bunx --version | tr -d '\r')
if [ "$reported_bunx_version" != "$bun_version" ]; then
  echo "verified bunx reported $reported_bunx_version, expected $bun_version" >&2
  exit 1
fi

if [ -n "${GITHUB_PATH:-}" ]; then
  path_for_runner=$install_directory
  case "$runner_os" in
    Windows | windows | MINGW* | MSYS* | CYGWIN*)
      if command -v cygpath >/dev/null 2>&1; then
        path_for_runner=$(cygpath -w "$install_directory")
      fi
      ;;
  esac
  printf '%s\n' "$path_for_runner" >>"$GITHUB_PATH"
fi

echo "installed and verified Bun $bun_version from $asset_name at $resolved_binary ($resolved_binary_sha256)"
