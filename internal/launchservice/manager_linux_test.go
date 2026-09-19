package launchservice

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/process"
	"github.com/example/conan-manager/internal/server"
)

// Opt-in host integration: disposable unit/config/database only.
func TestSystemdLauncher(t *testing.T) {
	launcher := os.Getenv("CONAN_TEST_LAUNCHER")
	if launcher == "" {
		t.Skip("set CONAN_TEST_LAUNCHER on isolated Linux test host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	id := "integration-" + strconv.Itoa(os.Getpid())
	root, err := os.MkdirTemp("/var/tmp", "conan-service-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(root, "game")
	ram := filepath.Join(root, "ram")
	saved := filepath.Join(install, "ConanSandbox/Saved")
	executable := filepath.Join(install, "ConanSandbox/Binaries/Linux/ConanSandboxServer-Linux-Shipping")
	for _, p := range []string{saved, filepath.Dir(executable), ram} {
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{install, filepath.Join(install, "ConanSandbox"), saved} {
		if err := os.Chown(p, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	code := "#!/usr/bin/python3.12\nimport sqlite3,time\nfrom pathlib import Path\ndb=sqlite3.connect('ConanSandbox/Saved/game_0.db')\ndb.execute('PRAGMA journal_mode=WAL')\ndb.execute('CREATE TABLE IF NOT EXISTS probe(value INTEGER)')\ndb.execute('INSERT INTO probe VALUES (42)')\ndb.commit()\nPath('ready').touch()\ntime.sleep(120)\n"
	if err := os.WriteFile(executable, []byte(code), 0755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("mount", "-t", "tmpfs", "-o", "size=64m,nosuid,nodev", "tmpfs", ram).CombinedOutput(); err != nil {
		t.Fatalf("mount: %s %v", out, err)
	}
	defer exec.Command("umount", ram).Run()
	if err := os.Mkdir(filepath.Join(ram, "database"), 0755); err != nil {
		t.Fatal(err)
	}
	configDir := "/etc/conan-manager/launch"
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, id+".json")
	b, _ := json.Marshal(map[string]any{"server_id": id, "install_dir": install, "ram_dir": ram, "executable": executable, "args": []string{"ConanSandbox"}, "uid": 65534, "gid": 65534})
	if err := os.WriteFile(config, b, 0644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(config)
	unitPath := filepath.Join("/run/systemd/system", unit(id))
	unitText := "[Service]\nType=exec\nExecStart=" + launcher + " --config " + config + "\nKillMode=mixed\nTimeoutStopSec=810\n"
	if err := os.WriteFile(unitPath, []byte(unitText), 0644); err != nil {
		t.Fatal(err)
	}
	defer func() {
		command(context.Background(), "stop", unit(id))
		os.Remove(unitPath)
		command(context.Background(), "daemon-reload")
		command(context.Background(), "reset-failed", unit(id))
	}()
	if _, err := command(ctx, "daemon-reload"); err != nil {
		t.Fatal(err)
	}
	m := New(id)
	cfg := server.Config{ID: id, InstallDir: install, RamdiskPath: ram, RamdiskEnabled: true, Runtime: server.RuntimeNative}
	bad := cfg
	bad.LaunchArgs = "untrusted"
	if err := m.Start(ctx, bad); err == nil {
		t.Fatal("accepted mismatched args")
	}
	if err := m.Start(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(install, "ready")); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	s, err := m.Inspect(ctx, id)
	if err != nil || s.State != process.StateRunning || s.PID == 0 {
		t.Fatalf("snapshot %+v %v", s, err)
	}
	if err := m.Start(ctx, cfg); err == nil {
		t.Fatal("duplicate start accepted")
	}
	if err := m.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := m.Wait(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(install, "ConanSandbox/.conan-manager-ram-session")); !os.IsNotExist(err) {
		t.Fatal("session not persisted", err)
	}
	check := "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); assert c.execute('select value from probe').fetchone()[0]==42; assert c.execute('pragma integrity_check').fetchone()[0]=='ok'"
	if out, err := exec.Command("python3", "-c", check, filepath.Join(saved, "game_0.db")).CombinedOutput(); err != nil {
		t.Fatalf("persist %s %v", out, err)
	}
	if err := os.Remove(filepath.Join(install, "ready")); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx, cfg); err != nil {
		t.Fatal("restart", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(install, "ready")); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	if err := m.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := m.Wait(ctx, id); err != nil {
		t.Fatal(err)
	}
	check = "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); assert c.execute('select count(*) from probe').fetchone()[0]==2; assert c.execute('pragma integrity_check').fetchone()[0]=='ok'"
	if out, err := exec.Command("python3", "-c", check, filepath.Join(saved, "game_0.db")).CombinedOutput(); err != nil {
		t.Fatalf("second persist %s %v", out, err)
	}
	entries, err := os.ReadDir(filepath.Join(ram, "database"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("RAM cleanup: %v %v", entries, err)
	}
}
