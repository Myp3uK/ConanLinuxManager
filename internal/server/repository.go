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
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `INSERT INTO servers
        (id,name,engine,runtime,install_dir,steamcmd_dir,branch,launch_args,game_port,query_port,rcon_port,ramdisk_enabled,ramdisk_path,ramdisk_size_mb,sync_interval_seconds,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.ID, c.Name, c.Engine, c.Runtime, c.InstallDir, c.SteamCMDDir, defaultBranch(c.Branch), c.LaunchArgs, c.GamePort, c.QueryPort, c.RCONPort, c.RamdiskEnabled, c.RamdiskPath, c.RamdiskSizeMB, c.SyncIntervalSeconds, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}
	return nil
}

func (r *Repository) List(ctx context.Context) ([]Config, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,name,engine,runtime,install_dir,steamcmd_dir,branch,launch_args,game_port,query_port,rcon_port,ramdisk_enabled,ramdisk_path,ramdisk_size_mb,sync_interval_seconds,created_at,updated_at FROM servers ORDER BY name`)
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
	row := r.db.QueryRowContext(ctx, `SELECT id,name,engine,runtime,install_dir,steamcmd_dir,branch,launch_args,game_port,query_port,rcon_port,ramdisk_enabled,ramdisk_path,ramdisk_size_mb,sync_interval_seconds,created_at,updated_at FROM servers WHERE id=?`, id)
	c, err := scan(row)
	if err == sql.ErrNoRows {
		return Config{}, fmt.Errorf("server %q not found", id)
	}
	return c, err
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
	var ramdisk int
	if err := s.Scan(&c.ID, &c.Name, &engine, &runtime, &c.InstallDir, &c.SteamCMDDir, &c.Branch, &c.LaunchArgs, &c.GamePort, &c.QueryPort, &rcon, &ramdisk, &c.RamdiskPath, &c.RamdiskSizeMB, &c.SyncIntervalSeconds, &created, &updated); err != nil {
		return Config{}, err
	}
	c.Engine, c.Runtime = Engine(engine), Runtime(runtime)
	c.RamdiskEnabled = ramdisk != 0
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
