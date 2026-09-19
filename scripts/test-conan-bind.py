#!/usr/bin/env python3
"""Disposable Conan UE5 bind mount experiment. Run inside private mount namespace.

Leaves reports and a copy of the test save in /var/tmp; never modifies the
original Saved. Manager must be stopped before running.
"""
import json
from contextlib import closing
import os
from pathlib import Path
import pwd
import shutil
import signal
import sqlite3
import subprocess
import tempfile
import time

GAME = Path('/var/lib/conan-manager/servers/ue5-test/game')
EXE = GAME / 'ConanSandbox/Binaries/Linux/ConanSandboxServer-Linux-Shipping'
SAVED = GAME / 'ConanSandbox/Saved'


def main():
    assert subprocess.run(['systemctl', 'is-active', '--quiet', 'conan-manager']).returncode != 0
    assert EXE.is_file() and SAVED.is_dir() and not SAVED.is_symlink()
    # Refuse to touch the installation if any process has its executable open.
    for item in Path('/proc').glob('[0-9]*/exe'):
        try:
            if item.resolve() == EXE:
                raise RuntimeError('Conan is already running')
        except (FileNotFoundError, PermissionError):
            pass
    root = Path(tempfile.mkdtemp(prefix='conan-bind-', dir='/var/tmp'))
    print('REPORT_DIR=' + str(root), flush=True)
    ram, saved = root / 'ram', root / 'Saved'
    ram.mkdir()
    saved.mkdir()
    account = pwd.getpwnam('conan-manager')
    mounts, process = [], None
    report = {'root': str(root), 'phases': []}

    def record(value):
        report['phases'].append(value)
        (root / 'report.json').write_text(json.dumps(report, indent=2))
        print(json.dumps(value), flush=True)

    def mount(*args):
        subprocess.run(['mount', *map(str, args)], check=True)
        mounts.append(Path(args[-1]))

    def launch(label):
        output = open(root / (label + '.log'), 'wb')
        try:
            child = subprocess.Popen(
                [str(EXE), 'ConanSandbox', '-log', '-Port=38777', '-QueryPort=38015',
                 '-MULTIHOME=127.0.0.1', '-MULTIHOMEHTTP=127.0.0.1'], cwd=GAME,
                stdout=output, stderr=subprocess.STDOUT, user=account.pw_uid,
                group=account.pw_gid, extra_groups=[], start_new_session=True,
                env={**os.environ, 'HOME': account.pw_dir})
        finally:
            output.close()
        return child

    def stop(child, force=False):
        begin = time.monotonic()
        forced = force
        if child.poll() is None:
            os.killpg(child.pid, signal.SIGKILL if force else signal.SIGTERM)
            try:
                child.wait(timeout=180)
            except subprocess.TimeoutExpired:
                forced = True
                os.killpg(child.pid, signal.SIGKILL)
                child.wait(timeout=20)
        return {'exit': child.returncode, 'forced': forced, 'seconds': round(time.monotonic()-begin, 2)}

    def backup_check():
        found = []
        for path in saved.glob('*backup*.db'):
            try:
                # Check a copy: never checkpoint a live game backup in place.
                copy = root / 'backup-check.db'
                shutil.copyfile(path, copy)
                with closing(sqlite3.connect(copy)) as db:
                    integrity = db.execute('PRAGMA integrity_check').fetchone()[0]
                found.append({'name': path.name, 'size': path.stat().st_size, 'integrity': integrity})
            except (OSError, sqlite3.Error) as error:
                found.append({'name': path.name, 'error': str(error)})
        return found

    try:
        # Snapshot the stopped source database through SQLite, incorporating WAL.
        source = sqlite3.connect((SAVED / 'game_0.db').as_uri() + '?mode=ro', uri=True)
        target = sqlite3.connect(saved / 'game_0.db')
        try:
            source.backup(target)
        finally:
            target.close()
            source.close()
        if (SAVED / 'Config').exists():
            shutil.copytree(SAVED / 'Config', saved / 'Config')
        mount('-t', 'tmpfs', '-o', 'size=512m,nosuid,nodev', 'tmpfs', ram)
        shutil.copyfile(saved / 'game_0.db', ram / 'game_0.db')
        for suffix in ('', '-wal', '-shm'):
            name = 'game_0.db' + suffix
            (ram / name).touch(exist_ok=True)
            (saved / name).touch(exist_ok=True)
        for path in [root, *root.rglob('*')]:
            os.chown(path, account.pw_uid, account.pw_gid)
        mount('--bind', saved, SAVED)
        for suffix in ('', '-wal', '-shm'):
            mount('--bind', ram / ('game_0.db' + suffix), SAVED / ('game_0.db' + suffix))
        if os.environ.get('CONAN_BIND_CLEANUP_ONLY') == '1':
            process = launch('cleanup-check')
            time.sleep(75)
            if process.poll() is not None:
                raise RuntimeError('Conan exited in cleanup check')
            record({'cleanup_graceful_stop': stop(process)})
            process = None
            with closing(sqlite3.connect(SAVED / 'game_0.db')) as db:
                record({'integrity': db.execute('PRAGMA integrity_check').fetchone()[0]})
            return
        process = launch('initial')
        changes, last = [], None
        deadline = time.monotonic() + 720
        # Require two distinct backup observations separated by at least four
        # minutes to avoid counting startup/rotation as two scheduled backups.
        first = None
        while time.monotonic() < deadline:
            time.sleep(10)
            if process.poll() is not None:
                raise RuntimeError('Conan exited during backup observation: ' + str(process.returncode))
            current = [(p.name, p.stat().st_size, p.stat().st_mtime_ns) for p in saved.glob('*backup*.db')]
            if current and current != last:
                now = time.monotonic()
                changes.append(current)
                record({'event': 'backup', 'ssd_backups': backup_check(),
                        'ram_files': sorted(p.name for p in ram.iterdir())})
                if first is not None and now-first >= 240:
                    break
                if first is None:
                    first = now
            last = current
        else:
            raise RuntimeError('No second scheduled backup within 12 minutes')
        record({'graceful_stop': stop(process)})
        process = None
        # Validate actual mounted DB after shutdown, including its WAL.
        with closing(sqlite3.connect(SAVED / 'game_0.db')) as db:
            record({'integrity_after_stop': db.execute('PRAGMA integrity_check').fetchone()[0]})
        process = launch('before-crash')
        time.sleep(75)
        if process.poll() is not None:
            raise RuntimeError('Restart failed')
        record({'crash': stop(process, force=True)})
        process = launch('recovery')
        time.sleep(90)
        record({'recovery_running': process.poll() is None})
        record({'recovery_stop': stop(process)})
        process = None
        with closing(sqlite3.connect(SAVED / 'game_0.db')) as db:
            record({'integrity_after_recovery': db.execute('PRAGMA integrity_check').fetchone()[0]})
        record({'final_backups': backup_check(), 'ram_files': sorted(p.name for p in ram.iterdir())})
    except Exception as error:
        record({'error': str(error)})
        raise
    finally:
        if process is not None and process.poll() is None:
            record({'cleanup_stop': stop(process)})
        # Save RAM state as evidence before unmounting it.
        for path in ram.glob('*'):
            if path.is_file():
                shutil.copyfile(path, root / ('final-' + path.name))
        for path in reversed(mounts):
            subprocess.run(['umount', str(path)], check=True)
        record({'cleanup': 'mounts removed; original Saved restored'})


if __name__ == '__main__':
    main()
