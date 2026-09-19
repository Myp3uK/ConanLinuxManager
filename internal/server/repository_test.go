package server

import (
	"context"
	"path/filepath"
	"testing"

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
