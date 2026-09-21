package unitservice

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeSpecFile(t *testing.T, dir string, spec Spec, mode os.FileMode) string {
	t.Helper()
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, spec.ServerID+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func validSpec(id string) Spec {
	return Spec{Version: 1, ServerID: id, Executable: "/bin/true", Args: []string{"x"}, Dir: "/tmp", LogPath: "/tmp/log"}
}

func TestReadSpecAcceptsOwnPrivateFile(t *testing.T) {
	dir := t.TempDir()
	path := writeSpecFile(t, dir, validSpec("alpha"), 0o640)
	if _, err := readSpec(path, "alpha"); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
}

// The runner executes whatever the specification names, so a file another
// user could have changed must never be trusted.
func TestReadSpecRejectsWritableFiles(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range []os.FileMode{0o660, 0o646, 0o666} {
		path := writeSpecFile(t, dir, validSpec("alpha"), mode)
		if _, err := readSpec(path, "alpha"); err == nil {
			t.Errorf("mode %v accepted", mode)
		}
	}
}

func TestReadSpecRejectsAnotherServersSpec(t *testing.T) {
	dir := t.TempDir()
	path := writeSpecFile(t, dir, validSpec("beta"), 0o640)
	if _, err := readSpec(path, "alpha"); err == nil {
		t.Fatal("spec for another server accepted")
	}
}

func TestReadSpecRejectsSymlinksAndRelativePaths(t *testing.T) {
	dir := t.TempDir()
	target := writeSpecFile(t, dir, validSpec("alpha"), 0o640)
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readSpec(link, "alpha"); err == nil {
		t.Error("symlinked spec accepted")
	}
	relative := validSpec("gamma")
	relative.Executable = "bin/game"
	path := writeSpecFile(t, dir, relative, 0o640)
	if _, err := readSpec(path, "gamma"); err == nil {
		t.Error("relative executable accepted")
	}
}
