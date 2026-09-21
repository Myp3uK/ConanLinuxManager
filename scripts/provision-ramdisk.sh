#!/usr/bin/env bash
# Create the tmpfs mount unit for one server's RAM database directory.
#
# systemd derives a mount unit's name from its mount point, so a template unit
# such as conan-manager-tmpfs@.mount is rejected with "Invalid argument". The
# unit therefore has to be generated per server, which is what this does.
#
#   sudo scripts/provision-ramdisk.sh ue5-test 2048
set -euo pipefail

ID="${1:-}"
SIZE_MB="${2:-2048}"
SERVICE_USER="${SERVICE_USER:-conan-manager}"
RUN_DIR="${RUN_DIR:-/run/conan-manager}"

if [[ -z "$ID" ]]; then
  echo "usage: $0 <server-id> [size-mb]" >&2
  exit 2
fi
if [[ ! "$ID" =~ ^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$ ]]; then
  echo "server id must match [a-zA-Z0-9][a-zA-Z0-9_-]{0,63}: $ID" >&2
  exit 2
fi
if [[ "$SIZE_MB" -lt 128 ]]; then
  echo "size must be at least 128 MB" >&2
  exit 2
fi
if [[ "${EUID}" -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi
if ! id "$SERVICE_USER" >/dev/null 2>&1; then
  echo "service user does not exist: $SERVICE_USER" >&2
  exit 1
fi

WHERE="${RUN_DIR}/${ID}"
UNIT="$(systemd-escape --path --suffix=mount "$WHERE")"

install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" "$RUN_DIR"
install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" "$WHERE"

cat > "/etc/systemd/system/${UNIT}" <<UNIT_EOF
[Unit]
Description=Conan Manager RAM database for ${ID}
Before=conan-manager.service
After=local-fs.target

[Mount]
What=tmpfs
Where=${WHERE}
Type=tmpfs
# uid/gid are required: without them the tmpfs root belongs to root and the
# manager, which runs unprivileged, cannot create its database directory.
Options=mode=0750,uid=${SERVICE_USER},gid=${SERVICE_USER},size=${SIZE_MB}M,nosuid,nodev,noexec

[Install]
WantedBy=multi-user.target
UNIT_EOF

systemctl daemon-reload
systemctl enable --now "$UNIT"
findmnt -o TARGET,FSTYPE,SIZE,OPTIONS "$WHERE"
ls -ld "$WHERE"
echo "provisioned ${UNIT}"
