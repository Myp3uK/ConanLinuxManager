package sessionlock

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContentionAliasAndRelease(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "install")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	l, err := Acquire(dir, alias)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if other, err := Acquire(alias); err == nil {
		other.Close()
		t.Fatal("alias bypassed lease")
	}
	l.Close()
	other, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	other.Close()
}
