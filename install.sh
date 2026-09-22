#!/usr/bin/env bash
# Installs Conan Manager on Ubuntu/Debian with systemd.
#
#   sudo ./install.sh                                # panel on 127.0.0.1:8080 only
#   sudo ./install.sh --domain manager.example.com   # plus Caddy with HTTPS
#   sudo ./install.sh --uninstall [--purge]          # remove (--purge: worlds too)
#
# Run it again to upgrade: the environment file, the database and the servers
# are kept; binaries, units and the Caddy site are replaced.
set -Eeuo pipefail
# Nothing may fail silently: name the line that stopped the installation.
trap 'echo "install.sh: failed at line ${LINENO}: ${BASH_COMMAND}" >&2' ERR

PREFIX="${PREFIX:-/opt/conan-manager}"
DATA_DIR="${DATA_DIR:-/var/lib/conan-manager}"
CONFIG_DIR="${CONFIG_DIR:-/etc/conan-manager}"
SERVICE_USER="${SERVICE_USER:-conan-manager}"
LISTEN="127.0.0.1:8080"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${CONFIG_DIR}/conan-manager.env"
CADDYFILE=/etc/caddy/Caddyfile
BINARIES="conan-manager conan-recover conan-launch conan-bind"

DOMAIN="" EMAIL="" ALLOW_IPS="" DEPS=1 UNINSTALL=0 PURGE=0

usage() {
  sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'
  cat <<'EOF'
Options:
  --domain NAME      serve the panel at https://NAME through Caddy
  --email ADDR       address for Let's Encrypt notices (optional)
  --allow-ips LIST   only these addresses/networks may open the panel,
                     e.g. "203.0.113.10,192.168.1.0/24"
  --no-deps          do not install packages with apt
  --uninstall        remove the manager; --purge also deletes servers,
                     backups and the database
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain) DOMAIN="${2:-}"; shift 2 ;;
    --email) EMAIL="${2:-}"; shift 2 ;;
    --allow-ips) ALLOW_IPS="${2:-}"; shift 2 ;;
    --no-deps) DEPS=0; shift ;;
    --uninstall) UNINSTALL=1; shift ;;
    --purge) PURGE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[[ "${EUID}" -eq 0 ]] || die "run as root: sudo $0 $*"
command -v systemctl >/dev/null && [[ -d /run/systemd/system ]] || die "systemd is required"

# ---------- uninstall ----------

if [[ "${UNINSTALL}" -eq 1 ]]; then
  say "stopping servers and the manager"
  for unit in $(systemctl list-units --all --plain --no-legend 'conan-server@*' 'conan-launch@*' | awk '{print $1}'); do
    systemctl stop "${unit}" || true
  done
  systemctl disable --now conan-manager.service 2>/dev/null || true
  for m in $(findmnt -rn -o TARGET | grep '^/run/conan-manager' | sort -r); do umount "$m" || warn "could not unmount $m"; done
  rm -f /etc/systemd/system/conan-manager.service /etc/systemd/system/conan-server@.service \
        /etc/systemd/system/conan-launch@.service /etc/polkit-1/rules.d/50-conan-manager.rules
  rm -rf "${PREFIX}" /run/conan-manager
  if [[ -f "${CADDYFILE}" ]] && grep -q '^# conan-manager begin' "${CADDYFILE}"; then
    sed -i '/^# conan-manager begin/,/^# conan-manager end/d' "${CADDYFILE}"
    systemctl reload caddy 2>/dev/null || true
    say "panel site removed from ${CADDYFILE} (Caddy itself is left installed)"
  fi
  systemctl daemon-reload
  if [[ "${PURGE}" -eq 1 ]]; then
    rm -rf "${DATA_DIR}" "${CONFIG_DIR}"
    userdel "${SERVICE_USER}" 2>/dev/null || true
    groupdel "${SERVICE_USER}" 2>/dev/null || true
    say "removed everything, including servers, backups and ${CONFIG_DIR}"
  else
    say "removed; kept ${DATA_DIR} (servers, backups, database) and ${CONFIG_DIR}"
  fi
  exit 0
fi

# ---------- checks ----------

