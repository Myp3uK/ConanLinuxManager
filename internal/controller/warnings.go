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
		players, err := c.Players(ctx, cfg.ID)
		return len(players), err
	}
	first := true
	say := func(text string) error {
		if _, err := c.rcon(ctx, cfg, cfg.Warnings.CommandLine(text)); err != nil {
			return err
		}
		if first {
			first = false
			c.emit(events.PlayersWarned, cfg, "Players were warned before the "+action, map[string]string{"action": action, "message": text})
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
