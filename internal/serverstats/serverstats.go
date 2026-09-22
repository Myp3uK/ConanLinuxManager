// Package serverstats reads how a running server is doing: the status report
// the game writes to its log every minute, and what Linux reports about the
// process and the host.
package serverstats

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Game is one "LogServerStats: Status report" line. Checked against a live
// UE5 server: Mem is the host's available and total memory, then the
// process's used and peak memory (the used one matches VmRSS); CPU is the
// process's share of the whole machine, then of one core; FPS is the minimum,
// average and maximum over the minute.
type Game struct {
	At            time.Time `json:"at"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	HostAvailable uint64    `json:"host_available_bytes"`
	HostTotal     uint64    `json:"host_total_bytes"`
	ProcessMemory uint64    `json:"process_bytes"`
	ProcessPeak   uint64    `json:"process_peak_bytes"`
	CPUMachine    float64   `json:"cpu_machine_percent"`
	CPUCore       float64   `json:"cpu_core_percent"`
	Players       int       `json:"players"`
	FPSMin        float64   `json:"fps_min"`
	FPSAvg        float64   `json:"fps_avg"`
	FPSMax        float64   `json:"fps_max"`
	// NPCs is the total of NPC_AILOD, the NPCs of every AI detail level.
	NPCs int `json:"npcs"`
}

const statusMarker = "LogServerStats: Status report."

// ParseGame reads one log line; ok is false for any other line.
func ParseGame(line string) (Game, bool) {
	i := strings.Index(line, statusMarker)
	if i < 0 {
		return Game{}, false
	}
	var g Game
	g.At = logTime(line[:i])
	for _, field := range strings.Fields(line[i+len(statusMarker):]) {
		key, value, found := strings.Cut(field, "=")
		if !found {
			continue
		}
		parts := strings.Split(value, ":")
		switch key {
		case "Uptime":
			g.UptimeSeconds, _ = strconv.ParseInt(value, 10, 64)
		case "Mem":
			if len(parts) == 4 {
				g.HostAvailable, g.HostTotal, g.ProcessMemory, g.ProcessPeak = unsigned(parts[0]), unsigned(parts[1]), unsigned(parts[2]), unsigned(parts[3])
			}
		case "CPU":
			if len(parts) == 2 {
				g.CPUMachine, g.CPUCore = float(parts[0]), float(parts[1])
			}
		case "Players":
			g.Players, _ = strconv.Atoi(value)
		case "FPS":
			if len(parts) == 3 {
				g.FPSMin, g.FPSAvg, g.FPSMax = float(parts[0]), float(parts[1]), float(parts[2])
			}
		case "NPC_AILOD":
			for _, p := range parts {
				n, _ := strconv.Atoi(p)
				g.NPCs += n
			}
		}
	}
	return g, true
}

// logTime reads the "[2026.09.22-02.17.45:854]" prefix; the game writes UTC.
func logTime(prefix string) time.Time {
	start := strings.IndexByte(prefix, '[')
	end := strings.IndexByte(prefix, ']')
	if start < 0 || end <= start {
		return time.Time{}
	}
	// Go takes fractional seconds only after a dot.
	stamp := prefix[start+1 : end]
	if i := strings.LastIndexByte(stamp, ':'); i >= 0 {
		stamp = stamp[:i] + "." + stamp[i+1:]
	}
	t, err := time.Parse("2006.01.02-15.04.05.000", stamp)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func unsigned(s string) uint64 { n, _ := strconv.ParseUint(s, 10, 64); return n }
func float(s string) float64   { f, _ := strconv.ParseFloat(s, 64); return f }

// tailBytes is how much of the log end is searched. The report comes once a
// minute; a busy log writes far less than this in between.
const tailBytes = 1 << 20

// ErrNoReport means the log end holds no status report yet.
var ErrNoReport = errors.New("no status report in the log yet")

// LastGame returns the newest status report in the log at path.
func LastGame(path string) (Game, error) {
	f, err := os.Open(path)
	if err != nil {
		return Game{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Game{}, err
	}
	offset := info.Size() - tailBytes
	if offset < 0 {
		offset = 0
	}
	buf := make([]byte, info.Size()-offset)
	if _, err := f.ReadAt(buf, offset); err != nil && !errors.Is(err, io.EOF) {
		return Game{}, err
	}
	for len(buf) > 0 {
		i := bytes.LastIndexByte(buf, '\n')
		line := buf[i+1:]
		if bytes.Contains(line, []byte(statusMarker)) {
			if g, ok := ParseGame(strings.TrimRight(string(line), "\r")); ok {
				return g, nil
			}
		}
		if i < 0 {
			break
		}
		buf = buf[:i]
	}
	return Game{}, ErrNoReport
}
