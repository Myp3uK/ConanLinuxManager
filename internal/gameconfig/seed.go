package gameconfig

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

// LogCategories are the log categories a fresh server starts with and their
// levels, as the Windows Dedicated Server Launcher writes them: the game's
// defaults log so much that ConanSandbox.log grows by gigabytes. Levels from
// quietest: None, Fatal, Error, Warning, Display, Log, Verbose, VeryVerbose.
var LogCategories = [][2]string{
	{"Gamecode_AI", "Error"}, {"Gamecode_NPC", "Error"}, {"Gamecode_Effects", "Error"}, {"Network", "Error"},
	{"SmokeTest", "Error"}, {"AI", "Error"}, {"LogAnimation", "Error"}, {"LogDataTable", "Error"},
	{"LogStreamableManager", "Error"}, {"ConanSandbox", "Warning"}, {"NPC", "Fatal"}, {"Modding", "Warning"},
	{"LogScriptCore", "Error"}, {"LogRep", "Error"}, {"ConanBattlePass", "Error"}, {"ItemInventory", "Fatal"},
	{"LogTextLocalizationManager", "Error"}, {"Combat", "Error"}, {"building", "Error"}, {"LogModController", "Error"},
	{"GlobalServerChannel", "Warning"}, {"LogPhysics", "Error"}, {"PIE", "Error"}, {"LogCharacterMovement", "Fatal"},
	{"LogNetPackageMap", "Warning"}, {"LogDreamworld", "Warning"}, {"LogHttp", "Warning"},
	{"LogMobilePatchingUtils", "Fatal"}, {"LogBlueprintUserMessage", "Fatal"}, {"Persistence", "Warning"},
	{"LogSkinnedMeshComp", "Warning"}, {"LogGameTweakSystem", "Warning"}, {"LogLevelStreaming", "Error"},
	{"thrall", "Error"}, {"LogScript", "Error"}, {"LogNet", "Error"}, {"LogIrisReferences", "Error"},
	{"LogRcon", "Error"}, {"LogIris", "Error"},
}

// iniHeader is the line the game itself puts at the top of every config file
// it writes. A seeded file carries it too, so the game is handed a file in the
// shape it writes rather than one it has to make sense of.
const iniHeader = ";METADATA=(Diff=true, UseCommands=true)\n"

// seeds are the files a new server starts with. Ports are left out: the
// manager passes them on the command line, and a copy here would go stale
// when they change. ServerSettings.ini is written by the game in full on
// its first stop; the seed holds only what the panel shows.
func seeds(cfg server.Config) map[string]string {
	var logs strings.Builder
	for _, c := range LogCategories {
		logs.WriteString(c[0] + "=" + c[1] + "\n")
	}
	return map[string]string{
		// The join password lives in ServerSettings.ini, which is where the
		// panel writes it; a second empty copy here would be one place too
		// many to look when it does not take effect.
		"Engine": iniHeader + "[OnlineSubsystem]\nServerName=" + oneLine(cfg.Name) + "\n\n" +
			"[/Script/OnlineSubsystemUtils.IpNetDriver]\nNetServerMaxTickRate=30\n\n" +
			"[Core.Log]\n" + logs.String(),
		"Game": iniHeader + "[RconPlugin]\nRconEnabled=False\nRconPort=25575\nRconMaxKarma=60\nRconMessageMethod=0\nRconPassword=\n\n" +
			"[/Script/Engine.GameSession]\nMaxPlayers=40\n",
		"ServerSettings": iniHeader + "[ServerSettings]\nAdminPassword=\nserverRegion=0\nPVPEnabled=False\n" +
			"IsBattlEyeEnabled=False\nIsVACEnabled=False\n",
	}
}

func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// Seed writes the files a server does not have yet, so the settings and the
// log levels apply from the first start, and returns the files it wrote.
// Existing files are never touched.
func Seed(cfg server.Config) ([]string, error) {
	var written []string
	for _, file := range []string{"Engine", "Game", "ServerSettings"} {
		path, err := FilePath(cfg, file)
		if err != nil {
			return written, err
		}
		if _, err := os.Stat(path); err == nil || !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return written, err
		}
		if err := writeFileAtomic(path, []byte(seeds(cfg)[file])); err != nil {
			return written, err
		}
		written = append(written, file)
	}
	return written, nil
}
