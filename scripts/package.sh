#!/usr/bin/env bash
# Builds a release package for Linux amd64:
#   dist/conan-manager-<version>-linux-amd64.tar.gz
# with bin/, deploy/, install.sh and README.md. On the target:
#   tar xzf conan-manager-*.tar.gz && cd conan-manager-* && sudo ./install.sh --domain ...
set -euo pipefail
cd "$(dirname "$0")/.."
version="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || date +%Y%m%d)}"
name="conan-manager-${version}-linux-amd64"
out="dist/${name}"
rm -rf "${out}" && mkdir -p "${out}/bin"
for b in conan-manager conan-recover conan-launch conan-bind; do
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o "${out}/bin/${b}" "./cmd/${b}"
done
cp -r deploy install.sh README.md "${out}/"
tar -C dist -czf "dist/${name}.tar.gz" --owner=0 --group=0 --mode='u+rwX,go+rX,go-w' "${name}"
echo "dist/${name}.tar.gz"
