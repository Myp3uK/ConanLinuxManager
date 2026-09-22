package serverstats

import (
	"bufio"
	"io"
	"os"
	"strings"
	"time"
)

// startupMarker is the line the game writes once it has loaded and accepts
// players:
//
//	[2026.09.22-06.14.11:622][  0]LogServerStats: Startup report. StartupTime=21 Name=... QueryPort=37115 ...
const startupMarker = "LogServerStats: Startup report."

// Startup looks for the startup report in the game log. It reads on from
// where it stopped, and from the start again when the game rewrites the log
// at its next start.
type Startup struct {
	Path   string
	offset int64
}

// Find returns the time of a startup report written at or after since. The
// log of the previous run is still there until the game replaces it, so an
// older report does not count.
func (s *Startup) Find(since time.Time) (time.Time, bool, error) {
	f, err := os.Open(s.Path)
	if err != nil {
		return time.Time{}, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return time.Time{}, false, err
	}
	if info.Size() < s.offset {
		s.offset = 0
	}
	if _, err := f.Seek(s.offset, io.SeekStart); err != nil {
		return time.Time{}, false, err
	}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			// A line still being written is read whole next time.
			return time.Time{}, false, nil
		}
		s.offset += int64(len(line))
		if !strings.Contains(line, startupMarker) {
			continue
		}
		// The game's clock and the start time are both UTC; a few seconds
		// of slack cover how each is rounded.
		if at := logTime(line); !at.IsZero() && !at.Before(since.Add(-5*time.Second)) {
			return at, true, nil
		}
	}
}
