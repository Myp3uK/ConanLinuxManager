package steamcmd

import (
	"strings"
	"testing"
)

func TestWorkshopArgsPutEveryItemInOneRun(t *testing.T) {
	ids := []string{"111111", "222222", "333333"}
	args := strings.Join(workshopArgs("/cache", ids, false), " ")
	// Starting SteamCMD costs more than most mods take to download - it
	// updates itself, connects and logs in every time - so the whole set has
	// to go into one command line.
	if n := strings.Count(args, "+workshop_download_item"); n != 3 {
		t.Errorf("got %d download commands, want 3: %s", n, args)
	}
	if strings.Count(args, "+login") != 1 || strings.Count(args, "+quit") != 1 {
		t.Errorf("one run means one login and one quit: %s", args)
	}
	for _, id := range ids {
		if !strings.Contains(args, "440900 "+id) {
			t.Errorf("missing %s: %s", id, args)
		}
	}
	if strings.Contains(args, "validate") {
		t.Errorf("validate should be off unless it is asked for: %s", args)
	}
	if !strings.Contains(strings.Join(workshopArgs("/cache", ids, true), " "), "440900 111111 validate") {
		t.Error("validate should follow the item it applies to")
	}
}

// The fixture is shaped like a real run: colour codes sit in the middle of the
// words and the progress lines end in carriage returns.
const downloadOutput = "Loading Steam API...\x1b[0mOK\n" +
	"\x1b[0m\"@sSteamCmdForcePlatformType\" = \"windows\"\n" +
	"Downloading item 880454836 ...\r" +
	"\x1b[0mSuccess. Downloaded item 880454836 to \"/cache/steamapps/workshop/content/440900/880454836\" (26668482 bytes)\n" +
	"Downloading item 111111 ...\x1b[0m\n" +
	"ERROR! Download item 111111 failed (Failure).\n" +
	"Downloading item 222222 ...\n" +
	"ERROR! Download item 222222 failed (File Not Found)."

func TestDownloadReaderReadsEachItemsOutcome(t *testing.T) {
	var reader downloadReader
	// Written in small pieces, because a result can straddle two writes and
	// must not then be read as two broken ones.
	for i := 0; i < len(downloadOutput); i += 7 {
		end := i + 7
		if end > len(downloadOutput) {
			end = len(downloadOutput)
		}
		if _, err := reader.Write([]byte(downloadOutput[i:end])); err != nil {
			t.Fatal(err)
		}
	}
	// The last line has no line break; without a flush it would be lost.
	reader.flush()
	if got := reader.results["880454836"]; got.Dir != "/cache/steamapps/workshop/content/440900/880454836" || got.Reason != "" {
		t.Errorf("downloaded item: %+v", got)
	}
	if got := reader.results["111111"]; got.Reason != "Failure" || got.Dir != "" {
		t.Errorf("failed item: %+v", got)
	}
	if got := reader.results["222222"]; got.Reason != "File Not Found" {
		t.Errorf("refused item: %+v", got)
	}
	if len(reader.results) != 3 {
		t.Errorf("got %d results: %+v", len(reader.results), reader.results)
	}
}

func TestDownloadReaderIgnoresWhatIsNotAResult(t *testing.T) {
	var reader downloadReader
	if _, err := reader.Write([]byte("Downloading item 999 ...\nUpdate state (0x61) downloading, progress: 10.00\n")); err != nil {
		t.Fatal(err)
	}
	reader.flush()
	if len(reader.results) != 0 {
		t.Errorf("a mod being downloaded is not a result yet: %+v", reader.results)
	}
}
