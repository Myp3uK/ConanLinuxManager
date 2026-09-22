package server

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/scheduler"
	"github.com/example/conan-manager/internal/store"
)

func newScheduleRepo(t *testing.T) (*Repository, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := NewRepository(db.DB())
	cfg := Config{ID: "one", Name: "One", Engine: EngineUE5, Runtime: RuntimeNative,
		InstallDir: t.TempDir(), SteamCMDDir: t.TempDir(), GamePort: 7777, QueryPort: 27015}
	if err := r.Create(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	return r, db
}

func TestLegacyScheduleMigrationAndModePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r := NewRepository(db.DB())
	cfg := Config{ID: "one", Name: "One", Engine: EngineUE5, Runtime: RuntimeNative, InstallDir: t.TempDir(), SteamCMDDir: t.TempDir(), GamePort: 7777, QueryPort: 27015}
	if err := r.Create(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	_, err = db.DB().Exec(`DROP TABLE schedules; CREATE TABLE schedules (server_id TEXT PRIMARY KEY, action TEXT NOT NULL, enabled INTEGER NOT NULL, interval_seconds INTEGER NOT NULL CHECK(interval_seconds>=60), next_run TEXT NOT NULL, updated_at TEXT NOT NULL); INSERT INTO schedules VALUES ('one','stop',1,60,'2026-09-20T03:00:00Z','2026-09-19T03:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r = NewRepository(db.DB())
	s, err := r.GetSchedule(ctx, "one")
	if err != nil || s.Mode != "interval" || s.DailyTime != "" {
		t.Fatalf("migrated: %+v %v", s, err)
	}
	now := time.Date(2026, 9, 20, 3, 0, 0, 500000000, time.UTC)
	due, err := r.Due(ctx, now)
	if err != nil || len(due) != 1 {
		t.Fatalf("due: %v %v", due, err)
	}

	s.Mode, s.DailyTime, s.IntervalSeconds = "daily", "03:00", 0
	if err := r.SetSchedule(ctx, s); err != nil {
		t.Fatal(err)
	}
	s, err = r.GetSchedule(ctx, "one")
	if err != nil || s.Mode != "daily" || s.DailyTime != "03:00" {
		t.Fatalf("daily: %+v %v", s, err)
	}
	// A daily schedule no longer has to carry a meaningless interval.
	if s.IntervalSeconds != 0 {
		t.Fatalf("daily kept a placeholder interval: %d", s.IntervalSeconds)
	}

	s.DailyTime = "25:00"
	if err := r.SetSchedule(ctx, s); err == nil {
		t.Fatal("invalid time accepted")
	}

	s.DailyTime = ""
	s.Mode, s.CronExpression, s.Timezone = "cron", "0 3 * * *", "Europe/Berlin"
	if err := r.SetSchedule(ctx, s); err != nil {
		t.Fatal(err)
	}
	s, err = r.GetSchedule(ctx, "one")
	if err != nil || s.Mode != "cron" || s.CronExpression != "0 3 * * *" || s.Timezone != "Europe/Berlin" {
		t.Fatalf("cron: %+v %v", s, err)
	}
	if s.IntervalSeconds != 0 {
		t.Fatalf("cron kept a placeholder interval: %d", s.IntervalSeconds)
	}
}

// Claiming advances next_run and opens the history row in one transaction.
func TestClaimDueAdvancesAndRecordsRun(t *testing.T) {
	r, _ := newScheduleRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)
	if err := r.SetSchedule(ctx, scheduler.Schedule{ServerID: "one", Action: "restart", Enabled: true,
		Mode: "interval", IntervalSeconds: 60, NextRun: now}); err != nil {
		t.Fatal(err)
	}

	claims, err := r.ClaimDue(ctx, now, nil)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims: %+v %v", claims, err)
	}
	if claims[0].RunID == 0 {
		t.Fatal("claim did not open a history row")
	}

	stored, err := r.GetSchedule(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.NextRun.Equal(now.Add(time.Minute)) {
		t.Fatalf("next_run not advanced at claim time: %v", stored.NextRun)
	}
	// The same instant must not produce a second claim.
	again, err := r.ClaimDue(ctx, now, nil)
	if err != nil || len(again) != 0 {
		t.Fatalf("double claim: %+v %v", again, err)
	}

	runs, err := r.ScheduleRuns(ctx, "one", 50)
	if err != nil || len(runs) != 1 || runs[0].Status != "running" {
		t.Fatalf("history after claim: %+v %v", runs, err)
	}

	if err := r.CompleteRun(ctx, claims[0].RunID, now.Add(time.Second), errors.New("controller busy")); err != nil {
		t.Fatal(err)
	}
	runs, err = r.ScheduleRuns(ctx, "one", 50)
	if err != nil || len(runs) != 1 || runs[0].Status != "failed" || runs[0].Error != "controller busy" {
		t.Fatalf("completed history: %+v %v", runs, err)
	}
}

// A run left open by a killed process must be closed, not resumed.
func TestMarkInterruptedClosesOrphanedRuns(t *testing.T) {
	r, _ := newScheduleRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)
	if err := r.SetSchedule(ctx, scheduler.Schedule{ServerID: "one", Action: "stop", Enabled: true,
		Mode: "interval", IntervalSeconds: 60, NextRun: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ClaimDue(ctx, now, nil); err != nil {
		t.Fatal(err)
	}

	count, err := r.MarkInterrupted(ctx)
	if err != nil || count != 1 {
		t.Fatalf("interrupted: %d %v", count, err)
	}
	runs, err := r.ScheduleRuns(ctx, "one", 50)
	if err != nil || len(runs) != 1 || runs[0].Status != "interrupted" {
		t.Fatalf("history: %+v %v", runs, err)
	}
	if runs[0].FinishedAt.IsZero() {
		t.Fatal("interrupted run has no finish time")
	}
	// Nothing is left running, so a second pass changes nothing.
	count, err = r.MarkInterrupted(ctx)
	if err != nil || count != 0 {
		t.Fatalf("second pass: %d %v", count, err)
	}
}

func TestRetryAndClearFailures(t *testing.T) {
	r, _ := newScheduleRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)
	if err := r.SetSchedule(ctx, scheduler.Schedule{ServerID: "one", Action: "start", Enabled: true,
		Mode: "interval", IntervalSeconds: 60, NextRun: now}); err != nil {
		t.Fatal(err)
	}
	retryAt := now.Add(2 * time.Minute)
	if err := r.Retry(ctx, "one", retryAt, 2); err != nil {
		t.Fatal(err)
	}
	stored, err := r.GetSchedule(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if stored.FailureCount != 2 || !stored.NextRun.Equal(retryAt) {
		t.Fatalf("retry not persisted: %+v", stored)
	}
	// A restart of the manager must see the stored counter, not start from zero.
	if err := r.ClearFailures(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	stored, err = r.GetSchedule(ctx, "one")
	if err != nil || stored.FailureCount != 0 {
		t.Fatalf("failures not cleared: %+v %v", stored, err)
	}
}

// Editing a schedule resets its retry state so an old failure streak is not
// inherited by the new configuration.
func TestSetScheduleResetsFailureCount(t *testing.T) {
	r, _ := newScheduleRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)
	item := scheduler.Schedule{ServerID: "one", Action: "start", Enabled: true, Mode: "interval", IntervalSeconds: 60, NextRun: now}
	if err := r.SetSchedule(ctx, item); err != nil {
		t.Fatal(err)
	}
	if err := r.Retry(ctx, "one", now, 3); err != nil {
		t.Fatal(err)
	}
	item.Action = "restart"
	if err := r.SetSchedule(ctx, item); err != nil {
		t.Fatal(err)
	}
	stored, err := r.GetSchedule(ctx, "one")
	if err != nil || stored.FailureCount != 0 {
		t.Fatalf("failure count survived an edit: %+v %v", stored, err)
	}
}

// A schedule that was disabled or edited between reading and claiming must not
// be advanced on stale information.
func TestClaimSkipsScheduleChangedMeanwhile(t *testing.T) {
	r, db := newScheduleRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)
	if err := r.SetSchedule(ctx, scheduler.Schedule{ServerID: "one", Action: "stop", Enabled: true,
		Mode: "interval", IntervalSeconds: 60, NextRun: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`UPDATE schedules SET enabled=0 WHERE server_id='one'`); err != nil {
		t.Fatal(err)
	}
	claims, err := r.ClaimDue(ctx, now, nil)
	if err != nil || len(claims) != 0 {
		t.Fatalf("claimed a disabled schedule: %+v %v", claims, err)
	}
}

// A cron expression that can never fire again is disabled instead of being
// re-claimed on every tick.
func TestClaimDisablesUnsatisfiableSchedule(t *testing.T) {
	r, db := newScheduleRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)
	// Validate rejects this, so it is written directly as a corrupted row.
	if _, err := db.DB().Exec(`INSERT INTO schedules(server_id,action,enabled,mode,cron_expression,timezone,next_run,updated_at)
VALUES('one','stop',1,'cron','0 0 31 2 *','UTC',?,?)`,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	claims, err := r.ClaimDue(ctx, now, nil)
	if err != nil || len(claims) != 0 {
		t.Fatalf("claims: %+v %v", claims, err)
	}
	stored, err := r.GetSchedule(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Enabled {
		t.Fatal("unsatisfiable schedule stayed enabled")
	}
	runs, err := r.ScheduleRuns(ctx, "one", 50)
	if err != nil || len(runs) != 1 || runs[0].Status != "failed" {
		t.Fatalf("no failure recorded: %+v %v", runs, err)
	}
}

// A lead claims a run before its time, keeps that time as DueAt and moves
// next_run past it, so warnings can precede an action that stays on time.
func TestClaimDueWithLeadClaimsAhead(t *testing.T) {
	r, _ := newScheduleRepo(t)
	ctx := context.Background()
	due := time.Date(2026, 9, 20, 6, 0, 0, 0, time.UTC)
	if err := r.SetSchedule(ctx, scheduler.Schedule{ServerID: "one", Action: "restart", Enabled: true,
		Mode: "cron", CronExpression: "0 6 * * *", Timezone: "UTC", NextRun: due}); err != nil {
		t.Fatal(err)
	}
	lead := func(scheduler.Schedule) time.Duration { return 15 * time.Minute }
	if claims, err := r.ClaimDue(ctx, due.Add(-16*time.Minute), lead); err != nil || len(claims) != 0 {
		t.Fatalf("claimed too early: %+v %v", claims, err)
	}
	if claims, err := r.ClaimDue(ctx, due.Add(-10*time.Minute), nil); err != nil || len(claims) != 0 {
		t.Fatalf("claimed ahead without a lead: %+v %v", claims, err)
	}
	claims, err := r.ClaimDue(ctx, due.Add(-15*time.Minute), lead)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims: %+v %v", claims, err)
	}
	if !claims[0].DueAt.Equal(due) || !claims[0].NextRun.Equal(due.Add(24*time.Hour)) {
		t.Fatalf("due %v, next %v", claims[0].DueAt, claims[0].NextRun)
	}
	if again, err := r.ClaimDue(ctx, due, lead); err != nil || len(again) != 0 {
		t.Fatalf("claimed twice: %+v %v", again, err)
	}
}
