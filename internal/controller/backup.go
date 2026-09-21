package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/example/conan-manager/internal/backup"
	"github.com/example/conan-manager/internal/dbcheck"
	"github.com/example/conan-manager/internal/events"
	"github.com/example/conan-manager/internal/hooks"
	"github.com/example/conan-manager/internal/ramstate"
	"github.com/example/conan-manager/internal/recoveryalert"
	"github.com/example/conan-manager/internal/server"
)

// Reasons a backup is taken. An operator's own reason must match validReason.
const (
	ReasonManual     = "manual"
	ReasonPreUpdate  = "pre-update"
	ReasonPreRestore = "pre-restore"
)

var validReason = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// backupDir is where a server's backups live, apart from the game's own
// rotation so the game never deletes them. It may be on another disk.
func (c *Controller) backupDir(cfg server.Config) string {
	root := c.BackupDir
	if root == "" {
		root = filepath.Join(filepath.Dir(cfg.InstallDir), "backups")
	}
	return filepath.Join(root, cfg.ID)
}

// BackupJob takes a backup in the background.
func (c *Controller) BackupJob(ctx context.Context, id, reason string) (Job, error) {
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if reason == "" {
		reason = ReasonManual
	}
	if !validReason.MatchString(reason) {
		return Job{}, fmt.Errorf("reason must be lowercase letters, digits and dashes")
	}
	if !c.beginBackup(id) {
		return Job{}, fmt.Errorf("a backup of server %q is already running", id)
	}
	now := time.Now().UTC()
	job := Job{ID: fmt.Sprintf("backup-%d", now.UnixNano()), ServerID: id, Kind: "backup", Status: "running", CreatedAt: now, UpdatedAt: now}
	c.setJob(job)
	// The goroutine gets its own copy: the caller returns job concurrently.
	go func(job Job) {
		defer c.endBackup(id)
		_, err := c.takeBackup(context.Background(), cfg, reason)
		job.UpdatedAt = time.Now().UTC()
		job.Status = "completed"
		if err != nil {
			job.Status, job.Error = "failed", err.Error()
		}
		c.setJob(job)
	}(job)
	return job, nil
}

// takeBackup copies the world, running or not: pre_backup hooks may refuse,
// post_backup hooks get the file, for example to ship it off the host.
func (c *Controller) takeBackup(ctx context.Context, cfg server.Config, reason string) (server.Backup, error) {
	source := ramstate.LayoutFor(cfg).GameDatabase
	env := map[string]string{"CONAN_BACKUP_REASON": reason}
	if err := c.runHooks(ctx, hooks.Gate, "pre_backup", cfg, env); err != nil {
		c.emit(events.BackupFailed, cfg, "A pre_backup hook refused the backup", map[string]string{"error": err.Error(), "reason": reason})
		return server.Backup{}, fmt.Errorf("pre_backup hook: %w", err)
	}
	c.emit(events.BackupStarted, cfg, "Backup started", map[string]string{"reason": reason})
	started := time.Now()
	name := fmt.Sprintf("game_0_%s_%s.db", started.UTC().Format("20060102T150405.000Z"), reason)
	snap, err := backup.TakeSnapshot(ctx, source, filepath.Join(c.backupDir(cfg), name))
	if err != nil {
		c.emit(events.BackupFailed, cfg, "Backup failed", map[string]string{"error": err.Error(), "reason": reason})
		return server.Backup{}, err
	}
	record, err := c.Repo.AddBackup(ctx, server.Backup{ServerID: cfg.ID, Path: snap.Path, Source: source, Reason: reason, Size: snap.Size, SHA256: snap.SHA256})
	if err != nil {
		_ = os.Remove(snap.Path)
		c.emit(events.BackupFailed, cfg, "Backup could not be recorded", map[string]string{"error": err.Error(), "reason": reason})
		return server.Backup{}, err
	}
	c.emit(events.BackupFinished, cfg, "Backup finished", map[string]string{
		"reason": reason,
		"took":   time.Since(started).Round(time.Millisecond).String(),
		"size":   strconv.FormatInt(snap.Size, 10),
		"file":   filepath.Base(snap.Path),
	})
	env["CONAN_BACKUP_PATH"] = snap.Path
	env["CONAN_BACKUP_SHA256"] = snap.SHA256
	_ = c.runHooks(ctx, hooks.Async, "post_backup", cfg, env)
	c.pruneBackups(ctx, cfg)
	return record, nil
}

// pruneBackups keeps the newest BackupKeep backups of a server.
func (c *Controller) pruneBackups(ctx context.Context, cfg server.Config) {
	if c.BackupKeep <= 0 {
		return
	}
	list, err := c.Repo.Backups(ctx, cfg.ID)
	if err != nil || len(list) <= c.BackupKeep {
		return
	}
	for _, b := range list[c.BackupKeep:] {
		if err := c.removeBackup(ctx, cfg, b); err != nil {
			c.Logger.Warn("prune backup", "server", cfg.ID, "file", b.Path, "error", err)
		}
	}
}

