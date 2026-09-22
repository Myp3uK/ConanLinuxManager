package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type Schedule struct {
	ServerID        string    `json:"server_id"`
	Action          string    `json:"action"`
	Enabled         bool      `json:"enabled"`
	Mode            string    `json:"mode"`
	CronExpression  string    `json:"cron_expression,omitempty"`
	Timezone        string    `json:"timezone,omitempty"`
	IntervalSeconds int       `json:"interval_seconds"`
	DailyTime       string    `json:"daily_time,omitempty"`
	FailureCount    int       `json:"failure_count"`
	NextRun         time.Time `json:"next_run"`
	UpdatedAt       time.Time `json:"updated_at"`
	// DueAt is the time a claimed run is for. With a lead the run is claimed
	// before it, so the action can warn players and still happen on time.
	DueAt time.Time `json:"-"`
}

// Lead tells how long before its time a schedule is handed to Execute; the
// controller uses it to warn players ahead of a restart. It is capped at
// MaxLead.
type Lead func(Schedule) time.Duration

// MaxLead bounds a lead: the longest player warning is two hours.
const MaxLead = 2 * time.Hour

type Run struct {
	ID         int64     `json:"id"`
	ServerID   string    `json:"server_id"`
	Action     string    `json:"action"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// Claim is a schedule whose next_run has already been advanced, paired with the
// history row that records the attempt. Advancing before running is what keeps a
// crash mid-execution from replaying the same action on the next start.
type Claim struct {
	Schedule
	RunID int64
}

// PermanentError marks a failure that retrying cannot fix, such as an unknown
// action or a deleted server. Those skip the retry backoff and wait for the next
// natural occurrence instead of hammering the controller.
type PermanentError struct{ Err error }

func (e PermanentError) Error() string { return e.Err.Error() }

func (e PermanentError) Unwrap() error { return e.Err }

func Permanent(err error) error { return PermanentError{Err: err} }

func IsPermanent(err error) bool {
	var permanent PermanentError
	return errors.As(err, &permanent)
}

// Normalize fills in the defaults the API allows callers to omit.
func (s *Schedule) Normalize() {
	if s.Mode == "" {
		s.Mode = "interval"
	}
	if s.Mode == "cron" && s.Timezone == "" {
		s.Timezone = "UTC"
	}
	if s.Mode != "interval" {
		s.IntervalSeconds = 0
	}
}

func (s *Schedule) Validate() error {
	s.Normalize()
	if s.ServerID == "" {
		return fmt.Errorf("server_id is required")
	}
	if s.Action != "start" && s.Action != "stop" && s.Action != "restart" {
		return fmt.Errorf("action must be start, stop or restart")
	}
	switch s.Mode {
	case "interval":
		if s.IntervalSeconds < 60 {
			return fmt.Errorf("interval_seconds must be at least 60")
		}
	case "daily":
		if _, err := time.Parse("15:04", s.DailyTime); err != nil {
			return fmt.Errorf("daily_time must use HH:MM UTC")
		}
	case "cron":
		cron, err := ParseCron(s.CronExpression)
		if err != nil {
			return err
		}
		location, err := time.LoadLocation(s.Timezone)
		if err != nil {
			return fmt.Errorf("invalid timezone: %w", err)
		}
		// Reject specifications that can never match, such as 30 February.
		if _, err := cron.Next(time.Now().UTC(), location); err != nil {
			return err
		}
	default:
		return fmt.Errorf("mode must be interval, daily or cron")
	}
	if s.NextRun.IsZero() {
		return fmt.Errorf("next_run is required")
	}
	return nil
}

// Store is the persistence the runner needs. Claiming, advancing and recording
// are separate operations so a failure in one is visible in the history.
type Store interface {
	// ClaimDue atomically advances every schedule due by now, or by now plus
	// its lead, and opens a history row.
	ClaimDue(ctx context.Context, now time.Time, lead Lead) ([]Claim, error)
	// CompleteRun closes a history row with its outcome.
	CompleteRun(ctx context.Context, runID int64, finished time.Time, runErr error) error
	// Retry moves next_run earlier for a transient failure and stores the count.
	Retry(ctx context.Context, serverID string, at time.Time, failures int) error
	// ClearFailures resets the retry counter after a success or a permanent stop.
	ClearFailures(ctx context.Context, serverID string) error
	// MarkInterrupted closes history rows left running by a previous process.
	MarkInterrupted(ctx context.Context) (int, error)
}

type Runner struct {
	Schedules  Store
	Execute    func(context.Context, Schedule) error
	Lead       Lead
	RetryBase  time.Duration
	MaxRetries int
	Logger     *slog.Logger

	wg       sync.WaitGroup
	mu       sync.Mutex
	inflight map[string]bool
}

func (r *Runner) defaults() {
	if r.RetryBase <= 0 {
		r.RetryBase = time.Minute
	}
	if r.MaxRetries <= 0 {
		r.MaxRetries = 3
	}
	if r.Logger == nil {
		r.Logger = slog.New(slog.DiscardHandler)
	}
	if r.inflight == nil {
		r.inflight = make(map[string]bool)
	}
}

// Run ticks until the context is cancelled, then waits for running actions so a
// stop or restart is never abandoned half-way through shutdown.
func (r *Runner) Run(ctx context.Context) {
	r.defaults()
	if count, err := r.Schedules.MarkInterrupted(ctx); err != nil {
		r.Logger.Error("close interrupted scheduler runs", "error", err)
	} else if count > 0 {
		r.Logger.Warn("closed interrupted scheduler runs", "count", count)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.wg.Wait()
			return
		case now := <-ticker.C:
			if err := r.RunOnce(ctx, now.UTC()); err != nil {
				r.Logger.Error("scheduler tick", "error", err)
			}
		}
	}
}

// Wait blocks until every started action has finished.
func (r *Runner) Wait() { r.wg.Wait() }

// RunOnce claims everything due and starts each action in its own goroutine, so
// a long restart never delays the other servers' schedules.
func (r *Runner) RunOnce(ctx context.Context, now time.Time) error {
	r.defaults()
	claims, err := r.Schedules.ClaimDue(ctx, now.UTC(), r.Lead)
	if err != nil {
		return err
	}
	for _, claim := range claims {
		if !r.begin(claim.ServerID) {
			// next_run already moved on, so record the overlap instead of
			// silently queueing a second action for the same server.
			overlap := fmt.Errorf("previous %s for server %q is still running", claim.Action, claim.ServerID)
			if err := r.Schedules.CompleteRun(ctx, claim.RunID, time.Now().UTC(), Permanent(overlap)); err != nil {
				r.Logger.Error("record overlapping run", "server", claim.ServerID, "error", err)
			}
			continue
		}
		r.wg.Add(1)
		go func(claim Claim) {
			defer r.wg.Done()
			defer r.finish(claim.ServerID)
			r.runClaim(ctx, claim, now.UTC())
		}(claim)
	}
	return nil
}

func (r *Runner) begin(serverID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inflight[serverID] {
		return false
	}
	r.inflight[serverID] = true
	return true
}

func (r *Runner) finish(serverID string) {
	r.mu.Lock()
	delete(r.inflight, serverID)
	r.mu.Unlock()
}

// runClaim uses a detached context: a scheduled stop must finish persisting RAM
// even when the manager is already shutting down.
func (r *Runner) runClaim(ctx context.Context, claim Claim, now time.Time) {
	runErr := r.Execute(context.WithoutCancel(ctx), claim.Schedule)
	recordCtx := context.WithoutCancel(ctx)
	if err := r.Schedules.CompleteRun(recordCtx, claim.RunID, time.Now().UTC(), runErr); err != nil {
		r.Logger.Error("record scheduler run", "server", claim.ServerID, "error", err)
	}
	switch {
	case runErr == nil:
		if claim.FailureCount == 0 {
			return
		}
		if err := r.Schedules.ClearFailures(recordCtx, claim.ServerID); err != nil {
			r.Logger.Error("clear scheduler failures", "server", claim.ServerID, "error", err)
		}
	case IsPermanent(runErr):
		r.Logger.Error("scheduled action failed permanently", "server", claim.ServerID, "action", claim.Action, "error", runErr)
		if err := r.Schedules.ClearFailures(recordCtx, claim.ServerID); err != nil {
			r.Logger.Error("clear scheduler failures", "server", claim.ServerID, "error", err)
		}
	default:
		failures := claim.FailureCount + 1
		if failures > r.MaxRetries {
			r.Logger.Error("scheduled action exhausted retries", "server", claim.ServerID, "action", claim.Action, "attempts", failures, "error", runErr)
			if err := r.Schedules.ClearFailures(recordCtx, claim.ServerID); err != nil {
				r.Logger.Error("clear scheduler failures", "server", claim.ServerID, "error", err)
			}
			return
		}
		at := now.Add(RetryDelay(r.RetryBase, failures))
		r.Logger.Warn("scheduled action failed; retrying", "server", claim.ServerID, "action", claim.Action, "attempt", failures, "at", at, "error", runErr)
		if err := r.Schedules.Retry(recordCtx, claim.ServerID, at, failures); err != nil {
			r.Logger.Error("schedule scheduler retry", "server", claim.ServerID, "error", err)
		}
	}
}

// NextOccurrence returns the next run time strictly after now.
func (s Schedule) NextOccurrence(now time.Time) (time.Time, error) {
	now = now.UTC()
	switch s.Mode {
	case "cron":
		cron, err := ParseCron(s.CronExpression)
		if err != nil {
			return time.Time{}, err
		}
		zone := s.Timezone
		if zone == "" {
			zone = "UTC"
		}
		location, err := time.LoadLocation(zone)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid timezone: %w", err)
		}
		return cron.Next(now, location)
	case "daily":
		parsed, err := time.Parse("15:04", s.DailyTime)
		if err != nil {
			return time.Time{}, fmt.Errorf("daily_time must use HH:MM UTC")
		}
		next := time.Date(now.Year(), now.Month(), now.Day(), parsed.Hour(), parsed.Minute(), 0, 0, time.UTC)
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		return next, nil
	default:
		if s.IntervalSeconds < 60 {
			return time.Time{}, fmt.Errorf("interval_seconds must be at least 60")
		}
		return now.Add(time.Duration(s.IntervalSeconds) * time.Second), nil
	}
}

// RetryDelay doubles the base delay per attempt: 1, 2, 4 minutes by default.
func RetryDelay(base time.Duration, attempt int) time.Duration {
	if attempt < 1 {
		return base
	}
	for i := 1; i < attempt; i++ {
		base *= 2
	}
	return base
}
