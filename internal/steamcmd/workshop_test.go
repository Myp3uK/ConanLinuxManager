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

func TestParseWorkshopState(t *testing.T) {
	acf := `"AppWorkshop"
{
	"appid"		"440900"
	"SizeOnDisk"	"123456"
	"WorkshopItemsInstalled"
	{
		"1159180273"
		{
			"size"		"48271360"
			"timeupdated"	"1756000000"
			"manifest"	"7382658243512"
		}
		"2050780234"
		{
			"size"		"1024"
			"timeupdated"	"1700000000"
		}
	}
	"WorkshopItemDetails"
	{
		"1159180273"
		{
			"manifest"	"7382658243512"
			"timeupdated"	"1756000000"
			"timetouched"	"1757000000"
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
	pippi := items["1159180273"]
	if !pippi.Updated.Equal(time.Unix(1756000000, 0).UTC()) {
		t.Errorf("Updated = %v", pippi.Updated)
	}
	if pippi.Size != 48271360 || pippi.Manifest != "7382658243512" {
		t.Errorf("unexpected item: %+v", pippi)
	}
	if items["2050780234"].Manifest != "" {
		t.Errorf("manifest invented: %+v", items["2050780234"])
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
