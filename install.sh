#!/usr/bin/env bash
set -Eeuo pipefail

PREFIX="${PREFIX:-/opt/conan-manager}"
DATA_DIR="${DATA_DIR:-/var/lib/conan-manager}"
CONFIG_DIR="${CONFIG_DIR:-/etc/conan-manager}"
RUN_DIR="${RUN_DIR:-/run/conan-manager}"
SERVICE_USER="${SERVICE_USER:-conan-manager}"
SOURCE_BINARY="${SOURCE_BINARY:-./conan-manager}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

if [[ "${EUID}" -ne 0 ]]; then
  echo "Run as root: sudo ./install.sh" >&2
  exit 1
fi
for required in install getent groupadd useradd id openssl systemctl; do
  if ! command -v "${required}" >/dev/null 2>&1; then
    echo "Required command is missing: ${required}" >&2
    exit 1
  fi
done
if [[ ! -x "${SOURCE_BINARY}" ]]; then
  echo "Binary not found or not executable: ${SOURCE_BINARY}" >&2
  exit 1
fi

install -d -m 0755 "${PREFIX}" "${DATA_DIR}" "${CONFIG_DIR}" "${RUN_DIR}"
if ! getent group "${SERVICE_USER}" >/dev/null; then
  groupadd --system "${SERVICE_USER}"
fi
if ! id "${SERVICE_USER}" >/dev/null 2>&1; then
  useradd --system --gid "${SERVICE_USER}" --home-dir "${DATA_DIR}" --no-create-home --shell /usr/sbin/nologin "${SERVICE_USER}"
fi

install -m 0755 "${SOURCE_BINARY}" "${PREFIX}/conan-manager"
chown -R "${SERVICE_USER}:${SERVICE_USER}" "${DATA_DIR}" "${RUN_DIR}"
chown root:root "${CONFIG_DIR}"

if [[ ! -f "${CONFIG_DIR}/conan-manager.env" ]]; then
  umask 077
  cat > "${CONFIG_DIR}/conan-manager.env" <<EOF
CONAN_MANAGER_LISTEN=127.0.0.1:8080
CONAN_MANAGER_DATABASE=${DATA_DIR}/manager.db
CONAN_MANAGER_ADMIN_USERNAME=admin
CONAN_MANAGER_ADMIN_PASSWORD=$(openssl rand -base64 32 | tr -d '=+/\n' | cut -c1-32)
CONAN_MANAGER_SECRET_KEY=$(openssl rand -base64 48 | tr -d '=+/\n' | cut -c1-48)
EOF
fi
chmod 0600 "${CONFIG_DIR}/conan-manager.env"

install -m 0644 "${SCRIPT_DIR}/deploy/conan-manager.service" /etc/systemd/system/conan-manager.service
systemctl daemon-reload
systemctl enable conan-manager.service
systemctl restart conan-manager.service
systemctl --no-pager --full status conan-manager.service || true
echo "Installed Conan Manager at ${PREFIX}"
