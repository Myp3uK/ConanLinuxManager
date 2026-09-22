package controller

import (
	"context"
	"time"

	"github.com/example/conan-manager/internal/events"
	"github.com/example/conan-manager/internal/server"
)

// warnPlayers tells connected players that the server goes down for action,
// at each configured mark, and returns when the time is up. It returns at
// once when nobody is online, and early when the last player leaves. It never
// fails the action: a server that cannot be reached over RCON is taken down on
// schedule, as it would be without warnings.
func (c *Controller) warnPlayers(ctx context.Context, cfg server.Config, action string) error {
	if !cfg.Warnings.Enabled() {
		return nil
	}
	count := func() (int, error) {
		// Each mark asks the game: the cached list may be a minute old.
		players, err := c.Players(ctx, cfg.ID, true)
		return len(players), err
	}
	first := true
	say := func(text string) error {
		if _, err := c.rcon(ctx, cfg, cfg.Warnings.CommandLine(text)); err != nil {
			return err
		}
		if first {
			first = false
			// The warning itself is what a Discord channel for players shows.
			c.emit(events.PlayersWarned, cfg, "Players were warned before the "+action, map[string]string{"action": action, "announce": text})
		}
		return nil
	}
	err := announce(ctx, cfg.Warnings, action, count, say, sleepCtx)
	if err != nil && ctx.Err() == nil {
		c.Logger.Warn("player warning failed; continuing", "server", cfg.ID, "action", action, "error", err)
		c.emit(events.RconFailed, cfg, "Players could not be warned over RCON", map[string]string{"action": action, "error": err.Error()})
		return nil
	}
	return ctx.Err()
}

// announce is the timing of warnPlayers, apart from the game.
func announce(ctx context.Context, w server.Warnings, action string, count func() (int, error), say func(string) error, wait func(context.Context, time.Duration) error) error {
	marks := w.Marks()
	for i, minutes := range marks {
		online, err := count()
		if err != nil {
			return err
		}
		if online == 0 {
			return nil
		}
		if err := say(w.Message(action, minutes)); err != nil {
			return err
		}
		next := 0
		if i+1 < len(marks) {
			next = marks[i+1]
		}
		if err := wait(ctx, time.Duration(minutes-next)*time.Minute); err != nil {
			return err
		}
	}
	return nil
}

// warnUntil is warnPlayers for an action fixed in time: each warning goes out
// its minutes before due, and it returns at due whether anyone is online or
// not, so a scheduled restart happens exactly when it was set for.
func (c *Controller) warnUntil(ctx context.Context, cfg server.Config, action string, due time.Time) error {
	count := func() (int, error) {
		if !cfg.Warnings.Enabled() {
			return 0, nil
		}
		players, err := c.Players(ctx, cfg.ID, true)
		return len(players), err
	}
	first := true
	say := func(text string) error {
		if _, err := c.rcon(ctx, cfg, cfg.Warnings.CommandLine(text)); err != nil {
			return err
		}
		if first {
			first = false
			c.emit(events.PlayersWarned, cfg, "Players were warned before the "+action, map[string]string{"action": action, "announce": text})
		}
		return nil
	}
	err := countdown(ctx, cfg.Warnings, action, due, time.Now, count, say, sleepCtx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		c.Logger.Warn("player warning failed; the action stays on time", "server", cfg.ID, "action", action, "error", err)
		c.emit(events.RconFailed, cfg, "Players could not be warned over RCON", map[string]string{"action": action, "error": err.Error()})
	}
	return nil
}

// countdown is the timing of warnUntil, apart from the game. A mark already
// more than half a minute past, as after a late claim, is skipped. A failed
// warning is returned at the end and never moves the action.
func countdown(ctx context.Context, w server.Warnings, action string, due time.Time, now func() time.Time,
	count func() (int, error), say func(string) error, wait func(context.Context, time.Duration) error) error {
	var failed error
	for _, minutes := range w.Marks() {
		at := due.Add(-time.Duration(minutes) * time.Minute)
		late := now().Sub(at)
		if late > 30*time.Second {
			continue
		}
		if late < 0 {
			if err := wait(ctx, -late); err != nil {
				return err
			}
		}
		online, err := count()
		if err == nil && online > 0 {
			err = say(w.Message(action, minutes))
		}
		if err != nil && failed == nil {
			failed = err
		}
	}
	if left := due.Sub(now()); left > 0 {
		if err := wait(ctx, left); err != nil {
			return err
		}
	}
	return failed
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
