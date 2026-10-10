package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Myp3uK/ConanLinuxManager/internal/events"
	"github.com/Myp3uK/ConanLinuxManager/internal/gameconfig"
	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

// acquireForConfig explains the refusal an operator will most often meet.
func (c *Controller) acquireForConfig(ctx context.Context, id string) (server.Config, error) {
	if state := c.Lifecycle(id).State; state == "running" || state == "starting" || state == "stopping" {
		return server.Config{}, fmt.Errorf("stop the server first: the game rewrites its config files when it stops, so a change made now would be lost")
	}
	return c.acquire(ctx, id, "configuring")
}

// maxConfigSize bounds a file written through the API; the game's own files
// are a few kilobytes.
const maxConfigSize = 1 << 20

// ConfigView is a game INI file as the API shows it.
type ConfigView struct {
	File     string               `json:"file"`
	Path     string               `json:"path"`
	Exists   bool                 `json:"exists"`
	Content  string               `json:"content"`
	Sections []gameconfig.Section `json:"sections"`
	// Managed lists keys the manager rewrites before each start; editing them
	// here has no lasting effect.
	Managed []string `json:"managed,omitempty"`
}

func readConfig(cfg server.Config, file string) (string, string, bool, error) {
	path, err := gameconfig.FilePath(cfg, file)
	if err != nil {
		return "", "", false, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return path, "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return path, string(data), true, nil
}

// ConfigFiles lists the INI files a server has: the game's own three and
// whatever else is in its config directory, such as the files mods read.
func (c *Controller) ConfigFiles(ctx context.Context, id string) ([]gameconfig.File, error) {
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return gameconfig.List(cfg)
}

// DeleteConfig removes a file an operator or a mod added. The game's own files
// are never removed: it writes them itself, and a missing one is a server that
// starts on defaults without saying so.
func (c *Controller) DeleteConfig(ctx context.Context, id, file string) error {
	if _, known := gameconfig.Files[file]; known {
		return server.ErrInvalidConfig("the game's own files cannot be removed, only emptied")
	}
	if !gameconfig.ValidConfigName(file) {
		return server.ErrInvalidConfig("invalid config file name")
	}
	cfg, err := c.acquireForConfig(ctx, id)
	if err != nil {
		return err
	}
	defer c.release(id)
	path, before, exists, err := readConfig(cfg, file)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("config file %q does not exist", file)
	}
	// The content is kept as a version first, so a file removed by mistake can
	// be read back out of the history.
	if _, err := c.Repo.SaveConfigVersion(ctx, server.ConfigVersion{ServerID: cfg.ID, File: file,
		Reason: "before delete", Content: before}); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	c.emit(events.ConfigChanged, cfg, file+": file removed", map[string]string{"file": file})
	return nil
}

// ReadConfig shows a game INI file. Reading is allowed while the server runs;
// only writing needs it stopped.
func (c *Controller) ReadConfig(ctx context.Context, id, file string) (ConfigView, error) {
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil {
		return ConfigView{}, err
	}
	path, content, exists, err := readConfig(cfg, file)
	if err != nil {
		return ConfigView{}, err
	}
	view := ConfigView{File: file, Path: path, Exists: exists, Content: content, Sections: gameconfig.Parse(content)}
	if view.Sections == nil {
		// A file the game has not written yet has no sections; clients get
		// an empty list rather than null.
		view.Sections = []gameconfig.Section{}
	}
	if file == "Game" && gameconfig.SettingsFor(cfg).Managed() {
		view.Managed = []string{"[/Script/ConanSandbox.ConanGameMode] PeriodicBackupInterval"}
	}
	return view, nil
}

// EditConfig changes keys in a game INI file. The game reads these files at
// start and rewrites them when it stops, so a change made while it runs would
// be lost without an error; the reservation refuses a running server.
func (c *Controller) EditConfig(ctx context.Context, id, file string, set map[string]map[string]string, remove map[string][]string) ([]gameconfig.Change, error) {
	cfg, err := c.acquireForConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	defer c.release(id)
	path, before, _, err := readConfig(cfg, file)
	if err != nil {
		return nil, err
	}
	after, changes, err := gameconfig.Edit(before, set, remove)
	if err != nil {
		return nil, server.ErrInvalidConfig(err.Error())
	}
	if len(changes) == 0 {
		return changes, nil
	}
	if err := c.replaceConfig(ctx, cfg, file, path, before, after, "before edit"); err != nil {
		return nil, err
	}
	details := map[string]string{"file": file}
	for i, ch := range changes {
		if i == 8 {
			details["more"] = strconv.Itoa(len(changes) - i)
			break
		}
		key := "[" + ch.Section + "] " + ch.Key
		if ch.Removed {
			details[key] = "removed"
		} else {
			details[key] = ch.New
		}
	}
	c.emit(events.ConfigChanged, cfg, fmt.Sprintf("%s: %d setting(s) changed", file, len(changes)), details)
	return changes, nil
}

// ReplaceConfig writes a whole file, for edits the key editor cannot express,
// such as Unreal arrays.
func (c *Controller) ReplaceConfig(ctx context.Context, id, file, content string) error {
	if len(content) > maxConfigSize {
		return server.ErrInvalidConfig("config file is too large")
	}
	if strings.ContainsRune(content, 0) {
		return server.ErrInvalidConfig("config file may not contain NUL bytes")
	}
	cfg, err := c.acquireForConfig(ctx, id)
	if err != nil {
		return err
	}
	defer c.release(id)
	path, before, exists, err := readConfig(cfg, file)
	if err != nil {
		return err
	}
	// A file that is not there yet is always a change, even when the content
	// is empty: that is how an operator adds a config a mod will fill in.
	if exists && before == content {
		return nil
	}
	if err := c.replaceConfig(ctx, cfg, file, path, before, content, "before replace"); err != nil {
		return err
	}
	c.emit(events.ConfigChanged, cfg, file+": file replaced", map[string]string{"file": file})
	return nil
}

// RestoreConfig puts back an earlier version. The current content is saved
// first, so a restore can itself be undone.
func (c *Controller) RestoreConfig(ctx context.Context, id, file string, version int64) error {
	cfg, err := c.acquireForConfig(ctx, id)
	if err != nil {
		return err
	}
	defer c.release(id)
	v, err := c.Repo.ConfigVersion(ctx, id, file, version)
	if err != nil {
		return err
	}
	path, before, _, err := readConfig(cfg, file)
	if err != nil {
		return err
	}
	if err := c.replaceConfig(ctx, cfg, file, path, before, v.Content, fmt.Sprintf("before restore of version %d", version)); err != nil {
		return err
	}
	c.emit(events.ConfigChanged, cfg, fmt.Sprintf("%s: restored version %d", file, version), map[string]string{"file": file})
	return nil
}

// replaceConfig saves the old content as a version, then writes the new
// content atomically. The caller holds the server's reservation.
func (c *Controller) replaceConfig(ctx context.Context, cfg server.Config, file, path, before, after, reason string) error {
	if _, err := c.Repo.SaveConfigVersion(ctx, server.ConfigVersion{ServerID: cfg.ID, File: file, Reason: reason, Content: before}); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return gameconfig.WriteFileAtomic(path, []byte(after))
}