if [[ -n "${DOMAIN}" && ! "${DOMAIN}" =~ ^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}$ ]]; then
  die "--domain must be a host name such as manager.example.com"
fi
[[ -z "${EMAIL}" || "${EMAIL}" =~ ^[^@[:space:]]+@[^@[:space:]]+$ ]] || die "--email is not an address"
[[ -z "${ALLOW_IPS}" || "${ALLOW_IPS}" =~ ^[0-9A-Fa-f:.,/[:space:]]+$ ]] || die "--allow-ips takes addresses and networks only"

# Binaries come from bin/ of a release package, or next to this script.
BIN_DIR="${SCRIPT_DIR}/bin"
[[ -x "${BIN_DIR}/conan-manager" ]] || BIN_DIR="${SCRIPT_DIR}"
[[ -x "${BIN_DIR}/conan-manager" ]] || die "conan-manager binary not found in ${SCRIPT_DIR}/bin; build it with scripts/package.sh"

if [[ -n "${DOMAIN}" ]]; then
  for port in 80 443; do
    holder="$(ss -Hltnp "sport = :${port}" 2>/dev/null | grep -o 'users:(("[^"]*' | head -1 | cut -d'"' -f2 || true)"
    if [[ -n "${holder}" && "${holder}" != caddy ]]; then
      die "port ${port} is taken by ${holder}; Caddy needs 80 and 443"
    fi
  done
fi

# ---------- packages ----------

apt_install() {
  local missing=()
  for p in "$@"; do dpkg -s "$p" >/dev/null 2>&1 || missing+=("$p"); done
  [[ ${#missing[@]} -eq 0 ]] && return 0
  say "installing ${missing[*]}"
  # Ubuntu's needrestart would otherwise print its service report here.
  local log; log="$(mktemp)"
  if ! DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1 apt-get install -y -qq "${missing[@]}" >"${log}" 2>&1; then
    cat "${log}" >&2; rm -f "${log}"; return 1
  fi
  rm -f "${log}"
}

if [[ "${DEPS}" -eq 1 ]]; then
  command -v apt-get >/dev/null || die "apt-get not found; install the dependencies yourself and use --no-deps"
  say "updating package lists"
  apt-get update -qq
  # SteamCMD is a 32-bit program; polkit lets the service start its game units.
  apt_install ca-certificates curl lib32gcc-s1 lib32stdc++6
  apt_install polkitd 2>/dev/null || apt_install policykit-1
fi

# ---------- user, directories, binaries ----------

if ! getent group "${SERVICE_USER}" >/dev/null; then groupadd --system "${SERVICE_USER}"; fi
if ! id "${SERVICE_USER}" >/dev/null 2>&1; then
  useradd --system --gid "${SERVICE_USER}" --home-dir "${DATA_DIR}" --no-create-home --shell /usr/sbin/nologin "${SERVICE_USER}"
fi

say "installing into ${PREFIX}"
install -d -m 0755 -o root -g root "${PREFIX}" "${CONFIG_DIR}"
# Worlds, backups and the database are nobody else's business.
install -d -m 0750 -o "${SERVICE_USER}" -g "${SERVICE_USER}" "${DATA_DIR}" "${DATA_DIR}/units" "${DATA_DIR}/backups"
# Hook scripts must be root-owned and not writable by the service user; the
# manager refuses anything else.
install -d -m 0755 -o root -g root "${CONFIG_DIR}/hooks"
for b in ${BINARIES}; do
  [[ -x "${BIN_DIR}/${b}" ]] || { [[ "$b" == conan-manager ]] && die "missing ${b}"; warn "${b} not in the package, skipped"; continue; }
  install -m 0755 -o root -g root "${BIN_DIR}/${b}" "${PREFIX}/${b}"
done
# The RAM launcher runs as root; only root may run it.
[[ -f "${PREFIX}/conan-launch" ]] && chmod 0700 "${PREFIX}/conan-launch"

# ---------- environment file ----------

random() { head -c 64 /dev/urandom | base64 | tr -d '=+/\n' | cut -c1-"$1"; }

NEW_PASSWORD=""
if [[ ! -f "${ENV_FILE}" ]]; then
  NEW_PASSWORD="$(random 24)"
  (umask 077; cat > "${ENV_FILE}" <<EOF
# Conan Manager. Keep this file root:root 0600. Every setting is described
# in deploy/conan-manager.env.example of the release package.
CONAN_MANAGER_LISTEN=${LISTEN}
CONAN_MANAGER_DATABASE=${DATA_DIR}/manager.db
CONAN_MANAGER_BACKUP_DIR=${DATA_DIR}/backups
CONAN_MANAGER_ADMIN_USERNAME=admin
CONAN_MANAGER_ADMIN_PASSWORD=${NEW_PASSWORD}
CONAN_MANAGER_SECRET_KEY=$(random 48)
CONAN_MANAGER_SERVER_UNITS=true
EOF
  )
  say "created ${ENV_FILE}"
fi
# The administrator's password and the secret key: root only. systemd reads
# the file as root before dropping to the service user.
chown root:root "${ENV_FILE}"
chmod 0600 "${ENV_FILE}"

# set_env replaces a variable in the environment file or adds it.
set_env() {
  local key="$1" value="$2"
  if grep -q "^${key}=" "${ENV_FILE}"; then
    sed -i "s|^${key}=.*|${key}=${value}|" "${ENV_FILE}"
  else
    printf '%s=%s\n' "${key}" "${value}" >> "${ENV_FILE}"
  fi
}

if [[ -n "${DOMAIN}" ]]; then
  # Caddy on this host is the only proxy believed for the client address.
  set_env CONAN_MANAGER_LISTEN "${LISTEN}"
  set_env CONAN_MANAGER_TRUSTED_PROXIES 127.0.0.1
  set_env CONAN_MANAGER_ALLOWED_ORIGIN "https://${DOMAIN}"
fi
[[ -n "${ALLOW_IPS}" ]] && set_env CONAN_MANAGER_ALLOWED_IPS "$(echo "${ALLOW_IPS}" | tr -s ' ,' ',' | sed 's/^,//;s/,$//')"

# ---------- units ----------

install -m 0644 "${SCRIPT_DIR}/deploy/conan-manager.service" /etc/systemd/system/conan-manager.service
# Game servers run as their own units so they survive manager restarts. The
# polkit rule lets the unprivileged manager start and stop exactly those.
install -m 0644 "${SCRIPT_DIR}/deploy/conan-server@.service" /etc/systemd/system/conan-server@.service
install -m 0644 "${SCRIPT_DIR}/deploy/conan-launch@.service" /etc/systemd/system/conan-launch@.service
if [[ -d /etc/polkit-1/rules.d ]]; then
  install -m 0644 -o root -g root "${SCRIPT_DIR}/deploy/50-conan-manager.rules" /etc/polkit-1/rules.d/50-conan-manager.rules
else
  warn "polkit is not installed: the manager cannot start server units; set CONAN_MANAGER_SERVER_UNITS=false or install polkitd"
fi
systemctl daemon-reload
systemctl enable --quiet conan-manager.service
systemctl restart conan-manager.service

say "waiting for the manager"
healthy=0
for _ in $(seq 1 30); do
  if curl -sf -m 2 "http://${LISTEN}/healthz" >/dev/null 2>&1; then healthy=1; break; fi
  sleep 1
done
if [[ "${healthy}" -ne 1 ]]; then
  journalctl -u conan-manager --no-pager -n 20 >&2 || true
  die "the manager did not start; see journalctl -u conan-manager"
fi

# ---------- Caddy ----------

if [[ -n "${DOMAIN}" ]]; then
  if ! command -v caddy >/dev/null; then
    [[ "${DEPS}" -eq 1 ]] || die "Caddy is not installed and --no-deps was given"
    say "installing Caddy"
    apt_install debian-keyring debian-archive-keyring apt-transport-https gnupg
    curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/gpg.key | gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt > /etc/apt/sources.list.d/caddy-stable.list
    apt-get update -qq
    apt_install caddy
  fi

  say "configuring https://${DOMAIN}"
  install -d -m 0755 /etc/caddy
  touch "${CADDYFILE}"
  backup="${CADDYFILE}.bak-$(date +%Y%m%d%H%M%S)"
  cp -a "${CADDYFILE}" "${backup}"
  sed -i '/^# conan-manager begin/,/^# conan-manager end/d' "${CADDYFILE}"
  # The packaged Caddyfile only serves a placeholder page on :80, which
  # would shadow the redirect to HTTPS; it is replaced. Any other sites the
  # operator keeps there stay.
  rest="$(grep -v '^[[:space:]]*#' "${CADDYFILE}" | tr -d '[:space:]' || true)"
  if [[ -z "${rest}" || "${rest}" == ':80{root*/usr/share/caddyfile_server}' ]]; then
    : > "${CADDYFILE}"
  fi
  if [[ -n "${EMAIL}" ]] && ! grep -q '^{' "${CADDYFILE}"; then
    printf '{\n\temail %s\n}\n\n' "${EMAIL}" | cat - "${CADDYFILE}" > "${CADDYFILE}.new" && mv "${CADDYFILE}.new" "${CADDYFILE}"
  fi
  cat >> "${CADDYFILE}" <<EOF
# conan-manager begin (managed by install.sh; edit outside these lines)
${DOMAIN} {
	request_body {
		max_size 2MB
	}
	reverse_proxy ${LISTEN}
}
# conan-manager end
EOF
  caddy validate --adapter caddyfile --config "${CADDYFILE}" >/dev/null 2>&1 || die "Caddy rejected ${CADDYFILE}; the previous version is in ${CADDYFILE}.bak-*"
  # A rerun that changes nothing leaves no backup behind.
  if cmp -s "${backup}" "${CADDYFILE}"; then rm -f "${backup}"; fi
  systemctl enable --quiet caddy
  systemctl reload caddy 2>/dev/null || systemctl restart caddy

  if command -v ufw >/dev/null && ufw status | grep -q '^Status: active'; then
    ufw allow 80/tcp comment 'Caddy' >/dev/null
    ufw allow 443/tcp comment 'Caddy' >/dev/null
    say "opened 80/tcp and 443/tcp in ufw"
  fi

  # DNS must point here and the router must forward 80 and 443, or Let's
  # Encrypt cannot reach Caddy.
  public_ip="$(curl -s -m 5 https://api.ipify.org || true)"
  dns_ip="$(getent ahostsv4 "${DOMAIN}" | awk 'NR==1{print $1}' || true)"
  if [[ -z "${dns_ip}" ]]; then
    warn "${DOMAIN} does not resolve yet; create an A record for ${public_ip:-the public address of this host}"
  elif [[ -n "${public_ip}" && "${dns_ip}" != "${public_ip}" ]]; then
    warn "${DOMAIN} points to ${dns_ip}, but this host is seen from outside as ${public_ip}"
  fi

  say "waiting for the certificate (up to 2 minutes)"
  cert=0
  for _ in $(seq 1 40); do
    # Asked through 127.0.0.1: a home router often cannot reach its own
    # public address from inside.
    if curl -sf -m 3 --resolve "${DOMAIN}:443:127.0.0.1" "https://${DOMAIN}/healthz" >/dev/null 2>&1; then cert=1; break; fi
    sleep 3
  done
  if [[ "${cert}" -ne 1 ]]; then
    warn "no certificate yet. Check that ${DOMAIN} points to ${public_ip:-this host} and that the router forwards TCP 80 and 443 here; Caddy keeps retrying: journalctl -u caddy -f"
  fi
fi

# ---------- summary ----------

echo
say "Conan Manager is installed"
if [[ -n "${DOMAIN}" ]]; then
  echo "    Panel:    https://${DOMAIN}/"
else
  echo "    Panel:    http://${LISTEN}/ (this host only; use an SSH tunnel or --domain)"
fi
echo "    Login:    $(grep '^CONAN_MANAGER_ADMIN_USERNAME=' "${ENV_FILE}" | cut -d= -f2-)"
if [[ -n "${NEW_PASSWORD}" ]]; then
  echo "    Password: ${NEW_PASSWORD}"
  echo "              (shown once; it is kept in ${ENV_FILE})"
else
  echo "    Password: sudo grep ADMIN_PASSWORD ${ENV_FILE}"
fi
echo "    Logs:     journalctl -u conan-manager -f"
echo "    Settings: ${ENV_FILE} (then systemctl restart conan-manager)"
