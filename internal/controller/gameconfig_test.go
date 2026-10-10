package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Myp3uK/ConanLinuxManager/internal/events"
	"github.com/Myp3uK/ConanLinuxManager/internal/gameconfig"
)

func TestConfigEditsAreVersionedAndReversible(t *testing.T) {
	c, _ := testController(t)
	c.Events = events.NewBus()
	install := t.TempDir()
	createServer(t, c, "a", install, true)
	cfg, err := c.Repo.Get(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	path, err := gameconfig.FilePath(cfg, "ServerSettings")
	if err != nil {
		t.Fatal(err)
	}
	original := "[ServerSettings]\nPlayerDamageMultiplier=1\nMaxNudity=0\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	changes, err := c.EditConfig(ctx, "a", "ServerSettings", map[string]map[string]string{"ServerSettings": {"PlayerDamageMultiplier": "3"}}, nil)
	if err != nil || len(changes) != 1 {
		t.Fatalf("edit: %v %+v", err, changes)
	}
	view, err := c.ReadConfig(ctx, "a", "ServerSettings")
	if err != nil || !strings.Contains(view.Content, "PlayerDamageMultiplier=3") {
		t.Fatalf("read after edit: %v %s", err, view.Content)
	}
	versions, err := c.Repo.ConfigVersions(ctx, "a", "ServerSettings")
	if err != nil || len(versions) != 1 {
		t.Fatalf("versions: %+v %v", versions, err)
	}
	if countEvents(c.Events, "a", events.ConfigChanged) != 1 {
		t.Fatal("change not reported")
	}

	// Restoring puts the original back and keeps the edited content as a new
	// version, so the restore can be undone too.
	if err := c.RestoreConfig(ctx, "a", "ServerSettings", versions[0].ID); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Fatalf("restore: %q", data)
	}
	versions, _ = c.Repo.ConfigVersions(ctx, "a", "ServerSettings")
	if len(versions) != 2 || !strings.Contains(versions[0].Reason, "restore") {
		t.Fatalf("restore did not save the replaced content: %+v", versions)
	}
}

// The game rewrites its INI files when it stops, so an edit to a running
// server would be lost without any error.
func TestConfigCannotBeEditedWhileTheServerRuns(t *testing.T) {
	c, _ := testController(t)
	createServer(t, c, "a", t.TempDir(), true)
	ctx := context.Background()
	if _, err := c.acquire(ctx, "a", "running"); err != nil {
		t.Fatal(err)
	}
	defer c.release("a")
	if _, err := c.EditConfig(ctx, "a", "Game", map[string]map[string]string{"S": {"K": "V"}}, nil); err == nil {
		t.Fatal("edit accepted while running")
	}
	if err := c.ReplaceConfig(ctx, "a", "Engine", "[X]\n"); err == nil {
		t.Fatal("replace accepted while running")
	}
	if _, err := c.ReadConfig(ctx, "a", "Game"); err != nil {
		t.Fatalf("reading must work while running: %v", err)
	}
}

func TestOnlyKnownConfigFilesAreReachable(t *testing.T) {
	c, _ := testController(t)
	createServer(t, c, "a", t.TempDir(), true)
	for _, file := range []string{"../../../etc/passwd", "Scalability", ""} {
		if _, err := c.ReadConfig(context.Background(), "a", file); err == nil {
			t.Errorf("file %q readable", file)
		}
	}
}

func TestReplaceConfigCreatesAnEmptyCustomFile(t *testing.T) {
	c, create := testController(t)
	ctx := context.Background()
	cfg := create("a", filepath.Join(t.TempDir(), "game"), "")
	// Adding a config a mod will fill in starts with an empty file, which an
	// unchanged-content check would otherwise swallow.
	if err := c.ReplaceConfig(ctx, cfg.ID, "ModHttp.ini", ""); err != nil {
		t.Fatal(err)
	}
	files, err := c.ConfigFiles(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found *gameconfig.File
	for i := range files {
		if files[i].Name == "ModHttp.ini" {
			found = &files[i]
		}
	}
	if found == nil || !found.Exists || !found.Custom {
		t.Fatalf("the file was not created: %+v", files)
	}
	view, err := c.ReadConfig(ctx, cfg.ID, "ModHttp.ini")
	if err != nil || view.Content != "" || !view.Exists {
		t.Fatalf("read back: %+v, %v", view, err)
	}
}

func TestDeleteConfigRemovesOnlyCustomFiles(t *testing.T) {
	c, create := testController(t)
	ctx := context.Background()
	cfg := create("a", filepath.Join(t.TempDir(), "game"), "")
	if err := c.ReplaceConfig(ctx, cfg.ID, "ModHttp.ini", "[Http]\n"); err != nil {
		t.Fatal(err)
	}
	// The game writes its own files; a missing one is a server quietly on
	// defaults, so those cannot be removed.
	if err := c.DeleteConfig(ctx, cfg.ID, "Game"); err == nil {
		t.Error("the game's own file should not be removable")
	}
	if err := c.DeleteConfig(ctx, cfg.ID, "../escape.ini"); err == nil {
		t.Error("a path should be refused")
	}
	if err := c.DeleteConfig(ctx, cfg.ID, "ModHttp.ini"); err != nil {
		t.Fatal(err)
	}
	// Removing it twice is an error, not a silent success.
	if err := c.DeleteConfig(ctx, cfg.ID, "ModHttp.ini"); err == nil {
		t.Error("deleting a file that is gone should say so")
	}
	files, err := c.ConfigFiles(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Name == "ModHttp.ini" {
			t.Fatalf("the file is still there: %+v", f)
		}
	}
	// Its content stays in the history, so a file removed by mistake is not
	// lost with it.
	versions, err := c.Repo.ConfigVersions(ctx, cfg.ID, "ModHttp.ini")
	if err != nil || len(versions) == 0 {
		t.Fatalf("no version kept: %v, %+v", err, versions)
	}
}
