#!/usr/bin/env python3
"""Root, private mount namespace; disposable data only."""
from contextlib import closing
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile

helper = sys.argv[1]
names = ("game_0.db", "game_0.db-wal", "game_0.db-shm")


def call(action, saved, ram, success=True):
    result = subprocess.run([helper, "--action", action, "--saved", str(saved),
                             "--ram", str(ram)], capture_output=True, text=True)
    assert (result.returncode == 0) == success, result.stderr


with tempfile.TemporaryDirectory(prefix="conan-helper-", dir="/var/tmp") as directory:
    root = Path(directory)
    saved, ram = root / "saved", root / "ram"
    saved.mkdir()
    ram.mkdir()
    call("mount", saved, ram, False)  # RAM must be tmpfs.
    subprocess.run(["mount", "-t", "tmpfs", "-o", "size=64m,nosuid,nodev",
                    "tmpfs", str(ram)], check=True)
    try:
        for name in names:
            (saved / name).touch()
            (ram / name).touch()
        # Validation must complete before any mount changes.
        (saved / names[-1]).unlink()
        (saved / names[-1]).symlink_to(ram / names[-1])
        call("mount", saved, ram, False)
        assert (saved / names[0]).stat().st_ino != (ram / names[0]).stat().st_ino or \
            (saved / names[0]).stat().st_dev != (ram / names[0]).stat().st_dev
        (saved / names[-1]).unlink()
        (saved / names[-1]).touch()
        alias = root / "alias"
        alias.symlink_to(saved, target_is_directory=True)
        call("mount", alias, ram, False)
        call("mount", saved, ram)
        call("mount", saved, ram, False)  # No stacking.
        for cycle in range(3):
            with closing(sqlite3.connect(saved / names[0])) as db:
                db.execute("PRAGMA journal_mode=WAL")
                db.execute("CREATE TABLE IF NOT EXISTS probe(value INTEGER)")
                db.execute("INSERT INTO probe VALUES (?)", (cycle,))
                db.commit()
                assert db.execute("SELECT count(*) FROM probe").fetchone()[0] == cycle + 1
                with closing(sqlite3.connect(saved / "game_backup.db")) as backup:
                    db.backup(backup)
                    assert backup.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
                assert db.execute("PRAGMA wal_checkpoint(TRUNCATE)").fetchone()[0] == 0
        assert not (ram / "game_backup.db").exists()
        assert (saved / "game_backup.db").stat().st_dev == saved.stat().st_dev
        # Wrong source must not detach anything.
        wrong = ram / "wrong"
        wrong.mkdir()
        for name in names:
            (wrong / name).touch()
        call("unmount", saved, wrong, False)
        call("unmount", saved, ram)
        assert (saved / names[0]).stat().st_size == 0
        with closing(sqlite3.connect(ram / names[0])) as db:
            assert db.execute("SELECT count(*) FROM probe").fetchone()[0] == 3
            assert db.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
        call("unmount", saved, ram, False)
        print("PASS: validation, no stacking, WAL/reopen, SSD backup, source identity, unmount")
    finally:
        for name in reversed(names):
            subprocess.run(["umount", str(saved / name)], capture_output=True)
        subprocess.run(["umount", str(ram)], check=True)