func (c *Controller) removeBackup(ctx context.Context, cfg server.Config, b server.Backup) error {
	if err := os.Remove(b.Path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return c.Repo.DeleteBackup(ctx, cfg.ID, b.ID)
}

// BackupView is a backup as the API lists it.
type BackupView struct {
	server.Backup
	Exists bool `json:"exists"`
}

func (c *Controller) Backups(ctx context.Context, id string) ([]BackupView, error) {
	if _, err := c.Repo.Get(ctx, id); err != nil {
		return nil, err
	}
	list, err := c.Repo.Backups(ctx, id)
	if err != nil {
		return nil, err
	}
	views := make([]BackupView, 0, len(list))
	for _, b := range list {
		_, statErr := os.Stat(b.Path)
		views = append(views, BackupView{Backup: b, Exists: statErr == nil})
	}
	return views, nil
}

func (c *Controller) DeleteBackup(ctx context.Context, id string, backupID int64) error {
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil {
		return err
	}
	b, err := c.Repo.Backup(ctx, id, backupID)
	if err != nil {
		return err
	}
	return c.removeBackup(ctx, cfg, b)
}

// RestoreBackup puts a backup back as the world of a stopped server. The
// world it replaces is backed up first, so a restore can itself be undone.
// A restored world that passes the integrity check also lifts a recovery
// block, which is the usual reason to restore.
func (c *Controller) RestoreBackup(ctx context.Context, id string, backupID int64) error {
	if state := c.Lifecycle(id).State; state == "running" || state == "starting" || state == "stopping" {
		return fmt.Errorf("stop the server first: a world cannot be replaced under a running game")
	}
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if cfg.RamdiskEnabled {
		return fmt.Errorf("restore is not supported with the RAM disk enabled; disable it first")
	}
	b, err := c.Repo.Backup(ctx, id, backupID)
	if err != nil {
		return err
	}
	if err := backup.VerifySnapshot(b.Path, b.SHA256); err != nil {
		return fmt.Errorf("backup %d is not usable: %w", backupID, err)
	}
	database := ramstate.LayoutFor(cfg).GameDatabase
	if _, err := os.Stat(database); err == nil {
		// The current world may itself be damaged; a copy that fails its
		// check must not stop the operator from restoring.
		if _, err := c.takeBackup(ctx, cfg, ReasonPreRestore); err != nil {
			c.Logger.Warn("could not back up the world before a restore", "server", id, "error", err)
		}
	}
	if _, err := c.acquire(ctx, id, "restoring"); err != nil {
		return err
	}
	defer c.release(id)
	if err := backup.RestoreSnapshot(b.Path, database); err != nil {
		return fmt.Errorf("restore backup %d: %w", backupID, err)
	}
	if err := dbcheck.Verify(ctx, database); err != nil {
		c.emit(events.DatabaseCorrupt, cfg, "Restored world failed its check", map[string]string{"error": err.Error(), "file": filepath.Base(b.Path)})
		return err
	}
	if cfg.RecoveryReason != "" {
		if err := c.Repo.ClearRecovery(ctx, id); err != nil {
			return err
		}
		if err := recoveryalert.Clear(cfg); err != nil {
			c.Logger.Warn("clear recovery alert", "server", id, "error", err)
		}
	}
	c.emit(events.BackupRestored, cfg, "World restored from a backup", map[string]string{"file": filepath.Base(b.Path)})
	return nil
}

func (c *Controller) beginBackup(id string) bool {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.backingUp == nil {
		c.backingUp = map[string]bool{}
	}
	if c.backingUp[id] {
		return false
	}
	c.backingUp[id] = true
	return true
}

func (c *Controller) endBackup(id string) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	delete(c.backingUp, id)
}

// RestoreJob runs RestoreBackup in the background. The checks that can fail
// at once are made before the job starts, so a request gets a clear refusal.
func (c *Controller) RestoreJob(ctx context.Context, id string, backupID int64) (Job, error) {
	if state := c.Lifecycle(id).State; state == "running" || state == "starting" || state == "stopping" {
		return Job{}, fmt.Errorf("stop the server first: a world cannot be replaced under a running game")
	}
	if _, err := c.Repo.Backup(ctx, id, backupID); err != nil {
		return Job{}, err
	}
	if !c.beginBackup(id) {
		return Job{}, fmt.Errorf("a backup or restore of server %q is already running", id)
	}
	now := time.Now().UTC()
	job := Job{ID: fmt.Sprintf("restore-%d", now.UnixNano()), ServerID: id, Kind: "restore", Status: "running", CreatedAt: now, UpdatedAt: now}
	c.setJob(job)
	go func(job Job) {
		defer c.endBackup(id)
		err := c.RestoreBackup(context.Background(), id, backupID)
		job.UpdatedAt = time.Now().UTC()
		job.Status = "completed"
		if err != nil {
			job.Status, job.Error = "failed", err.Error()
		}
		c.setJob(job)
	}(job)
	return job, nil
}
