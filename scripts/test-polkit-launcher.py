#!/usr/bin/env python3
"""Disposable polkit check for the explicit launcher unit allowlist."""
import json
import os
import sqlite3
import subprocess
import tempfile
import time
from pathlib import Path

ID = "polkit-check"
UNIT = f"conan-launch@{ID}.service"
FOREIGN_ID = "polkit-foreign"
FOREIGN_UNIT = f"conan-launch@{FOREIGN_ID}.service"
CONFIG_DIR = Path("/etc/conan-manager/launch")
RULE = Path("/etc/polkit-1/rules.d/50-conan-launch-test.rules")
UNIT_PATH = Path("/etc/systemd/system") / UNIT


def run(*args, user=None, check=True):
    command = list(args)
    if user:
        command = ["runuser", "-u", user, "--"] + command
    return subprocess.run(command, text=True, capture_output=True, check=check)


with tempfile.TemporaryDirectory(prefix="conan-polkit-", dir="/var/tmp") as temp:
    for path in (UNIT_PATH, RULE, CONFIG_DIR / f"{ID}.json", CONFIG_DIR / f"{FOREIGN_ID}.json"):
        if path.exists():
            raise SystemExit(f"refusing to overwrite existing path: {path}")
    root = Path(temp)
    root.chmod(0o755)
    install = root / "game"
    saved = install / "ConanSandbox" / "Saved"
    ram = root / "ram"
    saved.mkdir(parents=True)
    ram.mkdir()
    run("mount", "-t", "tmpfs", "-o", "size=64m,nosuid,nodev", "tmpfs", str(ram))
    try:
        (ram / "database").mkdir()
        for path in (install, install / "ConanSandbox", saved):
            os.chown(path, 65534, 65534)
        executable = install / "ConanSandboxServer-Linux-Shipping"
        executable.write_text("#!/bin/sh\ntouch ready\nsleep 120\n")
        executable.chmod(0o755)
        os.chown(executable, 65534, 65534)
        with sqlite3.connect(saved / "game_0.db") as db:
            db.execute("CREATE TABLE probe(value INTEGER)")
            db.commit()
        os.chown(saved / "game_0.db", 65534, 65534)
        config = CONFIG_DIR / f"{ID}.json"
        config.write_text(json.dumps({"server_id": ID, "install_dir": str(install),
            "ram_dir": str(ram), "executable": str(executable),
            "args": [], "uid": 65534, "gid": 65534}))
        config.chmod(0o644)
        UNIT_PATH.write_text("[Service]\nType=exec\nExecStart=/opt/conan-manager/conan-launch --config " + str(config) + "\nKillMode=mixed\nTimeoutStopSec=810\n")
        RULE.write_text('polkit.addRule(function(action, subject) { if (action.id === "org.freedesktop.systemd1.manage-units" && subject.user === "conan-manager" && action.lookup("unit") === "' + UNIT + '" && ["start", "stop"].indexOf(action.lookup("verb")) !== -1) return polkit.Result.YES; });\n')
        foreign_unit_path = Path("/run/systemd/system") / FOREIGN_UNIT
        if foreign_unit_path.exists():
            raise SystemExit(f"refusing to overwrite existing path: {foreign_unit_path}")
        foreign_unit_path.write_text("[Service]\nType=oneshot\nExecStart=/bin/true\n")
        run("systemctl", "daemon-reload")
        time.sleep(1)
        denied = run("systemctl", "start", FOREIGN_UNIT, user="conan-manager", check=False)
        if denied.returncode == 0:
            raise AssertionError("polkit allowed a foreign launcher unit")
        denied = run("systemctl", "restart", UNIT, user="conan-manager", check=False)
        if denied.returncode == 0:
            raise AssertionError("polkit allowed restart instead of start/stop")
        result = run("systemctl", "start", UNIT, user="conan-manager", check=False)
        if result.returncode:
            raise AssertionError(result.stderr or result.stdout)
        deadline = time.monotonic() + 15
        while not (install / "ready").exists():
            if time.monotonic() > deadline:
                raise AssertionError("launcher did not start")
            time.sleep(.1)
        result = run("systemctl", "stop", "--no-block", UNIT, user="conan-manager", check=False)
        if result.returncode:
            raise AssertionError(result.stderr or result.stdout)
        run("systemctl", "stop", UNIT, check=False)
        run("systemctl", "reset-failed", UNIT, check=False)
        run("systemctl", "reset-failed", FOREIGN_UNIT, check=False)
        print("polkit launcher allowlist and denial checks: PASS")
    finally:
        run("systemctl", "stop", UNIT, check=False)
        run("systemctl", "daemon-reload", check=False)
        UNIT_PATH.unlink(missing_ok=True)
        (Path("/run/systemd/system") / FOREIGN_UNIT).unlink(missing_ok=True)
        RULE.unlink(missing_ok=True)
        (CONFIG_DIR / f"{ID}.json").unlink(missing_ok=True)
        run("systemctl", "daemon-reload", check=False)
        run("umount", str(ram), check=False)
