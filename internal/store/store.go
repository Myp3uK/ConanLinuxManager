package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("sqlite pragma: %w", err)
		}
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping() error { return s.db.Ping() }

func (s *Store) DB() *sql.DB { return s.db }

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS servers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    engine TEXT NOT NULL CHECK (engine IN ('ue5', 'ue4')),
    runtime TEXT NOT NULL CHECK (runtime IN ('native', 'wine', 'proton')),
    install_dir TEXT NOT NULL,
    steamcmd_dir TEXT NOT NULL,
    branch TEXT NOT NULL DEFAULT 'public',
    launch_args TEXT NOT NULL DEFAULT '',
    game_port INTEGER NOT NULL,
    query_port INTEGER NOT NULL,
    rcon_port INTEGER,
    rcon_secret BLOB,
    ramdisk_enabled INTEGER NOT NULL DEFAULT 1,
    ramdisk_path TEXT NOT NULL,
    ramdisk_size_mb INTEGER NOT NULL DEFAULT 2048,
    sync_interval_seconds INTEGER NOT NULL DEFAULT 900,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS backup_targets (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('local', 'sftp', 'rsync', 's3')),
    address TEXT,
    port INTEGER,
    username TEXT,
    password_encrypted BLOB,
    ssh_private_key TEXT,
    remote_path TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS backup_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    source_path TEXT NOT NULL,
    destination_path TEXT NOT NULL,
    reason TEXT NOT NULL,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_backup_events_server_created
    ON backup_events(server_id, created_at DESC);

CREATE TABLE IF NOT EXISTS schedules (
    server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    action TEXT NOT NULL CHECK (action IN ('start', 'stop', 'restart')),
    enabled INTEGER NOT NULL DEFAULT 1,
    mode TEXT NOT NULL DEFAULT 'interval' CHECK (mode IN ('interval', 'daily', 'cron')),
    interval_seconds INTEGER NOT NULL CHECK (interval_seconds >= 60),
    daily_time TEXT,
    cron_expression TEXT,
    timezone TEXT NOT NULL DEFAULT 'UTC',
    next_run TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS scheduler_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    action TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    error TEXT,
    started_at TEXT NOT NULL,
    finished_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_scheduler_runs_server_started
    ON scheduler_runs(server_id, started_at DESC);
`)
	if err != nil {
		return fmt.Errorf("database migration: %w", err)
	}
	for _, column := range []struct{ name, definition string }{
		{"mode", "TEXT NOT NULL DEFAULT 'interval'"},
		{"daily_time", "TEXT"},
		{"cron_expression", "TEXT"},
		{"timezone", "TEXT NOT NULL DEFAULT 'UTC'"},
	} {
		var present string
		err := db.QueryRow(`SELECT name FROM pragma_table_info('schedules') WHERE name=?`, column.name).Scan(&present)
		if err == sql.ErrNoRows {
			if _, err := db.Exec(`ALTER TABLE schedules ADD COLUMN ` + column.name + ` ` + column.definition); err != nil {
				return fmt.Errorf("add schedules.%s: %w", column.name, err)
			}
		} else if err != nil {
			return fmt.Errorf("inspect schedules.%s: %w", column.name, err)
		}
	}
	return migrateScheduleModes(db)
}

// Rebuild once: SQLite cannot extend the CHECK constraint on an existing column.
// The transaction keeps both the original table and its rows intact on failure.
func migrateScheduleModes(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS manager_migrations (name TEXT PRIMARY KEY)`); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM manager_migrations WHERE name='schedule-cron-modes-v1'`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return tx.Commit()
	}
	_, err = tx.Exec(`
CREATE TABLE schedules_cron_upgrade (
 server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
 action TEXT NOT NULL CHECK(action IN ('start','stop','restart')),
 enabled INTEGER NOT NULL DEFAULT 1,
 mode TEXT NOT NULL DEFAULT 'interval' CHECK(mode IN ('interval','daily','cron')),
 interval_seconds INTEGER NOT NULL CHECK(interval_seconds>=60),
 daily_time TEXT,
 cron_expression TEXT,
 timezone TEXT NOT NULL DEFAULT 'UTC',
 next_run TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
INSERT INTO schedules_cron_upgrade SELECT server_id,action,enabled,mode,interval_seconds,daily_time,cron_expression,timezone,next_run,updated_at FROM schedules;
DROP TABLE schedules;
ALTER TABLE schedules_cron_upgrade RENAME TO schedules;
INSERT INTO manager_migrations VALUES ('schedule-cron-modes-v1');
`)
	if err != nil {
		return fmt.Errorf("upgrade schedule modes: %w", err)
	}
	return tx.Commit()
}
