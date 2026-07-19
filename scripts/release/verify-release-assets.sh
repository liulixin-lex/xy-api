#!/usr/bin/env bash

set -Eeuo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <release-assets-directory> <version>" >&2
  exit 2
fi

assets_directory=$1
version=$2

if [[ ! "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "invalid release version: $version" >&2
  exit 2
fi

if [ ! -d "$assets_directory" ]; then
  echo "release assets directory does not exist: $assets_directory" >&2
  exit 1
fi

expected_files=(
  "new-api-$version"
  "new-api-arm64-$version"
  "new-api-macos-$version"
  "new-api-$version.exe"
  checksums-linux.txt
  checksums-macos.txt
  checksums-windows.txt
)
aggregate_checksum_file=checksums.txt

for expected_file in "${expected_files[@]}"; do
  if [ ! -f "$assets_directory/$expected_file" ]; then
    echo "missing release asset: $expected_file" >&2
    exit 1
  fi
  if [ -L "$assets_directory/$expected_file" ]; then
    echo "release asset must not be a symbolic link: $expected_file" >&2
    exit 1
  fi
done

while IFS= read -r actual_entry; do
  allowed=false
  if [ "$actual_entry" = "$aggregate_checksum_file" ]; then
    allowed=true
  else
    for expected_file in "${expected_files[@]}"; do
      if [ "$actual_entry" = "$expected_file" ]; then
        allowed=true
        break
      fi
    done
  fi
  if [ "$allowed" != true ]; then
    echo "unexpected release asset: $actual_entry" >&2
    exit 1
  fi
  if [ -L "$assets_directory/$actual_entry" ] || [ ! -f "$assets_directory/$actual_entry" ]; then
    echo "release asset must be a regular file: $actual_entry" >&2
    exit 1
  fi
done < <(find "$assets_directory" -mindepth 1 -maxdepth 1 -printf '%f\n' | sort)

validate_checksum_inventory() {
  local checksum_file=$1
  shift
  local expected_names=("$@")
  local actual_names=()

  if ! awk 'NF != 2 || $1 !~ /^[0-9a-f]{64}$/ { exit 1 }' "$checksum_file"; then
    echo "invalid checksum file format: ${checksum_file##*/}" >&2
    exit 1
  fi
  mapfile -t actual_names < <(awk '{
    name = $2
    sub(/^\*/, "", name)
    sub(/^\.\//, "", name)
    print name
  }' "$checksum_file")
  if [ "${#actual_names[@]}" -ne "${#expected_names[@]}" ]; then
    echo "unexpected checksum entry count in ${checksum_file##*/}" >&2
    exit 1
  fi
  for index in "${!expected_names[@]}"; do
    if [ "${actual_names[$index]}" != "${expected_names[$index]}" ]; then
      echo "unexpected checksum entry in ${checksum_file##*/}: ${actual_names[$index]}" >&2
      exit 1
    fi
  done
}

(
  cd "$assets_directory"
  validate_checksum_inventory \
    checksums-linux.txt \
    "new-api-$version" \
    "new-api-arm64-$version"
  validate_checksum_inventory checksums-macos.txt "new-api-macos-$version"
  validate_checksum_inventory checksums-windows.txt "new-api-$version.exe"
  sha256sum --check --strict checksums-linux.txt
  sha256sum --check --strict checksums-macos.txt
  sha256sum --check --strict checksums-windows.txt

  generated_checksums=$(mktemp)
  trap 'rm -f "$generated_checksums"' EXIT
  sha256sum \
    "new-api-$version" \
    "new-api-arm64-$version" \
    "new-api-macos-$version" \
    "new-api-$version.exe" >"$generated_checksums"

  if [ -f "$aggregate_checksum_file" ]; then
    if [ -L "$aggregate_checksum_file" ]; then
      echo "$aggregate_checksum_file must not be a symbolic link" >&2
      exit 1
    fi
    if ! cmp -s "$generated_checksums" "$aggregate_checksum_file"; then
      echo "$aggregate_checksum_file does not match the platform checksums" >&2
      exit 1
    fi
  else
    install -m 0644 "$generated_checksums" "$aggregate_checksum_file"
  fi
  validate_checksum_inventory \
    "$aggregate_checksum_file" \
    "new-api-$version" \
    "new-api-arm64-$version" \
    "new-api-macos-$version" \
    "new-api-$version.exe"
  sha256sum --check --strict "$aggregate_checksum_file"
)

echo "verified release assets and checksums for $version"
