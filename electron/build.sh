#!/bin/bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:-$(cat "$ROOT_DIR/VERSION")}"
ELECTRON_VERSION="${VERSION#v}"
GO_LDFLAGS="-s -w -X github.com/QuantumNous/new-api/common.Version=${VERSION}"

if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "VERSION must use lowercase v semver, for example v0.0.1. Current value: $VERSION"
    exit 1
fi

echo "Building New API Electron App ($VERSION)..."

echo "Step 1: Installing frontend dependencies..."
cd "$ROOT_DIR/web"
bun install --frozen-lockfile

echo "Step 2: Building default frontend..."
cd "$ROOT_DIR/web/default"
DISABLE_ESLINT_PLUGIN='true' VITE_REACT_APP_VERSION="$VERSION" bun run build

echo "Step 3: Building classic frontend..."
cd "$ROOT_DIR/web/classic"
VITE_REACT_APP_VERSION="$VERSION" bun run build

echo "Step 4: Building Go backend..."
cd "$ROOT_DIR"
go mod download

if [[ "$OSTYPE" == "msys" || "$OSTYPE" == "cygwin" || "$OSTYPE" == "win32" ]]; then
    echo "Building backend for Windows..."
    CGO_ENABLED=0 GOEXPERIMENT=greenteagc GOOS=windows GOARCH="${GOARCH:-amd64}" go build -ldflags="$GO_LDFLAGS" -o new-api.exe
else
    echo "Building backend for current platform..."
    CGO_ENABLED=0 GOEXPERIMENT=greenteagc go build -ldflags="$GO_LDFLAGS" -o new-api
fi

echo "Step 5: Installing Electron dependencies..."
cd "$ROOT_DIR/electron"
npm ci

PACKAGE_JSON_BACKUP="$(mktemp)"
PACKAGE_LOCK_BACKUP="$(mktemp)"
cp package.json "$PACKAGE_JSON_BACKUP"
cp package-lock.json "$PACKAGE_LOCK_BACKUP"
cleanup_package_version() {
    cp "$PACKAGE_JSON_BACKUP" package.json
    cp "$PACKAGE_LOCK_BACKUP" package-lock.json
    rm -f "$PACKAGE_JSON_BACKUP" "$PACKAGE_LOCK_BACKUP"
}
trap cleanup_package_version EXIT

npm version "$ELECTRON_VERSION" --no-git-tag-version --allow-same-version

if [[ "$OSTYPE" == "darwin"* ]]; then
    echo "Building Electron package for macOS..."
    npm run build:mac
elif [[ "$OSTYPE" == "linux-gnu"* ]]; then
    echo "Building Electron package for Linux..."
    npm run build:linux
elif [[ "$OSTYPE" == "msys" || "$OSTYPE" == "cygwin" || "$OSTYPE" == "win32" ]]; then
    echo "Building Electron package for Windows..."
    npm run build:win
else
    echo "Unknown OS, building Electron package for current platform..."
    npm run build
fi

echo "Build complete. Check electron/dist/ for output."
