#!/usr/bin/env python3
"""Root-only disposable launcher integration tests. No real server data."""
from contextlib import closing
from pathlib import Path
import json
import os
import pwd
import signal
import sqlite3
import subprocess
import sys
import tempfile
import time

launcher = sys.argv[1]
user = pwd.getpwnam("nobody")
child_code = r'''
import os, signal, sqlite3, sys, time
from contextlib import closing
from pathlib import Path
mode = sys.argv[1]
assert os.getuid() != 0 and os.getgid() != 0
assert os.getgroups() == []
status = Path('/proc/self/status').read_text()
assert 'NoNewPrivs:\t1' in status
assert 'CapEff:\t0000000000000000' in status
db = sqlite3.connect('ConanSandbox/Saved/game_0.db')
db.execute('PRAGMA journal_mode=WAL')
db.execute('INSERT INTO probe VALUES (42)')
db.commit()
with closing(sqlite3.connect('ConanSandbox/Saved/game_backup.db')) as backup:
    db.backup(backup)
# A detached descendant must also be killed/reaped before persist.
if os.fork() == 0:
    os.setsid()
    time.sleep(120)
    os._exit(0)
Path('ready').touch()
if mode == 'crash':
    os.kill(os.getpid(), signal.SIGKILL)
if mode in ('signal', 'kill-launcher'):
    time.sleep(120)
db.close()
if mode == 'corrupt':
    Path('ConanSandbox/Saved/game_0.db').write_bytes(b'broken')
'''


def rows(path):
    with closing(sqlite3.connect(path)) as db:
        assert db.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
        return db.execute("SELECT count(*) FROM probe").fetchone()[0]


with tempfile.TemporaryDirectory(prefix="conan-launch-config-", dir="/root") as configs, \
        tempfile.TemporaryDirectory(prefix="conan-launch-test-", dir="/var/tmp") as data:
    root = Path(data)
    root.chmod(0o755)
    child = root / "child.py"
    child.write_text(child_code)
    for mode in ("normal", "crash", "signal", "kill-launcher", "corrupt"):
        install = root / mode
        saved = install / "ConanSandbox" / "Saved"
        saved.mkdir(parents=True)
        for path in (install, install / "ConanSandbox", saved):
            os.chown(path, user.pw_uid, user.pw_gid)
        dbpath = saved / "game_0.db"
        with closing(sqlite3.connect(dbpath)) as db:
            db.execute("CREATE TABLE probe(value INTEGER)")
            db.commit()
        os.chown(dbpath, user.pw_uid, user.pw_gid)
        ram = root / (mode + "-ram")
        ram.mkdir()
        subprocess.run(["mount", "-t", "tmpfs", "-o", "size=64m,nosuid,nodev", "tmpfs", str(ram)], check=True)
        process = None
        try:
            (ram / "database").mkdir()
            config = Path(configs) / (mode + ".json")
            config.write_text(json.dumps({"server_id": mode, "install_dir": str(install),
                "ram_dir": str(ram), "uid": user.pw_uid, "gid": user.pw_gid,
                "executable": "/usr/bin/python3.12", "args": [str(child), mode]}))
            config.chmod(0o600)
            command = [launcher, "--config", str(config)]
            process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            if mode in ("signal", "kill-launcher"):
                deadline = time.monotonic() + 15
                while not (install / "ready").exists():
                    if process.poll() is not None:
                        raise AssertionError(process.communicate())
                    assert time.monotonic() < deadline, "child startup timeout"
                    time.sleep(0.05)
                competitor = subprocess.run(command, capture_output=True, text=True, timeout=10)
                assert competitor.returncode != 0 and "session lock" in competitor.stderr, competitor.stderr
                process.send_signal(signal.SIGTERM if mode == "signal" else signal.SIGKILL)
            out, err = process.communicate(timeout=30)
            marker = install / "ConanSandbox" / ".conan-manager-ram-session"
            if mode == "corrupt":
                assert process.returncode != 0 and "persist failed" in err, err
                assert marker.is_dir()
                assert rows(dbpath) == 0
                assert (ram / "database" / "game_0.db").read_bytes() == b'broken'
            elif mode == "kill-launcher":
                assert marker.is_dir()
                assert rows(dbpath) == 0  # SSD baseline never overwritten.
                # Worker/descendants must die and release locks after parent death.
                time.sleep(0.5)
                retry = subprocess.run(command, capture_output=True, text=True, timeout=10)
                assert "recovery required" in retry.stderr, retry.stderr
            else:
                assert "Session persisted" in err, err
                assert (process.returncode == 0) == (mode in ("normal", "signal")), err
                assert not marker.exists()
                assert rows(dbpath) == 1
                assert dbpath.stat().st_uid == user.pw_uid
                assert rows(saved / "game_backup.db") == 1
                assert not (ram / "database" / "game_backup.db").exists()
                assert list((install / "ConanSandbox").glob(".conan-manager-ram-session.recovered-*"))
                assert not list((ram / "database").iterdir())
                if mode == "normal":
                    for expected in (2, 3):
                        retry = subprocess.run(command, capture_output=True, text=True, timeout=30)
                        assert retry.returncode == 0, retry.stderr
                        assert rows(dbpath) == expected
                        assert rows(saved / "game_backup.db") == expected
                        assert not marker.exists()
                        assert not list((ram / "database").iterdir())
            print("PASS:", mode)
        finally:
            if process is not None and process.poll() is None:
                process.kill()
                process.communicate(timeout=10)
            subprocess.run(["umount", str(ram)], check=True)
