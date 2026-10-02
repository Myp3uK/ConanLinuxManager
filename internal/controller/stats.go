package controller

import (
	"context"
	"path/filepath"
	"time"

	"github.com/example/conan-manager/internal/serverstats"
)

const (
	statsInterval = 30 * time.Second
	// statsHistory is an hour of samples.
	statsHistory = 120
)

// StatsPoint is one sample of the history the interface draws.
type StatsPoint struct {
	At         time.Time `json:"at"`
	CPUPercent float64   `json:"cpu"`
	RSS        uint64    `json:"rss"`
	FPS        float64   `json:"fps"`
	Players    int       `json:"players"`
}

// Stats is what a running server reports and what Linux says about it.
type Stats struct {
	Game    *serverstats.Game    `json:"game,omitempty"`
	Process *serverstats.Process `json:"process,omitempty"`
	Host    serverstats.Host     `json:"host"`
	History []StatsPoint         `json:"history"`
}

type statsState struct {
	pid     int
	game    *serverstats.Game
	process *serverstats.Process
	history []StatsPoint
}

// hostCPUWindow is the shortest gap between two CPU readings that is worth
// dividing by: closer together, the counters are mostly rounding, and the
// answer from the previous pair is the better one.
const hostCPUWindow = 2 * time.Second

// HostStats is the machine as a whole: its memory, its load average and how
// busy its cores were since the previous call. Every caller shares one pair of
// readings, so the figure is the same everywhere and asking more often only
// shortens the window it covers.
func (c *Controller) HostStats() serverstats.Host {
	host := serverstats.SampleHost()
	now, err := serverstats.SampleCPUTotal()
	c.hostMu.Lock()
	defer c.hostMu.Unlock()
	if err == nil {
		if now.At.Sub(c.hostCPU.At) >= hostCPUWindow || c.hostCPU.At.IsZero() {
			if busy, ok := now.BusySince(c.hostCPU); ok {
				c.hostBusy, c.hostKnown = busy, true
			}
			c.hostCPU = now
		}
	}
	host.CPUPercent, host.CPUKnown = c.hostBusy, c.hostKnown
	return host
}

// Stats returns the latest sample of a server; a stopped server has only the
// host part.
func (c *Controller) Stats(id string) Stats {
	// The host is read before the lock: it touches the filesystem, and the
	// stats lock is held by every sample of every server.
	out := Stats{Host: c.HostStats(), History: []StatsPoint{}}
	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	if s := c.stats[id]; s != nil {
		out.Game, out.Process = s.game, s.process
		out.History = append(out.History, s.history...)
	}
	return out
}

// WatchStats samples every running server until ctx ends.
func (c *Controller) WatchStats(ctx context.Context) {
	ticker := time.NewTicker(statsInterval)
	defer ticker.Stop()
	for {
		c.sampleStats(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Controller) sampleStats(ctx context.Context) {
	configs, err := c.Repo.List(ctx)
	if err != nil {
		return
	}
	for _, cfg := range configs {
		pid := 0
		if c.Lifecycle(cfg.ID).State == "running" {
			pid = c.Snapshot(cfg.ID).PID
		}
		c.statsMu.Lock()
		if c.stats == nil {
			c.stats = map[string]*statsState{}
		}
		s := c.stats[cfg.ID]
		if pid == 0 {
			delete(c.stats, cfg.ID)
			c.statsMu.Unlock()
			continue
		}
		if s == nil || s.pid != pid {
			// A new process starts a new history.
			s = &statsState{pid: pid}
			c.stats[cfg.ID] = s
		}
		previous := s.process
		c.statsMu.Unlock()

		// Reading files happens outside the lock.
		proc, procErr := serverstats.SampleProcess(pid, previous)
		game, gameErr := serverstats.LastGame(filepath.Join(cfg.InstallDir, LogNames["game"]))

		c.statsMu.Lock()
		if procErr == nil {
			s.process = &proc
		}
		// A report from before this process started belongs to the last run.
		started := c.Snapshot(cfg.ID).StartedAt
		if gameErr == nil && (started.IsZero() || !game.At.Before(started.Add(-time.Minute))) {
			s.game = &game
		}
		if s.process != nil && previous != nil {
			point := StatsPoint{At: proc.At.UTC(), CPUPercent: proc.CPUPercent, RSS: proc.RSS}
			if s.game != nil {
				point.FPS, point.Players = s.game.FPSAvg, s.game.Players
			}
			s.history = append(s.history, point)
			if len(s.history) > statsHistory {
				s.history = s.history[len(s.history)-statsHistory:]
			}
		}
		c.statsMu.Unlock()
	}
}
