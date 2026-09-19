#!/usr/bin/env python3
"""Disposable API/systemd launcher integration test; root-only Linux host test."""
import json
import os
import signal
import sqlite3
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path

ID = "api-launcher-check"
UNIT = f"conan-launch@{ID}.service"
PORT = 18080
CONFIG_DIR = Path("/etc/conan-manager/launch")
UNIT_PATH = Path("/run/systemd/system") / UNIT


def run(*args, check=True):
    return subprocess.run(args, text=True, capture_output=True, check=check)


def request(method, path, value=None):
    data = None if value is None else json.dumps(value).encode()
    req = urllib.request.Request(f"http://127.0.0.1:{PORT}{path}", data=data, method=method,
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=10) as response:
        return response.status, json.load(response) if response.status != 204 else None


def request_status(method, path, value=None):
    try:
        return request(method, path, value)
    except urllib.error.HTTPError as error:
        body = json.load(error)
        return error.code, body


def concurrent_request(method, path, result, key):
    try:
        result[key] = request_status(method, path)
    except Exception as error:
        result[key] = error


if os.geteuid() != 0:
    raise SystemExit("root required")
if UNIT_PATH.exists() or (CONFIG_DIR / f"{ID}.json").exists():
    raise SystemExit("refusing to overwrite existing disposable paths")

