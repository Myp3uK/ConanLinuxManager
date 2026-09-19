package watchdog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIncrementalHangAndOldMessages(t *testing.T) {
	p := filepath.Join(t.TempDir(), "game.log")
	if err := os.WriteFile(p, []byte("Hang detected on GameThread\n"), 0600); err != nil {
		t.Fatal(err)
	}
	w := New([]string{p})
	if reason, err := w.Check(); reason != "" || err != nil {
		t.Fatalf("old log: %s %v", reason, err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for i, text := range []string{"LogCore: Error: Hang detected on ", "GameThread (thread hasn't sent a heartbeat for 30.00 seconds):"} {
		if _, err := f.WriteString(text); err != nil {
			t.Fatal(err)
		}
		reason, err := w.Check()
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && reason != "" {
			t.Fatal(reason)
		}
		if i == 1 && !strings.HasPrefix(reason, "game_thread_hang:") {
			t.Fatal(reason)
		}
	}
}

func TestFloodAndOversizedLog(t *testing.T) {
	for _, size := range []int64{ReadBudget + 1, MaxLogBytes, 15 << 30} {
		p := filepath.Join(t.TempDir(), "game.log")
		w := New([]string{p})
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		f.Close()
		reason, err := w.Check()
		if err != nil || reason == "" {
			t.Fatalf("size %d: %s %v", size, reason, err)
		}
	}
}
