#!/usr/bin/env bash
# Replaces the manager with the newest published release and restarts it.
#
# install.sh puts this at /opt/conan-manager/self-update.sh, owned by root, and
# conan-manager-update.service runs it. The manager itself runs unprivileged
# and may only ask systemd to start that unit - it cannot say what to install.
# Everything that decides what lands on disk is here, in a root-owned file:
#
#   - only the repository below, only over HTTPS;
#   - only a release whose archive matches the checksum published beside it;
#   - only the binaries and the unit files. The environment file, the service
#     user, the data directory and the web server are install.sh's business
#     and are left alone.
#
# Run it by hand to update from a terminal:  sudo /opt/conan-manager/self-update.sh
set -euo pipefail

REPO="${CONAN_MANAGER_REPO:-Myp3uK/ConanLinuxManager}"
PREFIX="${PREFIX:-/opt/conan-manager}"
UNIT_DIR=/etc/systemd/system
BINARIES="conan-manager conan-recover conan-launch conan-bind"

say() { printf '==> %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

[[ "${EUID}" -eq 0 ]] || die "must run as root"
command -v curl >/dev/null || die "curl is required"

current="$("${PREFIX}/conan-manager" --version 2>/dev/null | awk '{print $1}')" || true
say "installed: ${current:-unknown}"

say "asking GitHub for the newest release"
latest="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
  | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
[[ -n "${latest}" ]] || die "no published release found"
say "published: ${latest}"

if [[ "${latest}" == "${current}" ]]; then
  say "already up to date"
  exit 0
fi

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
NAME="conan-manager-${latest}-linux-amd64"
BASE="https://github.com/${REPO}/releases/download/${latest}"

say "downloading ${NAME}"
curl -fsSL -o "${WORK}/${NAME}.tar.gz" "${BASE}/${NAME}.tar.gz" || die "download failed"
curl -fsSL -o "${WORK}/${NAME}.tar.gz.sha256" "${BASE}/${NAME}.tar.gz.sha256" \
  || die "the release has no checksum; refusing to install it unchecked"
( cd "${WORK}" && sha256sum --check --status "${NAME}.tar.gz.sha256" ) \
  || die "checksum does not match: the download is damaged or tampered with"

tar -C "${WORK}" -xzf "${WORK}/${NAME}.tar.gz"
SRC="${WORK}/${NAME}"
[[ -x "${SRC}/bin/conan-manager" ]] || die "the archive has no conan-manager binary"

# Keep what is running until the new files are in place, so a failure here
# leaves a working manager rather than half of one.
say "installing ${latest}"
for b in ${BINARIES}; do
  [[ -f "${SRC}/bin/${b}" ]] || continue
  install -m 0755 -o root -g root "${SRC}/bin/${b}" "${PREFIX}/${b}.new"
  mv -f "${PREFIX}/${b}.new" "${PREFIX}/${b}"
done
install -m 0755 -o root -g root "${SRC}/scripts/self-update.sh" "${PREFIX}/self-update.sh.new"
mv -f "${PREFIX}/self-update.sh.new" "${PREFIX}/self-update.sh"

# Unit files travel with the release; the environment file never does.
for u in conan-manager.service conan-server@.service conan-launch@.service conan-manager-update.service; do
  [[ -f "${SRC}/deploy/${u}" ]] && install -m 0644 -o root -g root "${SRC}/deploy/${u}" "${UNIT_DIR}/${u}"
done
if [[ -f "${SRC}/deploy/50-conan-manager.rules" && -d /etc/polkit-1/rules.d ]]; then
  install -m 0644 -o root -g root "${SRC}/deploy/50-conan-manager.rules" /etc/polkit-1/rules.d/50-conan-manager.rules
fi

systemctl daemon-reload
say "restarting the manager"
systemctl restart conan-manager.service
say "now running: $("${PREFIX}/conan-manager" --version 2>/dev/null || echo unknown)"
