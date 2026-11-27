#!/bin/sh
set -eu
arch=${1:-arm64}
case "$arch" in arm64|amd64) ;; *) echo "architecture must be arm64 or amd64" >&2; exit 2 ;; esac
cd "$(dirname "$0")/.."
mkdir -p bin
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags='-s -w' -o "bin/memeval-linux-$arch" ./cmd/memeval
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "bin/memeval-linux-$arch"
else
  shasum -a 256 "bin/memeval-linux-$arch"
fi
