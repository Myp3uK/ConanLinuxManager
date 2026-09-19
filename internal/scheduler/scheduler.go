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
	IntervalSeconds int       `json:"interval_seconds"`
	NextRun         time.Time `json:"next_run"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (s Schedule) Validate() error {
	if s.ServerID == "" {
		return fmt.Errorf("server_id is required")
	}
	if s.Action != "start" && s.Action != "stop" && s.Action != "restart" {
		return fmt.Errorf("action must be start, stop or restart")
	}
	if s.IntervalSeconds < 60 {
		return fmt.Errorf("interval_seconds must be at least 60")
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
	Execute func(context.Context, Schedule) error
}

func (r Runner) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			items, err := r.Schedules.Due(ctx, now.UTC())
			if err != nil {
				continue
			}
			for _, item := range items {
				_ = r.Execute(ctx, item)
				_ = r.Schedules.Advance(ctx, item.ServerID, now.UTC().Add(time.Duration(item.IntervalSeconds)*time.Second))
			}
		}
	}
}
