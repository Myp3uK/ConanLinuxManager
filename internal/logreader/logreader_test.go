package logreader

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	lines, err := (Reader{}).Tail(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "two" || lines[1] != "three" {
		t.Fatalf("unexpected lines: %#v", lines)
	}
}
