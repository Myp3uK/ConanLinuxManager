package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	// The pragmas go into the DSN so that every pooled connection gets them.
	// Executed once with db.Exec they applied to a single connection only:
	// the others failed at once with SQLITE_BUSY under concurrent writes and
	// ignored foreign keys, so ON DELETE CASCADE did not run on them.
	// _txlock=immediate takes the write lock at BEGIN; in WAL mode a deferred
	// transaction that reads and then writes gets SQLITE_BUSY, which
	// busy_timeout does not retry.
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	// The database holds password hashes, sessions and sealed secrets; only
	// the service user may read it, whatever the umask was.
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		_ = os.Chmod(p, 0o600)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping() error { return s.db.Ping() }

func (s *Store) DB() *sql.DB { return s.db }

// baseline is the current schema. Existing installations are brought up to it by
// the migration steps below, so this DDL only ever creates missing objects.
const baseline = `
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
    periodic_backup_interval_seconds INTEGER NOT NULL DEFAULT 0,
    restart_on_crash INTEGER NOT NULL DEFAULT 1,
    desired_state TEXT NOT NULL DEFAULT 'stopped' CHECK (desired_state IN ('running', 'stopped')),
    recovery_reason TEXT,
    hooks TEXT NOT NULL DEFAULT '{}',
    auto_update INTEGER NOT NULL DEFAULT 0,
    warnings TEXT NOT NULL DEFAULT '{}',
    options TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    time TEXT NOT NULL,
    type TEXT NOT NULL,
    severity TEXT NOT NULL,
    server_id TEXT,
    server_name TEXT,
    message TEXT NOT NULL,
    details TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_events_server ON events(server_id, id DESC);

CREATE TABLE IF NOT EXISTS notification_channels (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('discord', 'telegram', 'http')),
    secret TEXT NOT NULL,
    settings TEXT NOT NULL DEFAULT '{}',
    server_id TEXT REFERENCES servers(id) ON DELETE CASCADE,
    min_severity TEXT NOT NULL DEFAULT 'info' CHECK (min_severity IN ('info', 'warning', 'error')),
    event_types TEXT NOT NULL DEFAULT '[]',
    language TEXT NOT NULL DEFAULT 'ru' CHECK (language IN ('ru', 'en')),
    mention TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1,
    last_sent_at TEXT,
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS config_versions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    file TEXT NOT NULL,
    created_at TEXT NOT NULL,
    reason TEXT NOT NULL,
    content TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_config_versions ON config_versions(server_id, file, id DESC);

CREATE TABLE IF NOT EXISTS server_mods (
    server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    workshop_id TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1,
    title TEXT NOT NULL DEFAULT '',
    paks TEXT NOT NULL DEFAULT '[]',
    size_bytes INTEGER NOT NULL DEFAULT 0,
    installed_updated TEXT,
    latest_updated TEXT,
    installed_at TEXT,
    checked_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    added_at TEXT NOT NULL,
    PRIMARY KEY (server_id, workshop_id)
);

CREATE TABLE IF NOT EXISTS hook_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    event TEXT NOT NULL,
    script TEXT NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT NOT NULL,
    exit_code INTEGER NOT NULL,
    timed_out INTEGER NOT NULL DEFAULT 0,
    output TEXT NOT NULL DEFAULT '',
    error TEXT
);
CREATE INDEX IF NOT EXISTS idx_hook_runs_server ON hook_runs(server_id, id DESC);

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
    sha256 TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_backup_events_server_created
    ON backup_events(server_id, created_at DESC);

CREATE TABLE IF NOT EXISTS schedules (
    server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    action TEXT NOT NULL CHECK (action IN ('start', 'stop', 'restart')),
    enabled INTEGER NOT NULL DEFAULT 1,
    mode TEXT NOT NULL DEFAULT 'interval' CHECK (mode IN ('interval', 'daily', 'cron')),
    interval_seconds INTEGER NOT NULL DEFAULT 0
        CHECK (mode <> 'interval' OR interval_seconds >= 60),
    daily_time TEXT,
    cron_expression TEXT,
    timezone TEXT NOT NULL DEFAULT 'UTC',
    failure_count INTEGER NOT NULL DEFAULT 0,
    next_run TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS scheduler_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    action TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'interrupted')),
    error TEXT,
    started_at TEXT NOT NULL,
    finished_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_scheduler_runs_server_started
    ON scheduler_runs(server_id, started_at DESC);

CREATE TABLE IF NOT EXISTS manager_migrations (name TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS panel_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS pending_restarts (
    server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    reason TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    due_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS server_ready (
    server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    started_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS admins (
    username TEXT PRIMARY KEY,
    password_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    password_changed_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL,
    csrf TEXT NOT NULL,
    mark TEXT NOT NULL,
    ip TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    last_seen TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    time TEXT NOT NULL,
    username TEXT NOT NULL DEFAULT '',
    ip TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    target TEXT NOT NULL DEFAULT '',
    status INTEGER NOT NULL DEFAULT 0,
    details TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_audit_log_target ON audit_log(target, id DESC);
`

