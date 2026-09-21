#!/usr/bin/env bash
# Build, test and deploy the manager on the Linux acceptance host.
#
# The host needs a Go toolchain; sources are synced from this working tree so
# uncommitted fixes can be verified before they are committed. Deploy keeps the
# previous binaries and rolls back when the new unit fails its health check.
#
#   SSH_BIN=/c/WINDOWS/System32/OpenSSH/ssh.exe scripts/node2.sh test
set -euo pipefail

HOST="${CONAN_TEST_HOST:-root@node2}"
SSH_BIN="${SSH_BIN:-ssh}"
SRC="/root/conan-src"
PREFIX="/opt/conan-manager"
BINARIES="conan-manager conan-launch conan-recover conan-bind"

remote() { "$SSH_BIN" -o BatchMode=yes "$HOST" "$@"; }
step() { printf '\n=== %s ===\n' "$1"; }

sync_sources() {
  step "sync $(pwd) -> $HOST:$SRC"
  tar -czf - --exclude='.git' --exclude='*.test' --exclude='conan-*-linux-amd64' \
      --exclude='__pycache__' --exclude='.claude' . |
    remote "rm -rf $SRC && mkdir -p $SRC && tar -xzf - -C $SRC"
  remote "ls $SRC >/dev/null && echo ok"
}

run_tests() {
  local packages="${1:-./...}"
  step "go vet"
  remote "cd $SRC && PATH=\$PATH:/usr/local/go/bin go vet ./..."
  step "go test -race $packages"
  # The exit status decides whether a deploy may proceed, so it is captured
  # before the output is filtered rather than lost in the pipeline.
  # "|| status=$?" matters: under set -e a failing substitution would end the
  # script here, before the failures were printed.
  local output status=0
  output="$(remote "cd $SRC && PATH=\$PATH:/usr/local/go/bin go test -race -count=1 $packages" 2>&1)" || status=$?
  printf '%s\n' "$output" | grep -vE 'no test files'
  if [[ $status -ne 0 ]]; then
    echo "TESTS FAILED - refusing to continue" >&2
    return 1
  fi
}

build_binaries() {
  step "build"
  remote "cd $SRC && mkdir -p bin && for b in $BINARIES; do
    PATH=\$PATH:/usr/local/go/bin go build -o bin/\$b ./cmd/\$b || exit 1
  done && ls -la bin/"
}

deploy() {
  local stamp; stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  step "deploy (rollback stamp $stamp)"
  remote "bash -s" <<REMOTE_EOF
set -euo pipefail
mkdir -p $PREFIX/rollback/$stamp
for b in $BINARIES; do
  [ -f $PREFIX/\$b ] && cp -a $PREFIX/\$b $PREFIX/rollback/$stamp/\$b || true
done
cp -a /etc/systemd/system/conan-manager.service $PREFIX/rollback/$stamp/ 2>/dev/null || true

systemctl stop conan-manager.service
for b in $BINARIES; do
  install -m 0755 -o root -g root $SRC/bin/\$b $PREFIX/\$b
done
chmod 0700 $PREFIX/conan-launch
install -m 0644 $SRC/deploy/conan-manager.service /etc/systemd/system/conan-manager.service
install -m 0644 $SRC/deploy/conan-launch@.service /etc/systemd/system/conan-launch@.service
install -m 0644 $SRC/deploy/conan-server@.service /etc/systemd/system/conan-server@.service
install -m 0644 -o root -g root $SRC/deploy/50-conan-manager.rules /etc/polkit-1/rules.d/50-conan-manager.rules
install -d -m 0750 -o conan-manager -g conan-manager /var/lib/conan-manager/units
install -d -m 0755 -o root -g root /etc/conan-manager/hooks
systemctl daemon-reload
systemctl start conan-manager.service

for i in \$(seq 1 20); do
  sleep 1
  if curl -sf -m 3 http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
    echo "health ok after \${i}s"
    systemctl is-active conan-manager.service
    echo "$stamp" > $PREFIX/rollback/LAST
    exit 0
  fi
done

echo "HEALTH CHECK FAILED - rolling back to $stamp" >&2
systemctl stop conan-manager.service || true
for b in $BINARIES; do
  [ -f $PREFIX/rollback/$stamp/\$b ] && install -m 0755 $PREFIX/rollback/$stamp/\$b $PREFIX/\$b || true
done
[ -f $PREFIX/rollback/$stamp/conan-manager.service ] &&
  install -m 0644 $PREFIX/rollback/$stamp/conan-manager.service /etc/systemd/system/conan-manager.service || true
systemctl daemon-reload
systemctl start conan-manager.service || true
journalctl -u conan-manager.service -n 30 --no-pager >&2
exit 1
REMOTE_EOF
}

rollback() {
  local stamp="${1:-}"
  step "rollback"
  remote "bash -s" <<REMOTE_EOF
set -euo pipefail
stamp="${stamp}"
[ -z "\$stamp" ] && stamp="\$(cat $PREFIX/rollback/LAST 2>/dev/null || true)"
[ -z "\$stamp" ] && { echo "no rollback point"; exit 1; }
[ -d "$PREFIX/rollback/\$stamp" ] || { echo "unknown rollback point \$stamp"; exit 1; }
systemctl stop conan-manager.service || true
for b in $BINARIES; do
  [ -f $PREFIX/rollback/\$stamp/\$b ] && install -m 0755 $PREFIX/rollback/\$stamp/\$b $PREFIX/\$b || true
done
[ -f $PREFIX/rollback/\$stamp/conan-manager.service ] &&
  install -m 0644 $PREFIX/rollback/\$stamp/conan-manager.service /etc/systemd/system/conan-manager.service || true
systemctl daemon-reload
systemctl start conan-manager.service
echo "rolled back to \$stamp"
REMOTE_EOF
}

status() {
  step "status"
  remote "bash -s" <<'REMOTE_EOF'
systemctl is-active conan-manager.service && systemctl show conan-manager.service -p MainPID,ExecMainStartTimestamp --no-pager
echo "--- health ---"
curl -s -m 3 http://127.0.0.1:8080/healthz || echo "(unreachable)"
echo
echo "--- conan mounts ---"
findmnt -rn -o TARGET,SIZE | grep -i conan || echo "(none)"
echo "--- valheim (must stay running) ---"
systemctl is-active valheim.service
REMOTE_EOF
}

case "${1:-}" in
  sync)     sync_sources ;;
  test)     sync_sources; run_tests "${2:-./...}" ;;
  build)    sync_sources; build_binaries ;;
  deploy)   sync_sources; run_tests; build_binaries; deploy; status ;;
  rollback) rollback "${2:-}" ;;
  status)   status ;;
  *) echo "usage: $0 {sync|test [pkgs]|build|deploy|rollback [stamp]|status}" >&2; exit 2 ;;
esac
