package server

import (
	"context"
	"fmt"
	"time"

	"github.com/example/conan-manager/internal/scheduler"
)

const scheduleColumns = `server_id, action, enabled, mode, interval_seconds, COALESCE(daily_time,''),
	COALESCE(cron_expression,''), COALESCE(timezone,'UTC'), failure_count, next_run, updated_at`

func scanSchedule(scan func(...any) error) (scheduler.Schedule, string, error) {
	var s scheduler.Schedule
	var enabled int
	var next, updated string
	err := scan(&s.ServerID, &s.Action, &enabled, &s.Mode, &s.IntervalSeconds, &s.DailyTime,
		&s.CronExpression, &s.Timezone, &s.FailureCount, &next, &updated)
	if err != nil {
		return s, "", err
	}
	s.Enabled = enabled != 0
	s.NextRun = parseDatabaseTime(next)
	s.UpdatedAt = parseDatabaseTime(updated)
	// The raw value guards the claim: it is compared verbatim so a schedule that
	// changed since it was read is never advanced on stale information.
	return s, next, nil
}

func (r *Repository) GetSchedule(ctx context.Context, id string) (scheduler.Schedule, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE server_id=?`, id)
	s, _, err := scanSchedule(row.Scan)
	return s, err
}

func (r *Repository) SetSchedule(ctx context.Context, s scheduler.Schedule) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if _, err := r.Get(ctx, s.ServerID); err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `INSERT INTO schedules
		(server_id,action,enabled,mode,interval_seconds,daily_time,cron_expression,timezone,failure_count,next_run,updated_at)
		VALUES(?,?,?,?,?,?,?,?,0,?,?)
		ON CONFLICT(server_id) DO UPDATE SET action=excluded.action,enabled=excluded.enabled,mode=excluded.mode,
			interval_seconds=excluded.interval_seconds,daily_time=excluded.daily_time,
			cron_expression=excluded.cron_expression,timezone=excluded.timezone,
			failure_count=0,next_run=excluded.next_run,updated_at=excluded.updated_at`,
		s.ServerID, s.Action, s.Enabled, s.Mode, s.IntervalSeconds, s.DailyTime, s.CronExpression,
		s.Timezone, s.NextRun.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
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

// Due reports the schedules that are ready to run without claiming them.
func (r *Repository) Due(ctx context.Context, now time.Time) ([]scheduler.Schedule, error) {
	items, err := r.due(ctx, now)
	if err != nil {
		return nil, err
	}
	result := make([]scheduler.Schedule, 0, len(items))
	for _, item := range items {
		result = append(result, item.Schedule)
	}
	return result, nil
}

type dueSchedule struct {
	scheduler.Schedule
	rawNextRun string
}

func (r *Repository) due(ctx context.Context, now time.Time) ([]dueSchedule, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+scheduleColumns+` FROM schedules
		WHERE enabled=1 AND julianday(next_run)<=julianday(?)`, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []dueSchedule
	for rows.Next() {
		s, raw, err := scanSchedule(rows.Scan)
		if err != nil {
			return nil, err
		}
		result = append(result, dueSchedule{Schedule: s, rawNextRun: raw})
	}
	return result, rows.Err()
}

// ClaimDue advances next_run and opens a history row in a single transaction.
// Advancing before the action runs is deliberate: a crash mid-action leaves an
// interrupted run in the history instead of replaying the action on restart.
func (r *Repository) ClaimDue(ctx context.Context, now time.Time) ([]scheduler.Claim, error) {
	items, err := r.due(ctx, now)
	if err != nil {
		return nil, err
	}
	claims := make([]scheduler.Claim, 0, len(items))
	for _, item := range items {
		next, err := item.NextOccurrence(now)
		if err != nil {
			// The stored specification can never fire again. Disabling it makes
			// the problem visible instead of re-claiming it every second.
			if disableErr := r.disableSchedule(ctx, item, now, err); disableErr != nil {
				return claims, disableErr
			}
			continue
		}
		claim, claimed, err := r.claim(ctx, item, next, now)
		if err != nil {
			return claims, err
		}
		if claimed {
			claims = append(claims, claim)
		}
	}
	return claims, nil
}

func (r *Repository) claim(ctx context.Context, item dueSchedule, next, now time.Time) (scheduler.Claim, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return scheduler.Claim{}, false, err
	}
	defer tx.Rollback()
	// The next_run guard is the claim: whoever advances the row first owns the
	// run, and a schedule edited or disabled meanwhile simply does not match.
	result, err := tx.ExecContext(ctx, `UPDATE schedules SET next_run=?, updated_at=?
		WHERE server_id=? AND next_run=? AND enabled=1`,
		next.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano),
		item.ServerID, item.rawNextRun)
	if err != nil {
		return scheduler.Claim{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return scheduler.Claim{}, false, err
	}
	if affected == 0 {
		return scheduler.Claim{}, false, nil
	}
	inserted, err := tx.ExecContext(ctx, `INSERT INTO scheduler_runs(server_id,action,status,started_at)
		VALUES(?,?,'running',?)`, item.ServerID, item.Action, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return scheduler.Claim{}, false, err
	}
	runID, err := inserted.LastInsertId()
	if err != nil {
		return scheduler.Claim{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return scheduler.Claim{}, false, err
	}
	claim := scheduler.Claim{Schedule: item.Schedule, RunID: runID}
	claim.NextRun = next
	return claim, true, nil
}

func (r *Repository) disableSchedule(ctx context.Context, item dueSchedule, now time.Time, reason error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE schedules SET enabled=0, updated_at=?
		WHERE server_id=? AND next_run=? AND enabled=1`,
		time.Now().UTC().Format(time.RFC3339Nano), item.ServerID, item.rawNextRun)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return nil
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO scheduler_runs(server_id,action,status,error,started_at,finished_at)
		VALUES(?,?,'failed',?,?,?)`, item.ServerID, item.Action,
		"schedule disabled: "+reason.Error(), stamp, stamp); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) CompleteRun(ctx context.Context, runID int64, finished time.Time, runErr error) error {
	status := "succeeded"
	var message any
	if runErr != nil {
		status = "failed"
		message = runErr.Error()
	}
	_, err := r.db.ExecContext(ctx, `UPDATE scheduler_runs SET status=?, error=?, finished_at=? WHERE id=?`,
		status, message, finished.UTC().Format(time.RFC3339Nano), runID)
	return err
}

func (r *Repository) Retry(ctx context.Context, serverID string, at time.Time, failures int) error {
	_, err := r.db.ExecContext(ctx, `UPDATE schedules SET next_run=?, failure_count=?, updated_at=? WHERE server_id=?`,
		at.UTC().Format(time.RFC3339Nano), failures, time.Now().UTC().Format(time.RFC3339Nano), serverID)
	return err
}

func (r *Repository) ClearFailures(ctx context.Context, serverID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE schedules SET failure_count=0 WHERE server_id=? AND failure_count<>0`, serverID)
	return err
}

// MarkInterrupted closes runs that a previous manager process left open. It runs
// at startup, where any row still marked running belongs to a dead process.
func (r *Repository) MarkInterrupted(ctx context.Context) (int, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE scheduler_runs SET status='interrupted', finished_at=?
		WHERE status='running'`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

func (r *Repository) ScheduleRuns(ctx context.Context, id string, limit int) ([]scheduler.Run, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,server_id,action,status,COALESCE(error,''),started_at,COALESCE(finished_at,'')
		FROM scheduler_runs WHERE server_id=? ORDER BY id DESC LIMIT ?`, id, limit)
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

var _ scheduler.Store = (*Repository)(nil)
