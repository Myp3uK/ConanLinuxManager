package controller

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/example/conan-manager/internal/events"
	"github.com/example/conan-manager/internal/server"
)

// A delayed restart happens a fixed time after it is set, with the same
// countdown as a scheduled one: an operator's «restart in 15 minutes», or the
// restart that installs an update found while the server runs. There is at
// most one per server; any stop of the server ends it, since the server then
// restarts, or stays down, by other means.

type pendingRun struct {
	server.PendingRestart
	cancel context.CancelFunc
}

// ErrPlannedSooner refuses a delayed restart that a scheduled restart or stop
// would come before.
type ErrPlannedSooner struct {
	At     time.Time
	Action string
}

func (e ErrPlannedSooner) Error() string {
	return fmt.Sprintf("a scheduled %s comes sooner, at %s UTC", e.Action, e.At.UTC().Format("15:04"))
}

// ErrAlreadyPending refuses a second delayed restart.
var ErrAlreadyPending = fmt.Errorf("a delayed restart is already set")

// Planned is the next restart or stop the server will go through, whatever
// set it.
type Planned struct {
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	// Source is schedule, manual or update.
	Source string `json:"source"`
}

// Pending returns the server's delayed restart, or nil.
func (c *Controller) Pending(id string) *server.PendingRestart {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if run := c.pending[id]; run != nil {
		p := run.PendingRestart
		return &p
	}
	return nil
}

// NextPlanned is the soonest of the delayed restart, a scheduled run whose
// warnings are under way and the schedule's next run.
func (c *Controller) NextPlanned(ctx context.Context, id string) *Planned {
	var next *Planned
	consider := func(p Planned) {
		if !p.At.IsZero() && (next == nil || p.At.Before(next.At)) {
			next = &p
		}
	}
	if p := c.Pending(id); p != nil {
		consider(Planned{At: p.DueAt, Action: "restart", Source: p.Reason})
	}
	if p, ok := c.scheduledPlan(ctx, id); ok {
		consider(p)
	}
	return next
}

// scheduledPlan is the next scheduled restart or stop. The run under way
// counts: its schedule has already moved on to the following one.
func (c *Controller) scheduledPlan(ctx context.Context, id string) (Planned, bool) {
	c.pendingMu.Lock()
	running, ok := c.scheduledDue[id]
	c.pendingMu.Unlock()
	if ok {
		return running, true
	}
	sch, err := c.Repo.GetSchedule(ctx, id)
	if err != nil || !sch.Enabled || (sch.Action != "restart" && sch.Action != "stop") || sch.NextRun.IsZero() {
		return Planned{}, false
	}
	return Planned{At: sch.NextRun, Action: sch.Action, Source: "schedule"}, true
}

// markScheduled records a scheduled run under way until the returned
// function is called.
func (c *Controller) markScheduled(id string, p Planned) func() {
	c.pendingMu.Lock()
	if c.scheduledDue == nil {
		c.scheduledDue = map[string]Planned{}
	}
	c.scheduledDue[id] = p
	c.pendingMu.Unlock()
	return func() {
		c.pendingMu.Lock()
		delete(c.scheduledDue, id)
		c.pendingMu.Unlock()
	}
}

// DelayRestart is the operator's restart after the server's delay.
func (c *Controller) DelayRestart(ctx context.Context, id string) (server.PendingRestart, error) {
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil {
		return server.PendingRestart{}, err
	}
	return c.planRestart(ctx, cfg, server.PendingManual, "")
}

// planRestart sets a delayed restart and starts its countdown.
func (c *Controller) planRestart(ctx context.Context, cfg server.Config, reason, detail string) (server.PendingRestart, error) {
	if state := c.Lifecycle(cfg.ID).State; state != "running" {
		return server.PendingRestart{}, fmt.Errorf("server %q is %s, not running", cfg.ID, state)
	}
	now := time.Now().UTC().Truncate(time.Second)
	p := server.PendingRestart{ServerID: cfg.ID, Reason: reason, Detail: detail, CreatedAt: now,
		DueAt: now.Add(time.Duration(cfg.Warnings.DelayMinutes()) * time.Minute)}
	if s, ok := c.scheduledPlan(ctx, cfg.ID); ok && !s.At.After(p.DueAt) {
		return server.PendingRestart{}, ErrPlannedSooner{At: s.At, Action: s.Action}
	}
	runCtx, cancel := context.WithCancel(context.Background())
	c.pendingMu.Lock()
	if c.pending == nil {
		c.pending = map[string]*pendingRun{}
	}
	if c.pending[cfg.ID] != nil {
		c.pendingMu.Unlock()
		cancel()
		return server.PendingRestart{}, ErrAlreadyPending
	}
	c.pending[cfg.ID] = &pendingRun{PendingRestart: p, cancel: cancel}
	c.pendingMu.Unlock()
	// Kept in memory regardless: a lost row only loses the restart if the
	// manager restarts before it.
	if err := c.Repo.SetPendingRestart(ctx, p); err != nil {
		c.Logger.Warn("delayed restart not stored", "server", cfg.ID, "error", err)
	}
	go c.runPending(runCtx, cfg, p, true)
	return p, nil
}

