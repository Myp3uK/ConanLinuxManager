package gameconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/server"
)

func testConfig(t *testing.T) server.Config {
	t.Helper()
	return server.Config{
		ID:         "srv",
		Runtime:    server.RuntimeNative,
		InstallDir: t.TempDir(),
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestApplyCreatesFileWhenGameHasNeverWrittenOne(t *testing.T) {
	cfg := testConfig(t)
	if err := Apply(cfg, Settings{PeriodicBackupInterval: 40 * time.Minute}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := read(t, Path(cfg))
	want := "[/Script/ConanSandbox.ConanGameMode]\nPeriodicBackupInterval=2400\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// The game rewrites this file on shutdown and owns everything in it except the
// keys the manager sets, so a merge that dropped content would silently reset
// the operator's server settings on the next start.
func TestApplyPreservesEverythingTheGameWrote(t *testing.T) {
	cfg := testConfig(t)
	original := strings.Join([]string{
		";METADATA=(Diff=true, UseCommands=true)",
		"[/Script/ConanSandbox.ConanGameMode]",
		"PeriodicBackupInterval=480",
		"SomeOtherKey=7",
		"",
		"[/Script/Engine.GameSession]",
		"MaxPlayers=40",
		"",
	}, "\n")
	if err := os.MkdirAll(filepath.Dir(Path(cfg)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(cfg), []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Apply(cfg, Settings{PeriodicBackupInterval: 40 * time.Minute}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := read(t, Path(cfg))
	for _, keep := range []string{";METADATA=(Diff=true, UseCommands=true)", "SomeOtherKey=7", "[/Script/Engine.GameSession]", "MaxPlayers=40"} {
		if !strings.Contains(got, keep) {
			t.Errorf("merge dropped %q\n%s", keep, got)
		}
	}
	if !strings.Contains(got, "PeriodicBackupInterval=2400") {
		t.Errorf("value not applied\n%s", got)
	}
	if strings.Contains(got, "PeriodicBackupInterval=480") {
		t.Errorf("old value still present\n%s", got)
	}
}

// A same-named key in another section belongs to another class. Writing it
// would change an unrelated setting.
func TestApplyIgnoresTheSameKeyInAnotherSection(t *testing.T) {
	cfg := testConfig(t)
	original := "[ServerSettings]\nPeriodicBackupInterval=60\n"
	if err := os.MkdirAll(filepath.Dir(Path(cfg)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(cfg), []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Apply(cfg, Settings{PeriodicBackupInterval: 5 * time.Minute}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := read(t, Path(cfg))
	if !strings.Contains(got, "[ServerSettings]\nPeriodicBackupInterval=60") {
		t.Errorf("foreign section was modified\n%s", got)
	}
	if !strings.Contains(got, "[/Script/ConanSandbox.ConanGameMode]\nPeriodicBackupInterval=300") {
		t.Errorf("own section missing\n%s", got)
	}
}

func TestApplyInsertsIntoAnExistingButEmptySection(t *testing.T) {
	cfg := testConfig(t)
	original := "[/Script/ConanSandbox.ConanGameMode]\n\n[Other]\nKey=1\n"
	if err := os.MkdirAll(filepath.Dir(Path(cfg)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(cfg), []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Apply(cfg, Settings{PeriodicBackupInterval: time.Hour}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := read(t, Path(cfg))
	want := "[/Script/ConanSandbox.ConanGameMode]\nPeriodicBackupInterval=3600\n\n[Other]\nKey=1\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestApplyKeepsCarriageReturns(t *testing.T) {
	cfg := testConfig(t)
	original := "[/Script/ConanSandbox.ConanGameMode]\r\nPeriodicBackupInterval=480\r\n"
	if err := os.MkdirAll(filepath.Dir(Path(cfg)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(cfg), []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Apply(cfg, Settings{PeriodicBackupInterval: 10 * time.Minute}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := read(t, Path(cfg))
	if got != "[/Script/ConanSandbox.ConanGameMode]\r\nPeriodicBackupInterval=600\r\n" {
		t.Fatalf("line endings not preserved: %q", got)
	}
}

// Start runs on every boot, so a no-op apply must not rewrite the file and must
// not leave a temporary behind for the game to trip over.
func TestApplyIsIdempotentAndLeavesNoTemporary(t *testing.T) {
	cfg := testConfig(t)
	settings := Settings{PeriodicBackupInterval: 40 * time.Minute}
	if err := Apply(cfg, settings); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	first := read(t, Path(cfg))
	info, err := os.Stat(Path(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(cfg, settings); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if second := read(t, Path(cfg)); second != first {
		t.Errorf("second apply changed the file:\n%q\n%q", first, second)
	}
	again, err := os.Stat(Path(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if !again.ModTime().Equal(info.ModTime()) {
		t.Errorf("unchanged configuration was rewritten")
	}
	entries, err := os.ReadDir(filepath.Dir(Path(cfg)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("temporary left behind: %s", entry.Name())
		}
	}
}

func TestApplyWritesNothingWhenUnmanaged(t *testing.T) {
	cfg := testConfig(t)
	if err := Apply(cfg, Settings{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(Path(cfg)); !os.IsNotExist(err) {
		t.Fatalf("unmanaged settings created %s (%v)", Path(cfg), err)
	}
	if Describe(Settings{}) != "unmanaged" {
		t.Errorf("Describe = %q", Describe(Settings{}))
	}
}

// A Windows build under Wine reads WindowsServer even though the manager runs
// on Linux, so the directory follows the runtime and not the host.
func TestPathFollowsTheRuntimeNotTheHost(t *testing.T) {
	for runtime, want := range map[server.Runtime]string{
		server.RuntimeNative: "LinuxServer",
		server.RuntimeWine:   "WindowsServer",
		server.RuntimeProton: "WindowsServer",
	} {
		cfg := server.Config{InstallDir: "/srv/game", Runtime: runtime}
		if got := filepath.Base(filepath.Dir(Path(cfg))); got != want {
			t.Errorf("runtime %q: got %q, want %q", runtime, got, want)
		}
	}
}

func TestSettingsForMapsSeconds(t *testing.T) {
	settings := SettingsFor(server.Config{PeriodicBackupIntervalSeconds: 2400})
	if settings.PeriodicBackupInterval != 40*time.Minute {
		t.Fatalf("got %v", settings.PeriodicBackupInterval)
	}
	if !settings.Managed() {
		t.Fatal("expected managed settings")
	}
	if SettingsFor(server.Config{}).Managed() {
		t.Fatal("zero interval must stay unmanaged")
	}
}
