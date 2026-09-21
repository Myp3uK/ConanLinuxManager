package logreader

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// The backward reader must agree with a plain split for every chunk boundary
// and line ending.
func TestTailMatchesAForwardRead(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&b, "[2026.09.21-09.%02d] LogNet: строка %d %s\r\n", i%60, i, strings.Repeat("x", i%97))
	}
	b.WriteString("no newline at the end")
	content := b.String()
	path := filepath.Join(t.TempDir(), "ConanSandbox.log")
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	all := strings.Split(strings.ReplaceAll(content, "\r", ""), "\n")
	for _, limit := range []int{1, 2, 700, 5001, 10000} {
		got, err := (Reader{}).Tail(path, limit)
		if err != nil {
			t.Fatal(err)
		}
		want := all
		if limit < len(all) {
			want = all[len(all)-limit:]
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("limit %d: got %d lines, first %q", limit, len(got), got[0])
		}
	}
}

func TestSearchFiltersWithoutRegardToCase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	content := "LogNet: Join succeeded: Конан\nLogTemp: tick\nLogNet: join succeeded: Valeria\nLogTemp: tick\n"
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	got, err := (Reader{}).Search(path, 10, "JOIN SUCCEEDED")
	if err != nil || len(got) != 2 || !strings.HasSuffix(got[0], "Конан") || !strings.HasSuffix(got[1], "Valeria") {
		t.Fatalf("%q %v", got, err)
	}
	if got, _ := (Reader{}).Search(path, 10, "nothing"); got == nil || len(got) != 0 {
		t.Fatalf("no match should be an empty list, got %#v", got)
	}
}
