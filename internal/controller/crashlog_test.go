package controller

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

func TestCrashExcerptPicksTheExplainingLines(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(dir, "ConanSandbox", "Saved", "Logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	game := "[1]LogNet: fine\r\n[2]LogServerStats: Status report. Uptime=60\r\n" +
		"[3]LogCore: Error: appError called: Assertion failed: Actor != nullptr\r\n[4]LogNet: fine again\r\n"
	if err := os.WriteFile(filepath.Join(logs, "ConanSandbox.log"), []byte(game), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, LogNames["process"]), []byte("Signal 11 caught.\nbye\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := crashExcerpt(server.Config{InstallDir: dir})
	want := "[3]LogCore: Error: appError called: Assertion failed: Actor != nullptr\nSignal 11 caught."
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if crashExcerpt(server.Config{InstallDir: t.TempDir()}) != "" {
		t.Fatal("excerpt from missing logs")
	}
	long := strings.Repeat("LogCore: Error: x\n", 50)
	_ = os.WriteFile(filepath.Join(logs, "ConanSandbox.log"), []byte(long), 0o644)
	if n := strings.Count(crashExcerpt(server.Config{InstallDir: dir}), "\n"); n != crashLines-1 {
		t.Fatalf("excerpt has %d lines, want %d", n+1, crashLines)
	}
}
