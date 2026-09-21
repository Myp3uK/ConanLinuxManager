package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTailLogReadsNamedLogsAndStaysInside(t *testing.T) {
	c, _ := testController(t)
	install := t.TempDir()
	createServer(t, c, "a", install, false)
	game := filepath.Join(install, LogNames["game"])
	if err := os.MkdirAll(filepath.Dir(game), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(game, []byte("LogInit: start\nLogNet: Join succeeded: Конан\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	lines, err := c.TailLog(ctx, "a", "", 10, "join")
	if err != nil || len(lines) != 1 {
		t.Fatalf("%q %v", lines, err)
	}
	if _, err := c.TailLog(ctx, "a", "../../etc/passwd", 10, ""); err == nil {
		t.Fatal("path outside the install directory accepted")
	}
	secret := filepath.Join(t.TempDir(), "conan-manager.env")
	if err := os.WriteFile(secret, []byte("CONAN_MANAGER_SECRET_KEY=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(install, "innocent.log")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := c.TailLog(ctx, "a", "innocent.log", 10, ""); err == nil {
		t.Fatal("a symlink led out of the install directory")
	}
}
