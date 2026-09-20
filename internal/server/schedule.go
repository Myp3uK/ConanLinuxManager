package server

import (
	"context"
	"fmt"
	"time"

	"github.com/example/conan-manager/internal/scheduler"
)

func (r *Repository) GetSchedule(ctx context.Context, id string) (scheduler.Schedule, error) {
	var s scheduler.Schedule
	var enabled int
	var next, updated string
	err := r.db.QueryRowContext(ctx, `SELECT server_id, action, enabled, mode, interval_seconds, COALESCE(daily_time,''), COALESCE(cron_expression,''), COALESCE(timezone,'UTC'), next_run, updated_at FROM schedules WHERE server_id=?`, id).Scan(&s.ServerID, &s.Action, &enabled, &s.Mode, &s.IntervalSeconds, &s.DailyTime, &s.CronExpression, &s.Timezone, &next, &updated)
	if err != nil {
		return s, err
	}
	s.Enabled = enabled != 0
	s.NextRun = parseDatabaseTime(next)
	s.UpdatedAt = parseDatabaseTime(updated)
	return s, nil
}

func (r *Repository) SetSchedule(ctx context.Context, s scheduler.Schedule) error {
	if s.Mode == "" {
		s.Mode = "interval"
	}
	if s.Mode == "daily" && s.IntervalSeconds == 0 {
		s.IntervalSeconds = 86400
	}
	if s.Mode == "cron" && s.Timezone == "" {
		s.Timezone = "UTC"
	}
	if err := s.Validate(); err != nil {
		return err
	}
	if _, err := r.Get(ctx, s.ServerID); err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `INSERT INTO schedules(server_id,action,enabled,mode,interval_seconds,daily_time,cron_expression,timezone,next_run,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(server_id) DO UPDATE SET action=excluded.action,enabled=excluded.enabled,mode=excluded.mode,interval_seconds=excluded.interval_seconds,daily_time=excluded.daily_time,cron_expression=excluded.cron_expression,timezone=excluded.timezone,next_run=excluded.next_run,updated_at=excluded.updated_at`, s.ServerID, s.Action, s.Enabled, s.Mode, s.IntervalSeconds, s.DailyTime, s.CronExpression, s.Timezone, s.NextRun.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	return err
}

func (r *Repository) DeleteSchedule(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM schedules WHERE server_id=?`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("schedule for server %q not found", id)
	}
	return nil
}

func (r *Repository) Due(ctx context.Context, now time.Time) ([]scheduler.Schedule, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT server_id, action, enabled, mode, interval_seconds, COALESCE(daily_time,''), COALESCE(cron_expression,''), COALESCE(timezone,'UTC'), next_run, updated_at FROM schedules WHERE enabled=1 AND julianday(next_run)<=julianday(?)`, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []scheduler.Schedule
	for rows.Next() {
		var s scheduler.Schedule
		var enabled int
		var next, updated string
		if err := rows.Scan(&s.ServerID, &s.Action, &enabled, &s.Mode, &s.IntervalSeconds, &s.DailyTime, &s.CronExpression, &s.Timezone, &next, &updated); err != nil {
			return nil, err
		}
		s.Enabled, s.NextRun, s.UpdatedAt = enabled != 0, parseDatabaseTime(next), parseDatabaseTime(updated)
		result = append(result, s)
	}
	return result, rows.Err()
}

func (r *Repository) Advance(ctx context.Context, id string, next time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE schedules SET next_run=?, updated_at=? WHERE server_id=?`, next.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (r *Repository) RecordScheduleRun(ctx context.Context, s scheduler.Schedule, started, finished time.Time, runErr error) error {
	status := "succeeded"
	var message any
	if runErr != nil {
		status = "failed"
		message = runErr.Error()
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO scheduler_runs(server_id,action,status,error,started_at,finished_at) VALUES(?,?,?,?,?,?)`, s.ServerID, s.Action, status, message, started.UTC().Format(time.RFC3339Nano), finished.UTC().Format(time.RFC3339Nano))
	return err
}

func (r *Repository) ScheduleRuns(ctx context.Context, id string, limit int) ([]scheduler.Run, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,server_id,action,status,COALESCE(error,''),started_at,COALESCE(finished_at,'') FROM scheduler_runs WHERE server_id=? ORDER BY id DESC LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]scheduler.Run, 0)
	for rows.Next() {
		var run scheduler.Run
		var message, started, finished string
		if err := rows.Scan(&run.ID, &run.ServerID, &run.Action, &run.Status, &message, &started, &finished); err != nil {
			return nil, err
		}
		run.Error = message
		run.StartedAt = parseDatabaseTime(started)
		run.FinishedAt = parseDatabaseTime(finished)
		result = append(result, run)
	}
	return result, rows.Err()
}
