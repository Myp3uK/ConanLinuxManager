package server

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

func (r *Repository) Create(ctx context.Context, c Config) error {
	if c.RamdiskEnabled && c.RamdiskSizeMB == 0 {
		c.RamdiskSizeMB = 2048
	}
	if c.SyncIntervalSeconds == 0 {
		c.SyncIntervalSeconds = 900
	}
	if err := c.Validate(); err != nil {
		return err
	}
	hooks, err := encodeHooks(c.Hooks)
	if err != nil {
		return err
	}
	warnings, err := encodeWarnings(c.Warnings)
	if err != nil {
		return err
	}
	opts, err := encodeOptions(c)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = r.db.ExecContext(ctx, `INSERT INTO servers
        (id,name,engine,runtime,install_dir,steamcmd_dir,branch,launch_args,game_port,query_port,rcon_port,ramdisk_enabled,ramdisk_path,ramdisk_size_mb,sync_interval_seconds,periodic_backup_interval_seconds,restart_on_crash,desired_state,hooks,auto_update,warnings,options,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'stopped',?,?,?,?,?,?)`, c.ID, c.Name, c.Engine, c.Runtime, c.InstallDir, c.SteamCMDDir, defaultBranch(c.Branch), c.LaunchArgs, c.GamePort, c.QueryPort, c.RCONPort, c.RamdiskEnabled, c.RamdiskPath, c.RamdiskSizeMB, c.SyncIntervalSeconds, c.PeriodicBackupIntervalSeconds, c.RestartOnCrash, hooks, c.AutoUpdate, warnings, opts, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}
	return nil
}

func (r *Repository) List(ctx context.Context) ([]Config, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,name,engine,runtime,install_dir,steamcmd_dir,branch,launch_args,game_port,query_port,rcon_port,ramdisk_enabled,ramdisk_path,ramdisk_size_mb,sync_interval_seconds,periodic_backup_interval_seconds,restart_on_crash,desired_state,recovery_reason,hooks,auto_update,warnings,options,created_at,updated_at FROM servers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	defer rows.Close()
	result := make([]Config, 0)
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *Repository) Get(ctx context.Context, id string) (Config, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id,name,engine,runtime,install_dir,steamcmd_dir,branch,launch_args,game_port,query_port,rcon_port,ramdisk_enabled,ramdisk_path,ramdisk_size_mb,sync_interval_seconds,periodic_backup_interval_seconds,restart_on_crash,desired_state,recovery_reason,hooks,auto_update,warnings,options,created_at,updated_at FROM servers WHERE id=?`, id)
	c, err := scan(row)
	if err == sql.ErrNoRows {
		return Config{}, fmt.Errorf("server %q not found", id)
	}
	return c, err
}

// Update rewrites the settings an operator may change after creation. The
// identity and the directories that on-disk state is keyed by are deliberately
// not among them: changing those would orphan a RAM session or an install.
func (r *Repository) Update(ctx context.Context, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	hooks, err := encodeHooks(c.Hooks)
	if err != nil {
		return err
	}
	warnings, err := encodeWarnings(c.Warnings)
	if err != nil {
		return err
	}
	opts, err := encodeOptions(c)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx, `UPDATE servers SET
        name=?,branch=?,launch_args=?,game_port=?,query_port=?,rcon_port=?,
        ramdisk_enabled=?,ramdisk_size_mb=?,sync_interval_seconds=?,
        periodic_backup_interval_seconds=?,restart_on_crash=?,hooks=?,auto_update=?,warnings=?,options=?,updated_at=? WHERE id=?`,
		c.Name, defaultBranch(c.Branch), c.LaunchArgs, c.GamePort, c.QueryPort, c.RCONPort,
		c.RamdiskEnabled, c.RamdiskSizeMB, c.SyncIntervalSeconds,
		c.PeriodicBackupIntervalSeconds, c.RestartOnCrash, hooks, c.AutoUpdate, warnings, opts, now.Format(time.RFC3339Nano), c.ID)
	if err != nil {
		return fmt.Errorf("update server: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("server %q not found", c.ID)
	}
	return nil
}

// SetDesiredState records what the operator asked for. It is separate from
// Update because it changes on every start and stop, not on configuration.
func (r *Repository) SetDesiredState(ctx context.Context, id, state string) error {
	if state != "running" && state != "stopped" {
		return fmt.Errorf("desired state must be running or stopped")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE servers SET desired_state=? WHERE id=?`, state, id)
	if err != nil {
		return fmt.Errorf("record desired state: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("server %q not found", id)
	}
	return nil
}

// SetRecovery blocks a server from starting and records why.
func (r *Repository) SetRecovery(ctx context.Context, id, reason string) error {
	if reason == "" {
		return fmt.Errorf("recovery reason is required")
	}
	_, err := r.db.ExecContext(ctx, `UPDATE servers SET recovery_reason=? WHERE id=?`, reason, id)
	return err
}

// ClearRecovery lifts the block after the world was verified.
func (r *Repository) ClearRecovery(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE servers SET recovery_reason=NULL WHERE id=?`, id)
	return err
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM servers WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete server: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("server %q not found", id)
	}
	return nil
}

type scanner interface{ Scan(...any) error }

func scan(s scanner) (Config, error) {
	var c Config
	var created, updated any
	var engine, runtime string
	var rcon sql.NullInt64
	var ramdisk, restart int
	var recovery sql.NullString
	var hooks string
	var autoUpdate int
	var warnings, opts string
	if err := s.Scan(&c.ID, &c.Name, &engine, &runtime, &c.InstallDir, &c.SteamCMDDir, &c.Branch, &c.LaunchArgs, &c.GamePort, &c.QueryPort, &rcon, &ramdisk, &c.RamdiskPath, &c.RamdiskSizeMB, &c.SyncIntervalSeconds, &c.PeriodicBackupIntervalSeconds, &restart, &c.DesiredState, &recovery, &hooks, &autoUpdate, &warnings, &opts, &created, &updated); err != nil {
		return Config{}, err
	}
	c.Engine, c.Runtime = Engine(engine), Runtime(runtime)
	c.RamdiskEnabled = ramdisk != 0
	c.RestartOnCrash = restart != 0
	c.RecoveryReason = recovery.String
	c.Hooks = decodeHooks(hooks)
	c.AutoUpdate = autoUpdate != 0
	c.Warnings = decodeWarnings(warnings)
	decodeOptions(opts, &c)
	c.legacyWarningText()
	if rcon.Valid {
		value := int(rcon.Int64)
		c.RCONPort = &value
	}
	c.CreatedAt = parseDatabaseTime(created)
	c.UpdatedAt = parseDatabaseTime(updated)
	return c, nil
}

func parseDatabaseTime(raw any) time.Time {
	var value string
	switch v := raw.(type) {
	case string:
		value = v
	case []byte:
		value = string(v)
	case time.Time:
		return v.UTC()
	default:
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func defaultBranch(value string) string {
	if value == "" {
		return "public"
	}
	return value
}