manager = Path(os.environ.get("CONAN_MANAGER_BIN", "/opt/conan-manager/conan-manager"))
launcher = Path(os.environ.get("CONAN_LAUNCH_BIN", "/opt/conan-manager/conan-launch"))
with tempfile.TemporaryDirectory(prefix="conan-api-test-", dir="/var/tmp") as temp:
    root = Path(temp)
    root.chmod(0o755)
    install = root / "game"
    saved = install / "ConanSandbox" / "Saved"
    ram = root / "ram"
    steam = root / "steamcmd"
    saved.mkdir(parents=True)
    ram.mkdir()
    steam.mkdir()
    run("mount", "-t", "tmpfs", "-o", "size=64m,nosuid,nodev", "tmpfs", str(ram))
    proc = None
    try:
        (ram / "database").mkdir()
        executable = install / "ConanSandbox/Binaries/Linux/ConanSandboxServer-Linux-Shipping"
        executable.parent.mkdir(parents=True)
        executable.write_text("#!/usr/bin/python3\nimport sqlite3,time\nc=sqlite3.connect('ConanSandbox/Saved/game_0.db'); c.execute('PRAGMA journal_mode=WAL'); c.execute('CREATE TABLE IF NOT EXISTS probe(v)'); c.execute('INSERT INTO probe VALUES (1)'); c.commit(); open('ready','w').close(); time.sleep(120)\n")
        executable.chmod(0o755)
        for path in (install, install / "ConanSandbox", saved):
            os.chown(path, 65534, 65534)
        os.chown(executable, 65534, 65534)
        with sqlite3.connect(saved / "game_0.db") as db:
            db.execute("CREATE TABLE probe(v)")
            db.commit()
        os.chown(saved / "game_0.db", 65534, 65534)
        cfg = {"server_id": ID, "install_dir": str(install), "ram_dir": str(ram), "executable": str(executable), "args": ["ConanSandbox", "-log"], "uid": 65534, "gid": 65534}
        config = CONFIG_DIR / f"{ID}.json"
        config.write_text(json.dumps(cfg))
        config.chmod(0o644)
        UNIT_PATH.write_text(f"[Service]\nType=exec\nExecStart={launcher} --config {config}\nKillMode=mixed\nTimeoutStopSec=810\n")
        run("systemctl", "daemon-reload")
        env = os.environ.copy()
        env.update({"CONAN_MANAGER_LISTEN": f"127.0.0.1:{PORT}", "CONAN_MANAGER_DATABASE": str(root / "manager.db"), "CONAN_MANAGER_LAUNCHER_IDS": ID, "CONAN_MANAGER_QUERY_INTERVAL": "1h"})
        def start_manager():
            child = subprocess.Popen([str(manager)], env=env, stdout=subprocess.PIPE,
                                     stderr=subprocess.PIPE, text=True)
            deadline = time.monotonic() + 15
            while True:
                try:
                    if request("GET", "/healthz")[1]["status"] == "ok":
                        return child
                except Exception:
                    pass
                if time.monotonic() > deadline:
                    output = ""
                    if child.poll() is not None:
                        output = f" exit={child.returncode} stdout={child.stdout.read()} stderr={child.stderr.read()}"
                    raise AssertionError("manager did not become healthy" + output)
                time.sleep(.1)

        proc = start_manager()
        value = {"id": ID, "name": ID, "engine": "ue5", "runtime": "native", "install_dir": str(install), "steamcmd_dir": str(steam), "branch": "", "launch_args": "-log", "game_port": 37777, "query_port": 37015, "ramdisk_enabled": True, "ramdisk_path": str(ram), "ramdisk_size_mb": 128, "sync_interval_seconds": 0}
        assert request("POST", "/api/v1/servers", value)[0] == 201
        for cycle in range(2):
            assert request("POST", f"/api/v1/servers/{ID}/start")[0] == 202
            while not (install / "ready").exists():
                state = run("systemctl", "is-active", UNIT, check=False)
                if state.returncode != 0:
                    raise AssertionError(run("journalctl", "-u", UNIT, "-n", "30", "--no-pager", check=False).stdout)
                time.sleep(.1)
            if cycle == 0:
                proc.kill()
                proc.wait(timeout=10)
                proc = start_manager()
                adopted = request("GET", f"/api/v1/servers/{ID}")[1]
                assert adopted["lifecycle"]["state"] == "running", adopted
            assert request("POST", f"/api/v1/servers/{ID}/stop")[0] == 202
            deadline = time.monotonic() + 15
            while True:
                detail = request("GET", f"/api/v1/servers/{ID}")[1]
                if detail["lifecycle"]["state"] == "idle":
                    break
                if time.monotonic() > deadline:
                    raise AssertionError(detail)
                time.sleep(.2)
            (install / "ready").unlink()
        marker = install / "ConanSandbox" / ".conan-manager-ram-session"
        marker.mkdir()
        status, detail = request("GET", f"/api/v1/servers/{ID}")
        assert status == 200 and detail["lifecycle"]["state"] == "recovery_required", detail
        status, detail = request_status("POST", f"/api/v1/servers/{ID}/start")
        assert status == 409 and "recovery required" in detail["error"], detail
        status, detail = request_status("DELETE", f"/api/v1/servers/{ID}")
        assert status == 409 and "recovery required" in detail["error"], detail
        marker.rmdir()
        status, detail = request("GET", f"/api/v1/servers/{ID}")
        assert status == 200 and detail["lifecycle"]["state"] == "idle", detail
        results = {}
        threads = [threading.Thread(target=concurrent_request,
                                    args=("POST", f"/api/v1/servers/{ID}/start", results, index))
                   for index in ("first", "second")]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join(timeout=10)
        assert all(isinstance(results[index], tuple) for index in ("first", "second")), results
        assert sorted(results[index][0] for index in ("first", "second")) == [202, 409], results
        while not (install / "ready").exists():
            if run("systemctl", "is-active", UNIT, check=False).returncode != 0:
                raise AssertionError(run("journalctl", "-u", UNIT, "-n", "30", "--no-pager", check=False).stdout)
            time.sleep(.1)
        assert request("POST", f"/api/v1/servers/{ID}/stop")[0] == 202
        deadline = time.monotonic() + 15
        while request("GET", f"/api/v1/servers/{ID}")[1]["lifecycle"]["state"] != "idle":
            if time.monotonic() > deadline:
                raise AssertionError("concurrent start did not stop")
            time.sleep(.2)
        print("API launcher start/stop/restart/adoption/recovery: PASS")
    finally:
        if proc is not None and proc.poll() is None:
            proc.send_signal(signal.SIGTERM)
            proc.wait(timeout=20)
        run("systemctl", "stop", UNIT, check=False)
        UNIT_PATH.unlink(missing_ok=True)
        (CONFIG_DIR / f"{ID}.json").unlink(missing_ok=True)
        run("systemctl", "daemon-reload", check=False)
        run("umount", str(ram), check=False)
