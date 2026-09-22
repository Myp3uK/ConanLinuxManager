#!/usr/bin/env bash
# Builds the Linux binaries into bin/ of the working tree, where install.sh
# finds them:
#   scripts/build.sh && sudo ./install.sh --domain manager.example.com
set -euo pipefail
cd "$(dirname "$0")/.."
command -v go >/dev/null || { echo "Go is not installed; see README, section 'Установка из исходников'" >&2; exit 1; }
mkdir -p bin
for b in conan-manager conan-recover conan-launch conan-bind; do
  echo "building ${b}"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o "bin/${b}" "./cmd/${b}"
done
ls -l bin