// CancelPending drops the server's delayed restart at the operator's word.
func (c *Controller) CancelPending(ctx context.Context, id string) error {
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil {
		return err
	}
	p := c.Pending(id)
	if p == nil {
		return fmt.Errorf("server %q has no delayed restart", id)
	}
	c.dropPending(id)
	if err := c.message(ctx, cfg, server.MsgRestartCancelled, nil, true); err != nil {
		c.Logger.Warn("cancel not said in game", "server", id, "error", err)
	}
	return nil
}

// dropPending ends a delayed restart, if there is one.
func (c *Controller) dropPending(id string) {
	c.pendingMu.Lock()
	run := c.pending[id]
	delete(c.pending, id)
	c.pendingMu.Unlock()
	if run == nil {
		return
	}
	run.cancel()
	if err := c.Repo.DeletePendingRestart(context.Background(), id); err != nil {
		c.Logger.Warn("delayed restart not removed from the database", "server", id, "error", err)
	}
}

// takePending removes the delayed restart p when its time comes. False means
// it was dropped or replaced meanwhile.
func (c *Controller) takePending(p server.PendingRestart) bool {
	c.pendingMu.Lock()
	run := c.pending[p.ServerID]
	mine := run != nil && run.CreatedAt.Equal(p.CreatedAt)
	if mine {
		delete(c.pending, p.ServerID)
	}
	c.pendingMu.Unlock()
	if mine {
		_ = c.Repo.DeletePendingRestart(context.Background(), p.ServerID)
	}
	return mine
}

// runPending counts a delayed restart down and restarts the server on time.
// fresh is false for one resumed after a restart of the manager, whose first
// message has gone out already.
func (c *Controller) runPending(ctx context.Context, cfg server.Config, p server.PendingRestart, fresh bool) {
	// The countdown uses the scheduled restart's messages.
	vars := map[string]string{}
	if p.Reason == server.PendingUpdate {
		vars["reason"] = "обновление"
	}
	delay := int(p.DueAt.Sub(p.CreatedAt).Round(time.Minute) / time.Minute)
	if fresh && p.Reason == server.PendingManual {
		// The operator's restart is announced at once with its full delay;
		// an update's is announced by the update message.
		err := c.message(ctx, cfg, server.MsgDelayedRestart, map[string]string{"minutes": strconv.Itoa(delay)}, true)
		if c.warningResult(ctx, cfg, server.MsgDelayedRestart, err) != nil {
			return
		}
	}
	var marks []int
	for _, m := range cfg.Warnings.Marks() {
		if m < delay {
			marks = append(marks, m)
		}
	}
	if c.warnUntil(ctx, cfg, server.MsgScheduledRestart, vars, marks, p.DueAt) != nil {
		return
	}
	if !c.takePending(p) {
		return
	}
	// A server that went down, or was restarted by other means, meanwhile
	// has nothing left to restart for.
	if c.Lifecycle(cfg.ID).State != "running" || c.Snapshot(cfg.ID).StartedAt.After(p.CreatedAt) {
		return
	}
	if err := c.Restart(context.Background(), cfg.ID); err != nil {
		c.emit(events.ServerStartFailed, cfg, "Delayed restart failed", map[string]string{"error": err.Error()})
	}
}

// ResumePending takes up the delayed restarts stored before the manager
// restarted. One whose time passed while the manager was down is dropped:
// players were told a time that is gone.
func (c *Controller) ResumePending(ctx context.Context) {
	list, err := c.Repo.PendingRestarts(ctx)
	if err != nil {
		c.Logger.Error("load delayed restarts", "error", err)
		return
	}
	for _, p := range list {
		cfg, err := c.Repo.Get(ctx, p.ServerID)
		if err != nil || time.Now().After(p.DueAt.Add(time.Minute)) || c.Lifecycle(p.ServerID).State != "running" {
			_ = c.Repo.DeletePendingRestart(ctx, p.ServerID)
			c.Logger.Info("delayed restart dropped after a manager restart", "server", p.ServerID, "due", p.DueAt)
			continue
		}
		runCtx, cancel := context.WithCancel(context.Background())
		c.pendingMu.Lock()
		if c.pending == nil {
			c.pending = map[string]*pendingRun{}
		}
		c.pending[p.ServerID] = &pendingRun{PendingRestart: p, cancel: cancel}
		c.pendingMu.Unlock()
		c.Logger.Info("delayed restart resumed", "server", p.ServerID, "due", p.DueAt)
		go c.runPending(runCtx, cfg, p, false)
	}
}

// online counts the players over RCON.
func (c *Controller) online(ctx context.Context, cfg server.Config) (int, error) {
	players, err := c.Players(ctx, cfg.ID, true)
	return len(players), err
}

func withVar(vars map[string]string, key, value string) map[string]string {
	all := make(map[string]string, len(vars)+1)
	for k, v := range vars {
		all[k] = v
	}
	all[key] = value
	return all
}

// minutesUntil rounds the time left up to whole minutes, as players read it.
func minutesUntil(at time.Time) string {
	left := time.Until(at)
	if left < time.Minute {
		return "1"
	}
	return strconv.Itoa(int((left + time.Minute - 1) / time.Minute))
}
