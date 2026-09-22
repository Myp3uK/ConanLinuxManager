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

install -d -m 0755 "${PREFIX}" "${CONFIG_DIR}"
# Worlds, backups and the database are nobody else's business.
install -d -m 0750 "${DATA_DIR}" "${DATA_DIR}/units" "${RUN_DIR}"
# Hook scripts must be root-owned and not writable by the service user; the
# manager refuses anything else.
install -d -m 0755 -o root -g root "${CONFIG_DIR}/hooks"
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
CONAN_MANAGER_SERVER_UNITS=true
EOF
fi
# The administrator's password and the secret key: root only. systemd reads
# the file as root before dropping to the service user.
chown root:root "${CONFIG_DIR}/conan-manager.env"
chmod 0600 "${CONFIG_DIR}/conan-manager.env"

install -m 0644 "${SCRIPT_DIR}/deploy/conan-manager.service" /etc/systemd/system/conan-manager.service
# Game servers run as their own units so they survive manager restarts. The
# polkit rule lets the unprivileged manager start and stop exactly those.
install -m 0644 "${SCRIPT_DIR}/deploy/conan-server@.service" /etc/systemd/system/conan-server@.service
if [[ -d /etc/polkit-1/rules.d ]]; then
  install -m 0644 -o root -g root "${SCRIPT_DIR}/deploy/50-conan-manager.rules" /etc/polkit-1/rules.d/50-conan-manager.rules
else
  echo "polkit is not installed: set CONAN_MANAGER_SERVER_UNITS=false or install polkitd" >&2
fi
systemctl daemon-reload
systemctl enable conan-manager.service
systemctl restart conan-manager.service
systemctl --no-pager --full status conan-manager.service || true
echo "Installed Conan Manager at ${PREFIX}"
echo "Administrator password: sudo grep ADMIN_PASSWORD ${CONFIG_DIR}/conan-manager.env"
echo "The panel listens on 127.0.0.1:8080; put HTTPS in front of it (deploy/nginx-conan-manager.conf.example)."
