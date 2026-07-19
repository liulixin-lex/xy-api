#!/usr/bin/env bash

set -Eeuo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <vMAJOR.MINOR.PATCH>" >&2
  exit 2
fi

version=$1
if [[ ! "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "invalid Electron release version: $version" >&2
  exit 2
fi

for command_name in cmp npm od sed sha256sum tr; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "required command is not installed: $command_name" >&2
    exit 1
  fi
done

repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
if [ ! -f "$repository_root/new-api.exe" ]; then
  echo "Windows backend binary is missing: $repository_root/new-api.exe" >&2
  exit 1
fi

cd "$repository_root/electron"
npm version "${version#v}" --no-git-tag-version --allow-same-version
npm ci
npm audit --audit-level=moderate
npm run build:win

cd dist
embedded_backend='win-unpacked/resources/bin/new-api.exe'
embedded_app='win-unpacked/resources/app.asar'
if [ ! -f "$embedded_backend" ] || [ ! -s "$embedded_backend" ]; then
  echo "Electron package is missing its embedded backend: $embedded_backend" >&2
  exit 1
fi
if [ ! -f "$embedded_app" ] || [ ! -s "$embedded_app" ]; then
  echo "Electron package is missing its application archive: $embedded_app" >&2
  exit 1
fi
if ! cmp -s "$repository_root/new-api.exe" "$embedded_backend"; then
  echo 'Electron package does not contain the backend binary built for this release.' >&2
  exit 1
fi
embedded_version=$("./$embedded_backend" --version | tr -d '\r')
if [ "$embedded_version" != "$version" ]; then
  echo "Electron embedded backend reports $embedded_version, expected $version" >&2
  exit 1
fi

shopt -s nullglob
desktop_version=${version#v}
source_portable="New-API-App ${desktop_version}.exe"
source_setup="New-API-App Setup ${desktop_version}.exe"
target_portable="New-API-App.${desktop_version}.exe"
target_setup="New-API-App.Setup.${desktop_version}.exe"

generated_installers=(*.exe)
if [ "${#generated_installers[@]}" -ne 2 ]; then
  echo "expected exactly two Electron Windows installers, found ${#generated_installers[@]}" >&2
  exit 1
fi
if [ ! -f "$source_portable" ] || [ ! -f "$source_setup" ]; then
  printf 'unexpected Electron installer inventory:\n' >&2
  printf '  %s\n' "${generated_installers[@]}" >&2
  exit 1
fi

mv -- "$source_portable" "$target_portable"
mv -- "$source_setup" "$target_setup"
installers=("$target_portable" "$target_setup")
for installer in "${installers[@]}"; do
  if [ ! -s "$installer" ]; then
    echo "Electron installer is empty: $installer" >&2
    exit 1
  fi
  pe_magic=$(od -An -N2 -tx1 "$installer" | tr -d '[:space:]')
  if [ "$pe_magic" != 4d5a ]; then
    echo "Electron installer is not a Windows PE executable: $installer" >&2
    exit 1
  fi
done
sha256sum "${installers[@]}" | sed 's/ \*/  /' >checksums-electron-windows.txt
sha256sum --check --strict checksums-electron-windows.txt

echo "built and verified Electron Windows assets for $version"
