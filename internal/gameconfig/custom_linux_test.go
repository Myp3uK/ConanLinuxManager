//go:build linux

package gameconfig

import (
	"os"
	"path/filepath"
	"testing"
)

// The manager runs as its own user, so a file it writes belongs to that user
// with a mode nobody else can read. A config an operator created by hand as
// root is world-readable; replacing it through the manager is what corrects
// both, because the write creates a new file and renames it into place.
func TestWriteFileAtomicCorrectsTheModeOfAFileMadeByHand(t *testing.T) {
	cfg := customConfig(t)
	if err := os.MkdirAll(Dir(cfg), 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(Dir(cfg), "ModHttp.ini")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Fatalf("content = %q, %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o007 != 0 {
		t.Errorf("mode %v still lets everyone read the file", mode)
	}
}
