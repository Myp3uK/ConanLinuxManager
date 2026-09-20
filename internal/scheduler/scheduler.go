package scheduler

import (
	"context"
	"fmt"
	"time"
)

type Schedule struct {
	ServerID        string    `json:"server_id"`
	Action          string    `json:"action"`
	Enabled         bool      `json:"enabled"`
	Mode            string    `json:"mode"`
	IntervalSeconds int       `json:"interval_seconds"`
	DailyTime       string    `json:"daily_time,omitempty"`
	NextRun         time.Time `json:"next_run"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Run struct {
	ID         int64     `json:"id"`
	ServerID   string    `json:"server_id"`
	Action     string    `json:"action"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

func (s Schedule) Validate() error {
	if s.ServerID == "" {
		return fmt.Errorf("server_id is required")
	}
	if s.Action != "start" && s.Action != "stop" && s.Action != "restart" {
		return fmt.Errorf("action must be start, stop or restart")
	}
	if s.Mode == "" {
		s.Mode = "interval"
	}
	if s.Mode != "interval" && s.Mode != "daily" {
		return fmt.Errorf("mode must be interval or daily")
	}
	if s.IntervalSeconds < 60 {
		return fmt.Errorf("interval_seconds must be at least 60")
	}
	if s.Mode == "daily" {
		if _, err := time.Parse("15:04", s.DailyTime); err != nil {
			return fmt.Errorf("daily_time must use HH:MM UTC")
		}
	}
	if s.NextRun.IsZero() {
		return fmt.Errorf("next_run is required")
	}
	return nil
}

type Runner struct {
	Schedules interface {
		Due(context.Context, time.Time) ([]Schedule, error)
		Advance(context.Context, string, time.Time) error
	}
	Execute    func(context.Context, Schedule) error
	Record     func(context.Context, Schedule, time.Time, time.Time, error) error
	RetryBase  time.Duration
	MaxRetries int
	failures   map[string]int
}

func (r Runner) Run(ctx context.Context) {
	if r.RetryBase <= 0 {
		r.RetryBase = time.Minute
	}
	if r.MaxRetries <= 0 {
		r.MaxRetries = 3
	}
	r.failures = make(map[string]int)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			_ = r.RunOnce(ctx, now.UTC())
		}
	}
}

func (r *Runner) RunOnce(ctx context.Context, now time.Time) error {
	if r.RetryBase <= 0 {
		r.RetryBase = time.Minute
	}
	if r.MaxRetries <= 0 {
		r.MaxRetries = 3
	}
	if r.failures == nil {
		r.failures = make(map[string]int)
	}
	items, err := r.Schedules.Due(ctx, now.UTC())
	if err != nil {
		return err
	}
	for _, item := range items {
		started := now.UTC()
		runErr := r.Execute(ctx, item)
		if r.Record != nil {
			_ = r.Record(ctx, item, started, time.Now().UTC(), runErr)
		}
		next := item.NextOccurrence(now.UTC())
		key := item.ServerID + "\x00" + item.Action
		if runErr == nil {
			delete(r.failures, key)
		} else {
			r.failures[key]++
			if r.failures[key] <= r.MaxRetries {
				next = now.UTC().Add(retryDelay(r.RetryBase, r.failures[key]))
			} else {
				delete(r.failures, key)
			}
		}
		if err := r.Schedules.Advance(ctx, item.ServerID, next); err != nil {
			return err
		}
	}
	return nil
}

func (s Schedule) NextOccurrence(now time.Time) time.Time {
	if s.Mode != "daily" {
		return now.UTC().Add(time.Duration(s.IntervalSeconds) * time.Second)
	}
	parsed, err := time.Parse("15:04", s.DailyTime)
	if err != nil {
		return now.UTC().Add(24 * time.Hour)
	}
	next := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), parsed.Hour(), parsed.Minute(), 0, 0, time.UTC)
	if !next.After(now.UTC()) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

func retryDelay(base time.Duration, attempt int) time.Duration {
	if attempt < 1 {
		return base
	}
	for i := 1; i < attempt; i++ {
		base *= 2
	}
	return base
}
