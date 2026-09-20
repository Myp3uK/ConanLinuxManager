package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/scheduler"
	"github.com/example/conan-manager/internal/store"
)

func TestLegacyScheduleMigrationAndDailyPersistence(t *testing.T) {
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
	if err != nil || s.Mode != "daily" || s.IntervalSeconds != 86400 {
		t.Fatalf("daily: %+v %v", s, err)
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
	if err := r.RecordScheduleRun(ctx, scheduler.Schedule{ServerID: "one", Action: "stop"}, now, now.Add(time.Second), nil); err != nil {
		t.Fatal(err)
	}
	runs, err := r.ScheduleRuns(ctx, "one", 50)
	if err != nil || len(runs) != 1 || runs[0].Status != "succeeded" || runs[0].Error != "" {
		t.Fatalf("history: %+v %v", runs, err)
	}
}
