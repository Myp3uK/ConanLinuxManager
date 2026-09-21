package controller

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/example/conan-manager/internal/gameconfig"
	"github.com/example/conan-manager/internal/server"
)

func TestRconSettingsComeFromGameIni(t *testing.T) {
	cfg := server.Config{ID: "a", InstallDir: t.TempDir(), Runtime: server.RuntimeNative}
	path := gameconfig.Path(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	ini := "[/Script/ConanSandbox.ConanGameMode]\nPeriodicBackupInterval=2400\n\n[RconPlugin]\r\nRconEnabled=True\r\nRconPort=25704\r\nRconMaxKarma=240\r\nRconMessageMethod=0\r\nRconPassword=example-rcon-pass\r\n"
	if err := os.WriteFile(path, []byte(ini), 0o640); err != nil {
		t.Fatal(err)
	}
	s, err := RconSettings(cfg)
	if err != nil || s.Usable() != nil || s.Port != 25704 || s.Password != "example-rcon-pass" {
		t.Fatalf("%+v %v", s, err)
	}
	// A server that never had RCON configured says how to turn it on.
	if s, _ := RconSettings(server.Config{ID: "b", InstallDir: t.TempDir()}); s.Usable() == nil {
		t.Fatal("missing section accepted")
	}
}
