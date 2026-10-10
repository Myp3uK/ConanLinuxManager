#!/usr/bin/env bash
# Builds a release package for Linux amd64:
#   dist/conan-manager-<version>-linux-amd64.tar.gz
# with bin/, deploy/, install.sh, README.md and a checksum file. On the target:
#   tar xzf conan-manager-*.tar.gz && cd conan-manager-* && sudo ./install.sh --domain ...
set -euo pipefail
cd "$(dirname "$0")/.."
version="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || date +%Y%m%d)}"
name="conan-manager-${version}-linux-amd64"
out="dist/${name}"
rm -rf "${out}" && mkdir -p "${out}"

# One place builds the binaries, so the package carries the same stamped
# version the panel shows.
VERSION="${version}" scripts/build.sh >/dev/null
cp -r bin "${out}/bin"
cp -r deploy install.sh README.md "${out}/"
cp -r scripts "${out}/scripts"

tar -C dist -czf "dist/${name}.tar.gz" --owner=0 --group=0 --mode='u+rwX,go+rX,go-w' "${name}"
( cd dist && sha256sum "${name}.tar.gz" > "${name}.tar.gz.sha256" )
echo "dist/${name}.tar.gz"
cat "dist/${name}.tar.gz.sha256"
