package server

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Myp3uK/ConanLinuxManager/internal/store"
)

func TestParseWorkshopID(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"1159180273", "1159180273"},
		{"  1159180273 ", "1159180273"},
		{"https://steamcommunity.com/sharedfiles/filedetails/?id=1159180273", "1159180273"},
		{"https://steamcommunity.com/workshop/filedetails/?id=1159180273&searchtext=pippi", "1159180273"},
		{"steam://url/CommunityFilePage/1159180273", "1159180273"},
	} {
		got, err := ParseWorkshopID(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseWorkshopID(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "   ", "Pippi", "0123", "12a34", "https://steamcommunity.com/app/440900"} {
		if got, err := ParseWorkshopID(bad); err == nil {
			t.Errorf("ParseWorkshopID(%q) = %q; want an error", bad, got)
		}
	}
}

func modsRepo(t *testing.T) (*Repository, context.Context) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repo := NewRepository(db.DB())
	ctx := context.Background()
	cfg := Config{ID: "alpha", Name: "Alpha", Engine: EngineUE5, Runtime: RuntimeNative, InstallDir: "/srv/alpha",
		SteamCMDDir: "/srv/steamcmd", GamePort: 7777, QueryPort: 27015}
	if err := repo.Create(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	return repo, ctx
}

func TestSetModsKeepsInstalledStateAndOrder(t *testing.T) {
	repo, ctx := modsRepo(t)
	if err := repo.SetMods(ctx, "alpha", []Mod{
		{WorkshopID: "111111", Enabled: true},
		{WorkshopID: "222222", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	updated := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if err := repo.MarkModInstalled(ctx, "alpha", "111111", []string{"Pippi.pak"}, 4096, updated); err != nil {
		t.Fatal(err)
	}
	// Reordering must not drop what the manager knows about an installed mod,
	// or every reorder would mean downloading everything again.
	if err := repo.SetMods(ctx, "alpha", []Mod{
		{WorkshopID: "222222", Enabled: false},
		{WorkshopID: "111111", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	list, err := repo.Mods(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].WorkshopID != "222222" || list[1].WorkshopID != "111111" {
		t.Fatalf("unexpected order: %+v", list)
	}
	if list[0].Enabled {
		t.Errorf("mod 222222 should be disabled")
	}
	installed := list[1]
	if len(installed.Paks) != 1 || installed.Paks[0] != "Pippi.pak" || installed.Size != 4096 {
		t.Fatalf("installed state lost: %+v", installed)
	}
	if !installed.InstalledUpdated.Equal(updated) {
		t.Errorf("installed_updated = %v; want %v", installed.InstalledUpdated, updated)
	}
	if installed.NeedsInstall() || installed.UpdateAvailable() {
		t.Errorf("installed mod should be up to date: %+v", installed)
	}
	if !list[0].NeedsInstall() {
		t.Errorf("mod without paks needs an install: %+v", list[0])
	}
}

func TestSetModsRemovesWhatWasLeftOut(t *testing.T) {
	repo, ctx := modsRepo(t)
	if err := repo.SetMods(ctx, "alpha", []Mod{{WorkshopID: "111111", Enabled: true}, {WorkshopID: "222222"}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetMods(ctx, "alpha", []Mod{{WorkshopID: "222222"}}); err != nil {
		t.Fatal(err)
	}
	list, err := repo.Mods(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].WorkshopID != "222222" {
		t.Fatalf("unexpected list: %+v", list)
	}
	if err := repo.SetMods(ctx, "alpha", nil); err != nil {
		t.Fatal(err)
	}
	if list, err = repo.Mods(ctx, "alpha"); err != nil || len(list) != 0 {
		t.Fatalf("empty list: %v, %+v", err, list)
	}
}

func TestSetModsRejectsBadInput(t *testing.T) {
	repo, ctx := modsRepo(t)
	if err := repo.SetMods(ctx, "alpha", []Mod{{WorkshopID: "111111"}, {WorkshopID: "111111"}}); err == nil {
		t.Error("a duplicate mod should be rejected")
	}
	if err := repo.SetMods(ctx, "alpha", []Mod{{WorkshopID: "not-an-id"}}); err == nil {
		t.Error("a non-numeric id should be rejected")
	}
	many := make([]Mod, maxModsPerServer+1)
	for i := range many {
		many[i] = Mod{WorkshopID: fmt.Sprint(100000 + i)}
	}
	if err := repo.SetMods(ctx, "alpha", many); err == nil {
		t.Error("an oversized list should be rejected")
	}
}

func TestMarkModCheckedTracksUpdatesAndFailures(t *testing.T) {
	repo, ctx := modsRepo(t)
	if err := repo.SetMods(ctx, "alpha", []Mod{{WorkshopID: "111111", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	installed := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if err := repo.MarkModInstalled(ctx, "alpha", "111111", []string{"Pippi.pak"}, 10, installed); err != nil {
		t.Fatal(err)
	}
	newer := installed.Add(48 * time.Hour)
	if err := repo.MarkModChecked(ctx, "alpha", "111111", "Pippi", newer, ""); err != nil {
		t.Fatal(err)
	}
	list, err := repo.Mods(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Title != "Pippi" || !list[0].UpdateAvailable() {
		t.Fatalf("expected a waiting update: %+v", list[0])
	}
	// A failed check keeps the title and the timestamps it had: a list that
	// empties itself when Steam is unreachable is worse than a stale one.
	if err := repo.MarkModChecked(ctx, "alpha", "111111", "", time.Time{}, "Steam unreachable"); err != nil {
		t.Fatal(err)
	}
	if list, err = repo.Mods(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if list[0].Title != "Pippi" || !list[0].LatestUpdated.Equal(newer) || list[0].LastError != "Steam unreachable" {
		t.Fatalf("failed check lost state: %+v", list[0])
	}
}

func TestDeletingServerRemovesItsMods(t *testing.T) {
	repo, ctx := modsRepo(t)
	if err := repo.SetMods(ctx, "alpha", []Mod{{WorkshopID: "111111", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	list, err := repo.Mods(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("mods outlived their server: %+v", list)
	}
}
