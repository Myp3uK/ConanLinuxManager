#!/usr/bin/env bash
# Builds the Linux binaries into bin/ of the working tree, where install.sh
# finds them:
#   scripts/build.sh && sudo ./install.sh --domain manager.example.com
#
# The version is stamped into the binary: the panel shows it and compares it
# with what is published to decide whether an update is on offer. A tree with
# uncommitted changes, or one with no tags, builds something that calls itself
# "dev" and is never offered an update.
set -euo pipefail
cd "$(dirname "$0")/.."
command -v go >/dev/null || { echo "Go is not installed; see docs/install.md" >&2; exit 1; }

MODULE=github.com/Myp3uK/ConanLinuxManager
VERSION="${VERSION:-$(git describe --tags --dirty --always 2>/dev/null || echo dev)}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || true)}"
DATE="${DATE:-$(date -u +%Y-%m-%d)}"
LDFLAGS="-s -w"
LDFLAGS+=" -X ${MODULE}/internal/version.Version=${VERSION}"
LDFLAGS+=" -X ${MODULE}/internal/version.Commit=${COMMIT}"
LDFLAGS+=" -X ${MODULE}/internal/version.Date=${DATE}"

echo "version ${VERSION}"
mkdir -p bin
for b in conan-manager conan-recover conan-launch conan-bind; do
  echo "building ${b}"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "${LDFLAGS}" -o "bin/${b}" "./cmd/${b}"
done
ls -l bin
