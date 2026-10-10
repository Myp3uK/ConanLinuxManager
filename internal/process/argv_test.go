package process

import (
	"strings"
	"testing"

	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

func TestArgvPassesConfiguredPorts(t *testing.T) {
	cfg := server.Config{Runtime: server.RuntimeNative, InstallDir: "/srv/game", LaunchArgs: "-log", GamePort: 37777, QueryPort: 37015}
	_, args, err := Argv(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(args, " "); got != "ConanSandbox -log -Port=37777 -QueryPort=37015" {
		t.Fatalf("args: %s", got)
	}
}

// An operator who already chose a port in the launch arguments keeps it; the
// game would otherwise see the flag twice with different values.
func TestArgvKeepsPortsGivenInLaunchArgs(t *testing.T) {
	cfg := server.Config{Runtime: server.RuntimeNative, InstallDir: "/srv/game", LaunchArgs: "-log -port=7000 -QUERYPORT=7001", GamePort: 37777, QueryPort: 37015}
	_, args, err := Argv(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(args, " "); got != "ConanSandbox -log -port=7000 -QUERYPORT=7001" {
		t.Fatalf("args: %s", got)
	}
}

func TestArgvWindowsRuntimesGetPortsToo(t *testing.T) {
	cfg := server.Config{Runtime: server.RuntimeWine, InstallDir: "/srv/game", GamePort: 7777, QueryPort: 27015}
	path, args, err := Argv(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if path != "wine64" || args[len(args)-1] != "-QueryPort=27015" {
		t.Fatalf("wine argv: %s %v", path, args)
	}
}

func TestArgvLoadsTheChosenMapAndAddress(t *testing.T) {
	cfg := server.Config{Runtime: server.RuntimeNative, InstallDir: "/srv/game", LaunchArgs: "-log", GamePort: 7777, QueryPort: 27015,
		Map: "/Game/Maps/ConanSandbox/ConanSandbox", Multihome: "192.168.0.100"}
	_, args, err := Argv(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(args, " "); got != "ConanSandbox /Game/Maps/ConanSandbox/ConanSandbox -log -Port=7777 -QueryPort=27015 -MULTIHOME=192.168.0.100" {
		t.Fatalf("args: %s", got)
	}
}
