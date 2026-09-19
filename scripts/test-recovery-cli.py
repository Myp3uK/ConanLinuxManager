#!/usr/bin/env python3
"""Disposable offline conan-recover drill; never uses a configured server."""
import json
import os
import sqlite3
import subprocess
import tempfile
from pathlib import Path


RECOVER = os.environ.get("CONAN_RECOVER_BIN", "/root/conan-recover-test")


def make_db(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    with sqlite3.connect(path) as db:
        db.execute("CREATE TABLE probe(value TEXT)")
        db.execute("INSERT INTO probe VALUES (?)", (value,))
        db.commit()


def read_value(path):
    with sqlite3.connect(path) as db:
        return db.execute("SELECT value FROM probe").fetchone()[0]


if os.geteuid() != 0:
    raise SystemExit("root required")

with tempfile.TemporaryDirectory(prefix="conan-recovery-cli-", dir="/var/tmp") as temp:
    root = Path(temp)
    install = root / "game"
    saved = install / "ConanSandbox" / "Saved"
    ram = root / "ram"
    database = ram / "database" / "game_0.db"
    session = install / "ConanSandbox" / ".conan-manager-ram-session"
    baseline = session / "baseline" / "game_0.db"

    make_db(saved / "game_0.db", "saved")
    make_db(database, "ram")
    manifest = {
        "version": 1,
        "server_id": "cli-recovery-check",
        "ram_database": str(ram / "database"),
        "baseline_files": ["game_0.db"],
    }
    session.mkdir()
    baseline.parent.mkdir()
    make_db(baseline, "baseline")
    (session / "manifest.json").write_text(json.dumps(manifest))

    result = subprocess.run(
        [RECOVER, "--server", "cli-recovery-check", "--install-dir", str(install),
         "--ram-dir", str(ram), "--source", "ram"],
        text=True, capture_output=True,
    )
    if result.returncode != 0:
        raise AssertionError(result.stderr)
    assert read_value(saved / "game_0.db") == "ram"
    assert not session.exists()
    assert list(install.glob("ConanSandbox/.conan-manager-ram-session.recovered-*"))

    # A second independent fixture proves explicit baseline selection.
    install2 = root / "game-baseline"
    saved2 = install2 / "ConanSandbox" / "Saved"
    ram2 = root / "ram-baseline"
    session2 = install2 / "ConanSandbox" / ".conan-manager-ram-session"
    make_db(saved2 / "game_0.db", "saved")
    session2.mkdir(parents=True)
    (session2 / "baseline").mkdir()
    make_db(session2 / "baseline" / "game_0.db", "baseline")
    (session2 / "manifest.json").write_text(json.dumps({
        "version": 1,
        "server_id": "cli-recovery-baseline",
        "ram_database": str(ram2 / "database"),
        "baseline_files": ["game_0.db"],
    }))
    result = subprocess.run(
        [RECOVER, "--server", "cli-recovery-baseline", "--install-dir", str(install2),
         "--ram-dir", str(ram2), "--source", "baseline"],
        text=True, capture_output=True,
    )
    if result.returncode != 0:
        raise AssertionError(result.stderr)
    assert read_value(saved2 / "game_0.db") == "baseline"
    assert not session2.exists()
    print("CLI recovery ram/baseline/archive: PASS")
