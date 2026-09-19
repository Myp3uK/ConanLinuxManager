package steamcmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureValidatesDirectory(t *testing.T) {
	service := Service{}
	if _, err := service.Ensure(context.Background(), "relative"); err == nil {
		t.Fatal("relative SteamCMD path accepted")
	}
	directory := filepath.Join(t.TempDir(), "steamcmd")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(directory, "steamcmd.sh")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o750); err != nil {
		t.Fatal(err)
	}
	got, err := service.Ensure(context.Background(), directory)
	if err != nil || got != binary {
		t.Fatalf("existing installation: %v, %s", err, got)
	}
}
