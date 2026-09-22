package serverstats

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStartupReportOfThisRunOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ConanSandbox.log")
	old := "[2026.09.22-03.02.51:100][  0]LogServerStats: Startup report. StartupTime=21 Name=Old\n"
	if err := os.WriteFile(path, []byte("[2026.09.22-06.13.50:000][  0]LogInit: boot\n"+old), 0o644); err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 9, 22, 6, 13, 50, 0, time.UTC)
	s := &Startup{Path: path}
	if _, ok, err := s.Find(since); ok || err != nil {
		t.Fatalf("an older run's report counted: %v %v", ok, err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("[2026.09.22-06.14.11:622][  0]LogServerStats: Startup report. StartupTime=21 Name=ConanManager Test")
	_ = f.Close()
	if _, ok, _ := s.Find(since); ok {
		t.Fatal("a line still being written counted")
	}
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(" QueryPort=37115\n")
	_ = f.Close()
	at, ok, err := s.Find(since)
	if !ok || err != nil || !at.Equal(time.Date(2026, 9, 22, 6, 14, 11, 622_000_000, time.UTC)) {
		t.Fatalf("got %v %v %v", at, ok, err)
	}
	// The game rewrites the log at its next start.
	if err := os.WriteFile(path, []byte("[2026.09.22-07.00.40:000][  0]LogInit: boot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Find(since); ok || err != nil {
		t.Fatalf("after the rewrite: %v %v", ok, err)
	}
}