// step is one schema upgrade. needed inspects the live schema instead of trusting
// the bookkeeping table alone, so a database restored from an older backup is
// still repaired while an already-current database is left untouched.
type step struct {
	name   string
	needed func(*sql.Tx) (bool, error)
	apply  func(*sql.Tx) error
}

// script runs a fixed SQL upgrade.
func script(statements string) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		_, err := tx.Exec(statements)
		return err
	}
}

// addColumns adds only the columns that are actually absent, so the same step
// repairs databases that stopped at different points of the schema history.
func addColumns(table string, definitions [][2]string) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		for _, definition := range definitions {
			missing, err := missingColumns(tx, table, definition[0])
			if err != nil {
				return err
			}
			if !missing {
				continue
			}
			if _, err := tx.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + definition[0] + ` ` + definition[1]); err != nil {
				return fmt.Errorf("add %s.%s: %w", table, definition[0], err)
			}
		}
		return nil
	}
}

var steps = []step{
	{
		// Pre-cron installations stored only interval schedules.
		name: "schedule-cron-modes-v1",
		needed: func(tx *sql.Tx) (bool, error) {
			return missingColumns(tx, "schedules", "mode", "daily_time", "cron_expression", "timezone")
		},
		apply: addColumns("schedules", [][2]string{
			{"mode", "TEXT NOT NULL DEFAULT 'interval'"},
			{"daily_time", "TEXT"},
			{"cron_expression", "TEXT"},
			{"timezone", "TEXT NOT NULL DEFAULT 'UTC'"},
		}),
	},
	{
		// interval_seconds used to be mandatory for every mode, so cron and daily
		// schedules had to carry a meaningless interval to satisfy the CHECK.
		name: "schedules-interval-optional-v2",
		needed: func(tx *sql.Tx) (bool, error) {
			return lacksDefinition(tx, "schedules", "mode <> 'interval'")
		},
		apply: script(`
CREATE TABLE schedules_v2 (
    server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    action TEXT NOT NULL CHECK (action IN ('start', 'stop', 'restart')),
    enabled INTEGER NOT NULL DEFAULT 1,
    mode TEXT NOT NULL DEFAULT 'interval' CHECK (mode IN ('interval', 'daily', 'cron')),
    interval_seconds INTEGER NOT NULL DEFAULT 0
        CHECK (mode <> 'interval' OR interval_seconds >= 60),
    daily_time TEXT,
    cron_expression TEXT,
    timezone TEXT NOT NULL DEFAULT 'UTC',
    failure_count INTEGER NOT NULL DEFAULT 0,
    next_run TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
INSERT INTO schedules_v2 (server_id, action, enabled, mode, interval_seconds,
    daily_time, cron_expression, timezone, failure_count, next_run, updated_at)
SELECT server_id, action, enabled, COALESCE(mode, 'interval'), COALESCE(interval_seconds, 0),
    daily_time, cron_expression, COALESCE(timezone, 'UTC'), 0, next_run, updated_at
FROM schedules;
DROP TABLE schedules;
ALTER TABLE schedules_v2 RENAME TO schedules;
`),
	},
	{
		// A crash used to leave runs stuck in 'running' with no way to record it.
		name: "scheduler-runs-interrupted-v2",
		needed: func(tx *sql.Tx) (bool, error) {
			return lacksDefinition(tx, "scheduler_runs", "interrupted")
		},
		apply: script(`
CREATE TABLE scheduler_runs_v2 (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    action TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'interrupted')),
    error TEXT,
    started_at TEXT NOT NULL,
    finished_at TEXT
);
INSERT INTO scheduler_runs_v2 (id, server_id, action, status, error, started_at, finished_at)
SELECT id, server_id, action, status, error, started_at, finished_at FROM scheduler_runs;
DROP TABLE scheduler_runs;
ALTER TABLE scheduler_runs_v2 RENAME TO scheduler_runs;
CREATE INDEX IF NOT EXISTS idx_scheduler_runs_server_started
    ON scheduler_runs(server_id, started_at DESC);
`),
	},
	{
		// The manager now owns the game's backup cadence, which was previously
		// left at the stock value of roughly eight minutes. Zero keeps that
		// behaviour for existing servers rather than silently changing it.
		name: "servers-periodic-backup-interval-v1",
		needed: func(tx *sql.Tx) (bool, error) {
			return missingColumns(tx, "servers", "periodic_backup_interval_seconds")
		},
		apply: addColumns("servers", [][2]string{
			{"periodic_backup_interval_seconds", "INTEGER NOT NULL DEFAULT 0"},
		}),
	},
	{
		// desired_state lets the manager bring servers back after a host reboot
		// and tell an operator's stop apart from a crash. Existing servers start
		// as stopped, so an upgrade never launches a server on its own.
		name: "servers-desired-state-v1",
		needed: func(tx *sql.Tx) (bool, error) {
			return missingColumns(tx, "servers", "restart_on_crash", "desired_state")
		},
		apply: addColumns("servers", [][2]string{
			{"restart_on_crash", "INTEGER NOT NULL DEFAULT 1"},
			{"desired_state", "TEXT NOT NULL DEFAULT 'stopped' CHECK (desired_state IN ('running', 'stopped'))"},
		}),
	},
	{
		// recovery_reason blocks a server whose world failed its check after a
		// kill or crash. It is stored so the block survives a manager restart.
		name: "servers-recovery-reason-v1",
		needed: func(tx *sql.Tx) (bool, error) {
			return missingColumns(tx, "servers", "recovery_reason")
		},
		apply: addColumns("servers", [][2]string{{"recovery_reason", "TEXT"}}),
	},
	{
		name: "servers-hooks-v1",
		needed: func(tx *sql.Tx) (bool, error) {
			return missingColumns(tx, "servers", "hooks")
		},
		apply: addColumns("servers", [][2]string{{"hooks", "TEXT NOT NULL DEFAULT '{}'"}}),
	},
	{
		name: "servers-auto-update-v1",
		needed: func(tx *sql.Tx) (bool, error) {
			return missingColumns(tx, "servers", "auto_update")
		},
		apply: addColumns("servers", [][2]string{{"auto_update", "INTEGER NOT NULL DEFAULT 0"}}),
	},
	{
		name: "servers-warnings-v1",
		needed: func(tx *sql.Tx) (bool, error) {
			return missingColumns(tx, "servers", "warnings")
		},
		apply: addColumns("servers", [][2]string{{"warnings", "TEXT NOT NULL DEFAULT '{}'"}}),
	},
	{
		name: "backup-events-sha256-v1",
		needed: func(tx *sql.Tx) (bool, error) {
			return missingColumns(tx, "backup_events", "sha256")
		},
		apply: addColumns("backup_events", [][2]string{{"sha256", "TEXT NOT NULL DEFAULT ''"}}),
	},
	{
		name: "servers-options-v1",
		needed: func(tx *sql.Tx) (bool, error) {
			return missingColumns(tx, "servers", "options")
		},
		apply: addColumns("servers", [][2]string{{"options", "TEXT NOT NULL DEFAULT '{}'"}}),
	},
	{
		// Channels were Discord only; Telegram and any HTTP endpoint came
		// later, with settings of their own.
		name: "notification-channels-kinds-v1",
		needed: func(tx *sql.Tx) (bool, error) {
			return lacksDefinition(tx, "notification_channels", "'telegram'")
		},
		apply: script(`
CREATE TABLE notification_channels_v2 (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('discord', 'telegram', 'http')),
    secret TEXT NOT NULL,
    settings TEXT NOT NULL DEFAULT '{}',
    server_id TEXT REFERENCES servers(id) ON DELETE CASCADE,
    min_severity TEXT NOT NULL DEFAULT 'info' CHECK (min_severity IN ('info', 'warning', 'error')),
    event_types TEXT NOT NULL DEFAULT '[]',
    language TEXT NOT NULL DEFAULT 'ru' CHECK (language IN ('ru', 'en')),
    mention TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1,
    last_sent_at TEXT,
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
INSERT INTO notification_channels_v2 (id, name, kind, secret, server_id, min_severity, event_types,
    language, mention, enabled, last_sent_at, last_error, created_at, updated_at)
SELECT id, name, kind, secret, server_id, min_severity, event_types,
    language, mention, enabled, last_sent_at, last_error, created_at, updated_at
FROM notification_channels;
DROP TABLE notification_channels;
ALTER TABLE notification_channels_v2 RENAME TO notification_channels;
`),
	},
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(baseline); err != nil {
		return fmt.Errorf("database migration: %w", err)
	}
	for _, s := range steps {
		if err := applyStep(db, s); err != nil {
			return fmt.Errorf("migration %s: %w", s.name, err)
		}
	}
	return nil
}

// Each step runs in its own transaction: a failure rolls that step back and
// leaves every table it touched exactly as it was.
func applyStep(db *sql.DB, s step) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	needed, err := s.needed(tx)
	if err != nil {
		return err
	}
	if needed {
		if err := s.apply(tx); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO manager_migrations(name) VALUES(?)`, s.name); err != nil {
		return err
	}
	return tx.Commit()
}

// missingColumns reports whether any of the named columns is absent. A table that
// does not exist yet was just created from the baseline and needs no upgrade.
func missingColumns(tx *sql.Tx, table string, columns ...string) (bool, error) {
	present := map[string]bool{}
	rows, err := tx.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(present) == 0 {
		return false, nil
	}
	for _, column := range columns {
		if !present[column] {
			return true, nil
		}
	}
	return false, nil
}

// lacksDefinition reports whether a table exists but its stored DDL does not yet
// contain the given fragment. SQLite cannot alter a CHECK constraint in place, so
// this is how an outdated constraint is detected before rebuilding the table.
func lacksDefinition(tx *sql.Tx, table, fragment string) (bool, error) {
	var ddl string
	err := tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !strings.Contains(ddl, fragment), nil
}
