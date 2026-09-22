package gameconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/conan-manager/internal/server"
)

func TestSeedWritesOnlyMissingFiles(t *testing.T) {
	cfg := server.Config{ID: "a", Name: "My Server", InstallDir: t.TempDir(), Runtime: server.RuntimeNative}
	game, _ := FilePath(cfg, "Game")
	if err := os.MkdirAll(filepath.Dir(game), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(game, []byte("[RconPlugin]\nRconEnabled=True\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	written, err := Seed(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(written, ",") != "Engine,ServerSettings" {
		t.Fatalf("written %v", written)
	}
	if b, _ := os.ReadFile(game); string(b) != "[RconPlugin]\nRconEnabled=True\n" {
		t.Fatalf("existing file changed: %q", b)
	}
	engine, _ := FilePath(cfg, "Engine")
	b, _ := os.ReadFile(engine)
	sections := Parse(string(b))
	find := func(section, key string) string {
		for _, s := range sections {
			if s.Name == section {
				for _, e := range s.Entries {
					if e.Key == key {
						return e.Value
					}
				}
			}
		}
		return "<none>"
	}
	if find("OnlineSubsystem", "ServerName") != "My Server" || find("Core.Log", "NPC") != "Fatal" || find("Core.Log", "ConanSandbox") != "Warning" {
		t.Fatalf("engine seed:\n%s", b)
	}
	if find("URL", "Port") != "<none>" {
		t.Fatal("ports must come from the command line only")
	}
	// A second run changes nothing.
	if written, err := Seed(cfg); err != nil || len(written) != 0 {
		t.Fatal(written, err)
	}
}
