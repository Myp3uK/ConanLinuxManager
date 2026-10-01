package steamcmd

import (
	"strings"
	"testing"
	"time"
)

func TestWorkshopArgsForceWindowsPlatform(t *testing.T) {
	args := strings.Join(workshopArgs("/var/lib/conan-manager/workshop", "1159180273"), " ")
	// Without the platform override SteamCMD on Linux is told the item has no
	// content, so this is the one argument the download cannot work without.
	if !strings.Contains(args, "+@sSteamCmdForcePlatformType windows") {
		t.Errorf("missing platform override: %s", args)
	}
	if !strings.Contains(args, "+workshop_download_item 440900 1159180273") {
		t.Errorf("mods belong to the game app, not the server app: %s", args)
	}
	if !strings.Contains(args, "+login anonymous") || !strings.HasSuffix(args, "+quit") {
		t.Errorf("unexpected command line: %s", args)
	}
	// The override has to be set before the login it affects.
	if strings.Index(args, "sSteamCmdForcePlatformType") > strings.Index(args, "+login") {
		t.Errorf("platform override comes too late: %s", args)
	}
}

// The fixture is an appworkshop_440900.acf SteamCMD actually wrote, with a
// second item added by hand: the details block carries latest_timeupdated,
// which is how SteamCMD records that a newer version exists.
func TestParseWorkshopState(t *testing.T) {
	acf := `"AppWorkshop"
{
	"appid"		"440900"
	"SizeOnDisk"		"26668482"
	"NeedsUpdate"		"0"
	"NeedsDownload"		"0"
	"TimeLastUpdated"		"0"
	"WorkshopItemsInstalled"
	{
		"880454836"
		{
			"size"		"26668482"
			"timeupdated"		"1712142752"
			"manifest"		"4900369035769746925"
		}
		"2050780234"
		{
			"size"		"1024"
			"timeupdated"		"1700000000"
		}
	}
	"WorkshopItemDetails"
	{
		"880454836"
		{
			"manifest"		"4900369035769746925"
			"timeupdated"		"1712142752"
			"timetouched"		"1790845200"
			"latest_timeupdated"		"1712142752"
			"latest_manifest"		"4900369035769746925"
		}
		"2050780234"
		{
			"manifest"		"111"
			"timeupdated"		"1700000000"
			"latest_timeupdated"		"1760000000"
			"latest_manifest"		"222"
		}
	}
}
`
	items, err := parseWorkshopState(acf)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items: %+v", len(items), items)
	}
	pippi := items["880454836"]
	if !pippi.Updated.Equal(time.Unix(1712142752, 0).UTC()) {
		t.Errorf("Updated = %v", pippi.Updated)
	}
	// The manifest is the same value the Steam API returns as hcontent_file.
	if pippi.Size != 26668482 || pippi.Manifest != "4900369035769746925" {
		t.Errorf("unexpected item: %+v", pippi)
	}
	if !pippi.LatestUpdated.Equal(pippi.Updated) || pippi.LatestManifest != pippi.Manifest {
		t.Errorf("an up-to-date item should match its latest: %+v", pippi)
	}
	stale := items["2050780234"]
	if !stale.LatestUpdated.After(stale.Updated) || stale.LatestManifest == stale.Manifest {
		t.Errorf("expected a newer version on record: %+v", stale)
	}
}

func TestParseWorkshopStateRejectsSomethingElse(t *testing.T) {
	if _, err := parseWorkshopState(`"AppState" { "appid" "443030" }`); err == nil {
		t.Error("an app manifest is not a workshop manifest")
	}
}

func TestTransientSeparatesRetryableFromSettled(t *testing.T) {
	for _, retryable := range []string{
		"ERROR! Download item 1159180273 failed (Failure).",
		"ERROR! Failed to install app '443030' (Missing configuration)",
		"ERROR! Download item 1159180273 failed (Timeout).",
	} {
		if !transient(retryable) {
			t.Errorf("should retry: %s", retryable)
		}
	}
	for _, settled := range []string{
		"ERROR! Download item 1159180273 failed (Access Denied).",
		"ERROR! Download item 1159180273 failed (File Not Found).",
		"ERROR! Failed to install app '443030' (Disk write failure)",
	} {
		if transient(settled) {
			t.Errorf("should not retry: %s", settled)
		}
	}
}
