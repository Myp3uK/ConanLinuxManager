package gameconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/example/conan-manager/internal/server"
)

func customConfig(t *testing.T) server.Config {
	t.Helper()
	return server.Config{ID: "a", Name: "a", Engine: server.EngineUE5, Runtime: server.RuntimeNative,
		InstallDir: t.TempDir(), SteamCMDDir: "/srv/steamcmd", GamePort: 7777, QueryPort: 27015}
}

func TestValidConfigName(t *testing.T) {
	for _, good := range []string{"ModHttp.ini", "Engine.ini", "a.ini", "mod-settings_2.ini", "A.B.ini"} {
		if !ValidConfigName(good) {
			t.Errorf("%q should be accepted", good)
		}
	}
	// A name is only ever a file inside the server's own config directory.
	for _, bad := range []string{
		"", "ModHttp", "ModHttp.txt", ".hidden.ini", "../../etc/passwd.ini", "sub/dir.ini",
		`sub\dir.ini`, "/etc/passwd.ini", "..ini", "a..b.ini",
	} {
		if ValidConfigName(bad) {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestFileNameTakesShortNamesAndFileNames(t *testing.T) {
	for in, want := range map[string]string{
		"Game":           "Game.ini",
		"ServerSettings": "ServerSettings.ini",
		"Engine":         "Engine.ini",
		"ModHttp.ini":    "ModHttp.ini",
	} {
		got, err := FileName(in)
		if err != nil || got != want {
			t.Errorf("FileName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := FileName("../escape.ini"); err == nil {
		t.Error("a path should be refused")
	}
}

func TestFilePathStaysInTheConfigDirectory(t *testing.T) {
	cfg := customConfig(t)
	path, err := FilePath(cfg, "ModHttp.ini")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != Dir(cfg) {
		t.Errorf("path %q is outside %q", path, Dir(cfg))
	}
}

func TestListShowsTheGamesFilesFirstThenTheRest(t *testing.T) {
	cfg := customConfig(t)
	if err := os.MkdirAll(Dir(cfg), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Game.ini", "ModHttp.ini", "AMod.ini", "ServerSettings.ini.conan-manager.tmp", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(Dir(cfg), name), []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	files, err := List(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The game's three come first in a fixed order, so the panel's buttons do
	// not reshuffle as files appear.
	if len(files) != 5 {
		t.Fatalf("got %d files: %+v", len(files), files)
	}
	for i, want := range []string{"ServerSettings", "Game", "Engine", "AMod.ini", "ModHttp.ini"} {
		if files[i].Name != want {
			t.Fatalf("file %d = %q; want %q (%+v)", i, files[i].Name, want, files)
		}
	}
	if !files[1].Exists || files[2].Exists {
		t.Errorf("existence wrong: Game=%v Engine=%v", files[1].Exists, files[2].Exists)
	}
	if files[0].Custom || !files[3].Custom {
		t.Errorf("custom flag wrong: %+v", files)
	}
	// The manager's own temporary file and a non-INI are not configs.
	for _, f := range files {
		if f.File == "notes.txt" || filepath.Ext(f.File) != ".ini" {
			t.Errorf("unexpected entry: %+v", f)
		}
	}
}
