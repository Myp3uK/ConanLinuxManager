package steamcmd

import (
	"os"
	"path/filepath"
	"testing"
)

// Trimmed from real `app_info_print 443030` output on the Linux test host.
const appInfo = `Redirecting stderr to '/var/lib/conan-manager/Steam/logs/stderr.txt'
Logging in using anonymous Steam account ...
Waiting for user info...OK
AppID : 443030, change number : 33012345/0, last change : Mon Sep 21 04:13:35 2026
"443030"
{
	"common"
	{
		"name"		"Conan Exiles Dedicated Server"
	}
	"depots"
	{
		"443031"
		{
			"manifests"
			{
				"public"
				{
					"gid"		"123"
				}
			}
		}
		"branches"
		{
			"public"
			{
				"buildid"		"25356024"
				"timeupdated"		"1789652697"
			}
			"conan-exiles-legacy"
			{
				"buildid"		"24269196"
				"description"		"UE4 version of Conan Exiles Dedicated Server."
			}
		}
	}
}
`

func TestLatestBuildIsReadPerBranch(t *testing.T) {
	for branch, want := range map[string]string{"public": "25356024", "conan-exiles-legacy": "24269196"} {
		got, err := buildFromAppInfo(appInfo, branch)
		if err != nil || got != want {
			t.Errorf("%s: got %q %v, want %s", branch, got, err, want)
		}
	}
	if _, err := buildFromAppInfo(appInfo, "no-such-branch"); err == nil {
		t.Error("unknown branch accepted")
	}
	if _, err := buildFromAppInfo("Login Failure: No Connection", "public"); err == nil {
		t.Error("output without app info accepted")
	}
}

func TestInstalledBuildReadsTheManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "steamapps"), 0o750); err != nil {
		t.Fatal(err)
	}
	manifest := "\"AppState\"\n{\n\t\"appid\"\t\t\"443030\"\n\t\"StateFlags\"\t\t\"4\"\n\t\"buildid\"\t\t\"25356024\"\n\t\"TargetBuildID\"\t\t\"25356024\"\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "steamapps", "appmanifest_443030.acf"), []byte(manifest), 0o640); err != nil {
		t.Fatal(err)
	}
	got, err := InstalledBuild(dir)
	if err != nil || got != "25356024" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := InstalledBuild(t.TempDir()); err == nil {
		t.Fatal("missing manifest accepted")
	}
}
