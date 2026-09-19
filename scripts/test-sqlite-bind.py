#!/usr/bin/env python3
"""Run as root inside `unshare --mount --propagation private` on Linux.

Uses disposable directories only. Does not start Conan or touch manager data.
"""
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import tempfile


def command(*args):
    subprocess.run(args, check=True)


def main():
    with tempfile.TemporaryDirectory(prefix="conan-bind-probe-") as directory:
        root = Path(directory)
        ram, saved = root / "ram", root / "saved"
        ram.mkdir()
        saved.mkdir()
        mounts = []
        connection = None
        results = []
        try:
            command("mount", "-t", "tmpfs", "-o", "size=64m,nosuid,nodev", "tmpfs", str(ram))
            mounts.append(ram)
            for suffix in ("", "-wal", "-shm"):
                source = ram / ("game.db" + suffix)
                target = saved / source.name
                source.touch()
                target.touch()
                command("mount", "--bind", str(source), str(target))
                mounts.append(target)
            for cycle in range(3):
                connection = sqlite3.connect(saved / "game.db")
                connection.execute("PRAGMA journal_mode=WAL")
                connection.execute("CREATE TABLE IF NOT EXISTS probe (value INTEGER)")
                connection.execute("INSERT INTO probe VALUES (?)", (cycle,))
                connection.commit()
                checkpoint = connection.execute("PRAGMA wal_checkpoint(TRUNCATE)").fetchone()
                count = connection.execute("SELECT count(*) FROM probe").fetchone()[0]
                assert count == cycle + 1
                backup = sqlite3.connect(saved / "game_backup.db")
                try:
                    connection.backup(backup)
                    assert backup.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
                finally:
                    backup.close()
                connection.close()
                connection = None
                results.append({"cycle": cycle, "rows": count, "checkpoint": checkpoint})
            # SQLite implementations may tolerate EBUSY on close, but Conan's
            # maintenance/recovery can still require unlink or rename.
            for suffix in ("-wal", "-shm"):
                try:
                    os.unlink(saved / ("game.db" + suffix))
                    result = "removed"
                except OSError as error:
                    result = {"errno": error.errno, "message": error.strerror}
                results.append({"unlink": suffix, "result": result})
            print(json.dumps({"sqlite": sqlite3.sqlite_version, "results": results}, indent=2))
        finally:
            if connection is not None:
                connection.close()
            for target in reversed(mounts):
                command("umount", str(target))


if __name__ == "__main__":
    main()
