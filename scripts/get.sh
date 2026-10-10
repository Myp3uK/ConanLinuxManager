#!/usr/bin/env bash
# One-command install of Conan Manager on Ubuntu:
#
#   curl -fsSL https://raw.githubusercontent.com/Myp3uK/ConanLinuxManager/main/scripts/get.sh \
#     | sudo bash -s -- --domain manager.example.com
#
# It takes the newest published release, checks it against the checksum
# published beside it, unpacks it and hands over to install.sh, which does the
# actual work: the service user, the directories, the environment file, the
# systemd units and, with --domain, Caddy with a Let's Encrypt certificate.
#
# Everything after -- is passed to install.sh untouched, so --no-deps and the
# rest work the same way.
#
# You are about to run a script from the internet as root. Read it first:
#   curl -fsSL .../get.sh | less
set -euo pipefail

REPO="${CONAN_MANAGER_REPO:-Myp3uK/ConanLinuxManager}"
VERSION="${CONAN_MANAGER_VERSION:-}"   # empty means the newest release
FROM_SOURCE=0
ARGS=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="${2:-}"; shift 2 ;;
    --from-source) FROM_SOURCE=1; shift ;;
    --help|-h)
      # Print the comment block at the top, however long it grows.
      awk 'NR>1 && /^#/ { sub(/^# ?/, ""); print; next } NR>1 { exit }' "$0"
      echo
      echo "  --version TAG    install that release instead of the newest"
      echo "  --from-source    clone and build with Go instead of downloading a release"
      echo "  everything else is passed to install.sh (try --help there)"
      exit 0 ;;
    *) ARGS+=("$1"); shift ;;
  esac
done

die() { echo "error: $*" >&2; exit 1; }
note() { echo "==> $*"; }

[[ "${EUID}" -eq 0 ]] || die "run as root: pipe into 'sudo bash' rather than 'bash'"
[[ "$(uname -s)" == "Linux" ]] || die "this installs a Linux service"
[[ "$(uname -m)" == "x86_64" ]] || die "only x86_64 is published; use --from-source on $(uname -m)"
[[ -d /run/systemd/system ]] || die "systemd is required"
command -v curl >/dev/null || die "curl is required"
command -v tar >/dev/null || die "tar is required"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

install_from_source() {
  command -v git >/dev/null || die "git is required for --from-source"
  command -v go >/dev/null || die "Go 1.24+ is required for --from-source; see docs/install.md"
  note "cloning ${REPO}"
  git clone --depth 1 "https://github.com/${REPO}.git" "${WORK}/src"
  cd "${WORK}/src"
  note "building"
  scripts/build.sh
  note "installing"
  ./install.sh "${ARGS[@]+"${ARGS[@]}"}"
}

if [[ "${FROM_SOURCE}" -eq 1 ]]; then
  install_from_source
  exit 0
fi

if [[ -z "${VERSION}" ]]; then
  note "asking GitHub for the newest release"
  # The API answers with the release object; the tag is enough to build the
  # download URLs, so no JSON parser is needed on the target.
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1 || true)"
fi

if [[ -z "${VERSION}" ]]; then
  note "no published release found; building from source instead"
  install_from_source
  exit 0
fi

NAME="conan-manager-${VERSION}-linux-amd64"
BASE="https://github.com/${REPO}/releases/download/${VERSION}"

note "downloading ${NAME}"
curl -fsSL -o "${WORK}/${NAME}.tar.gz" "${BASE}/${NAME}.tar.gz" \
  || die "could not download ${BASE}/${NAME}.tar.gz"
curl -fsSL -o "${WORK}/${NAME}.tar.gz.sha256" "${BASE}/${NAME}.tar.gz.sha256" \
  || die "the release has no checksum file; refusing to install it unchecked"

note "checking the archive against its checksum"
( cd "${WORK}" && sha256sum --check --status "${NAME}.tar.gz.sha256" ) \
  || die "checksum does not match: the download is damaged or tampered with"

tar -C "${WORK}" -xzf "${WORK}/${NAME}.tar.gz"
cd "${WORK}/${NAME}"
note "installing ${VERSION}"
./install.sh "${ARGS[@]+"${ARGS[@]}"}"
