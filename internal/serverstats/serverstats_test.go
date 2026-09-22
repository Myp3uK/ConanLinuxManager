package serverstats

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A line from the live UE5 server on node2.
const liveLine = "[2026.09.22-02.17.45:854][838]LogServerStats: Status report. Uptime=18900 Mem=3867103232:16700375040:9419333632:11540819968 CPU=9.386333:56.318001 Players=0 FPS=26.84:33.45:38.49 NPC_AILOD=0:0:0:13182 B_AILOD=0:0:0:32 P_AILOD=0:0:0:334"

func TestParseGame(t *testing.T) {
	g, ok := ParseGame(liveLine)
	if !ok {
		t.Fatal("line not recognised")
	}
	want := Game{At: time.Date(2026, 9, 22, 2, 17, 45, 854e6, time.UTC), UptimeSeconds: 18900,
		HostAvailable: 3867103232, HostTotal: 16700375040, ProcessMemory: 9419333632, ProcessPeak: 11540819968,
		CPUMachine: 9.386333, CPUCore: 56.318001, Players: 0, FPSMin: 26.84, FPSAvg: 33.45, FPSMax: 38.49, NPCs: 13182}
	if g != want {
		t.Fatalf("got %+v\nwant %+v", g, want)
	}
	if _, ok := ParseGame("[2026.09.21-21.02.37:394][  0]LogServerStats: Startup report. StartupTime=21"); ok {
		t.Fatal("startup report taken for a status report")
	}
}

func TestLastGameTakesTheNewestReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ConanSandbox.log")
	older := strings.Replace(liveLine, "Players=0", "Players=3", 1)
	content := older + "\r\nLogNet: something\r\n" + liveLine + "\r\nLogNet: after\r\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := LastGame(path)
	if err != nil || g.UptimeSeconds != 18900 || g.Players != 0 {
		t.Fatalf("got %+v, %v", g, err)
	}
	if err := os.WriteFile(path, []byte("LogNet: nothing yet\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LastGame(path); err != ErrNoReport {
		t.Fatalf("err = %v, want ErrNoReport", err)
	}
}
