package controller

import (
	"context"
	"strconv"
	"time"

	"github.com/example/conan-manager/internal/events"
	"github.com/example/conan-manager/internal/server"
)

// warnPlayers announces an operator's restart or stop, or an update, as the
// message kind says, and returns when the server may go down. With countdown
// it says the message at each warning mark while anyone is online and returns
// early once nobody is; without, or when nobody is online from the start, it
// says the message once, without minutes. It never fails the action: a
// server that cannot be reached over RCON goes down as it would without
// warnings.
func (c *Controller) warnPlayers(ctx context.Context, cfg server.Config, kind string, vars map[string]string, countdown bool) error {
	count := func() (int, error) {
		players, err := c.Players(ctx, cfg.ID, true)
		return len(players), err
	}
	say := func(minutes int, inGame bool) error {
		all := map[string]string{}
		for k, v := range vars {
			all[k] = v
		}
		if minutes > 0 {
			all["minutes"] = strconv.Itoa(minutes)
		}
		return c.message(ctx, cfg, kind, all, inGame)
	}
	marks := cfg.Warnings.Marks()
	if !countdown {
		marks = nil
	}
	err := announce(ctx, marks, count, say, sleepCtx)
	return c.warningResult(ctx, cfg, kind, err)
}

func (c *Controller) warningResult(ctx context.Context, cfg server.Config, kind string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		c.Logger.Warn("player warning failed; continuing", "server", cfg.ID, "kind", kind, "error", err)
		c.emit(events.RconFailed, cfg, "Players could not be warned over RCON", map[string]string{"kind": kind, "error": err.Error()})
	}
	return nil
}

// announce is the timing of warnPlayers, apart from the game. A failed
// warning is returned at the end and never shortens the countdown.
func announce(ctx context.Context, marks []int, count func() (int, error), say func(minutes int, inGame bool) error, wait func(context.Context, time.Duration) error) error {
	var failed error
	note := func(err error) {
		if err != nil && failed == nil {
			failed = err
		}
	}
	said := false
	for i, minutes := range marks {
		online, err := count()
		note(err)
		if online == 0 {
			break
		}
		note(say(minutes, true))
		said = true
		next := 0
		if i+1 < len(marks) {
			next = marks[i+1]
		}
		if err := wait(ctx, time.Duration(minutes-next)*time.Minute); err != nil {
			return err
		}
	}
	if !said {
		// The action happens now: one message, to whoever is still there.
		online, err := count()
		note(err)
		note(say(0, online > 0))
	}
	return failed
}

// warnUntil announces a scheduled restart or stop fixed in time: each
// message goes out its minutes before due, and it returns at due whether
// anyone is online or not, so the action happens exactly when it was set
// for. Discord gets every message; the game only while someone is online.
func (c *Controller) warnUntil(ctx context.Context, cfg server.Config, action string, due time.Time) error {
	count := func() (int, error) {
		players, err := c.Players(ctx, cfg.ID, true)
		return len(players), err
	}
	say := func(minutes int, inGame bool) error {
		return c.message(ctx, cfg, server.MsgScheduledRestart, map[string]string{"minutes": strconv.Itoa(minutes), "action": action}, inGame)
	}
	marks := cfg.Warnings.Marks()
	err := countdown(ctx, marks, due, time.Now, count, say, sleepCtx)
	return c.warningResult(ctx, cfg, server.MsgScheduledRestart, err)
}

// countdown is the timing of warnUntil, apart from the game. A mark already
// more than half a minute past, as after a late claim, is skipped. A failed
// warning is returned at the end and never moves the action.
func countdown(ctx context.Context, marks []int, due time.Time, now func() time.Time,
	count func() (int, error), say func(minutes int, inGame bool) error, wait func(context.Context, time.Duration) error) error {
	var failed error
	note := func(err error) {
		if err != nil && failed == nil {
			failed = err
		}
	}
	for _, minutes := range marks {
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
		note(err)
		note(say(minutes, err == nil && online > 0))
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
