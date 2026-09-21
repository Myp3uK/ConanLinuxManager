package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/store"
)

func TestRepositoryCRUD(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewRepository(db.DB())
	cfg := Config{ID: "alpha", Name: "Alpha", Engine: EngineUE5, Runtime: RuntimeNative, InstallDir: "/srv/alpha", SteamCMDDir: "/srv/steamcmd", GamePort: 7777, QueryPort: 27015, RamdiskEnabled: true, RamdiskPath: "/run/conan-manager/alpha", RamdiskSizeMB: 512}
	if err := repo.Create(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(context.Background(), cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "public" || !got.RamdiskEnabled {
		t.Fatalf("unexpected server: %+v", got)
	}
	items, err := repo.List(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("list: %v, %d", err, len(items))
	}
	if err := repo.Delete(context.Background(), cfg.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryUpdateChangesSettingsAndKeepsIdentity(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewRepository(db.DB())
	ctx := context.Background()
	cfg := Config{ID: "alpha", Name: "Alpha", Engine: EngineUE5, Runtime: RuntimeNative, InstallDir: "/srv/alpha", SteamCMDDir: "/srv/steamcmd", GamePort: 7777, QueryPort: 27015, RamdiskEnabled: true, RamdiskPath: "/run/conan-manager/alpha", RamdiskSizeMB: 512}
	if err := repo.Create(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	created, err := repo.Get(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if created.PeriodicBackupIntervalSeconds != 0 {
		t.Fatalf("new server must default to the game's own cadence, got %d", created.PeriodicBackupIntervalSeconds)
	}

	updated := created
	updated.Name = "Renamed"
	updated.PeriodicBackupIntervalSeconds = 2400
	if err := repo.Update(ctx, updated); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Renamed" || got.PeriodicBackupIntervalSeconds != 2400 {
		t.Fatalf("update not stored: %+v", got)
	}
	if got.InstallDir != cfg.InstallDir || got.RamdiskPath != cfg.RamdiskPath {
		t.Fatalf("update moved on-disk paths: %+v", got)
	}
	if !got.UpdatedAt.After(created.UpdatedAt) && !got.UpdatedAt.Equal(created.UpdatedAt) {
		t.Fatalf("updated_at went backwards: %v -> %v", created.UpdatedAt, got.UpdatedAt)
	}

	missing := updated
	missing.ID = "ghost"
	if err := repo.Update(ctx, missing); err == nil {
		t.Fatal("updating an unknown server must fail")
	}
}

func TestPeriodicBackupIntervalIsValidated(t *testing.T) {
	base := Config{ID: "a", Name: "A", Engine: EngineUE5, Runtime: RuntimeNative, InstallDir: "/srv/a", SteamCMDDir: "/srv/steam", GamePort: 7777, QueryPort: 27015}
	for _, seconds := range []int{0, 60, 2400, 86400} {
		cfg := base
		cfg.PeriodicBackupIntervalSeconds = seconds
		if err := cfg.Validate(); err != nil {
			t.Errorf("%d rejected: %v", seconds, err)
		}
	}
	// Below a minute the copy of a large world would overlap the next cycle.
	for _, seconds := range []int{-1, 1, 59, 86401} {
		cfg := base
		cfg.PeriodicBackupIntervalSeconds = seconds
		if err := cfg.Validate(); err == nil {
			t.Errorf("%d accepted", seconds)
		}
	}
}

func TestHookConfigurationIsValidatedAndStored(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewRepository(db.DB())
	ctx := context.Background()
	cfg := Config{ID: "a", Name: "A", Engine: EngineUE5, Runtime: RuntimeNative, InstallDir: "/srv/a", SteamCMDDir: "/srv/s", GamePort: 7777, QueryPort: 27015,
		Hooks: map[string][]string{"pre_start": {"check-disk.sh"}, "on_crash": {"notify", "collect-dump"}}}
	if err := repo.Create(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hooks["on_crash"]) != 2 || got.Hooks["pre_start"][0] != "check-disk.sh" {
		t.Fatalf("hooks not stored: %+v", got.Hooks)
	}
	for _, bad := range []map[string][]string{
		{"before_start": {"x"}},
		{"pre_start": {"../etc/passwd"}},
		{"pre_start": {"/bin/sh"}},
		{"pre_start": {".hidden"}},
	} {
		invalid := cfg
		invalid.ID = "b"
		invalid.Hooks = bad
		if err := invalid.Validate(); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	run := HookRun{ServerID: "a", Event: "pre_start", Script: "check-disk.sh", StartedAt: time.Now(), FinishedAt: time.Now(), ExitCode: 2, Output: "disk full", Error: "exit status 2"}
	if err := repo.RecordHookRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	runs, err := repo.HookRuns(ctx, "a", 10)
	if err != nil || len(runs) != 1 || runs[0].ExitCode != 2 || runs[0].Error != "exit status 2" {
		t.Fatalf("runs: %+v %v", runs, err)
	}
}
