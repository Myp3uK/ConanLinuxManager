package mods

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

func testConfig(dir string) server.Config {
	return server.Config{ID: "alpha", Name: "Alpha", Engine: server.EngineUE5, Runtime: server.RuntimeNative,
		InstallDir: dir, SteamCMDDir: filepath.Join(dir, "steamcmd"), GamePort: 7777, QueryPort: 27015}
}

func TestRenderWritesEnabledModsInOrder(t *testing.T) {
	cfg := testConfig("/srv/alpha")
	got := Render(cfg, []server.Mod{
		{WorkshopID: "222222", Position: 1, Enabled: true, Paks: []string{"Second.pak"}},
		{WorkshopID: "111111", Position: 0, Enabled: true, Paks: []string{"First.pak", "FirstExtra.pak"}},
		{WorkshopID: "333333", Position: 2, Enabled: false, Paks: []string{"Disabled.pak"}},
		{WorkshopID: "444444", Position: 3, Enabled: true},
	})
	want := "/srv/alpha/ConanSandbox/Mods/111111/First.pak\n" +
		"/srv/alpha/ConanSandbox/Mods/111111/FirstExtra.pak\n" +
		"/srv/alpha/ConanSandbox/Mods/222222/Second.pak\n"
	if got != want {
		t.Errorf("Render() =\n%q\nwant\n%q", got, want)
	}
}

func TestParseListReadsTheFormsOperatorsWrite(t *testing.T) {
	entries := ParseList("# a comment\n" +
		"\n" +
		"/home/steam/.steam/steamapps/workshop/content/440900/1159180273/Pippi.pak\n" +
		"*2050780234/LBPR.pak\r\n" +
		"Local.pak\n" +
		"  /srv/alpha/ConanSandbox/Mods/880454836/AoC.pak  \n")
	if len(entries) != 4 {
		t.Fatalf("got %d entries: %+v", len(entries), entries)
	}
	for i, want := range []Entry{
		{WorkshopID: "1159180273", Pak: "Pippi.pak"},
		{WorkshopID: "2050780234", Pak: "LBPR.pak"},
		{WorkshopID: "", Pak: "Local.pak"},
		{WorkshopID: "880454836", Pak: "AoC.pak"},
	} {
		if entries[i].WorkshopID != want.WorkshopID || entries[i].Pak != want.Pak {
			t.Errorf("entry %d = %+v; want id %q pak %q", i, entries[i], want.WorkshopID, want.Pak)
		}
	}
	if entries[3].Raw != "/srv/alpha/ConanSandbox/Mods/880454836/AoC.pak" {
		t.Errorf("Raw kept surrounding space: %q", entries[3].Raw)
	}
}

func TestParseListIgnoresDirectoriesThatOnlyLookLikeIDs(t *testing.T) {
	entries := ParseList("/srv/alpha/mods/2024/Winter.pak\n")
	if len(entries) != 1 || entries[0].WorkshopID != "" {
		t.Fatalf("a four-digit directory is not a Workshop id: %+v", entries)
	}
}

func TestApplyWritesOnlyWhenTheListChanges(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	list := []server.Mod{{WorkshopID: "111111", Enabled: true, Paks: []string{"First.pak"}}}
	previous, changed, err := Apply(cfg, list)
	if err != nil || !changed || previous != "" {
		t.Fatalf("first write: %q, %v, %v", previous, changed, err)
	}
	info, err := os.Stat(ListPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	before := info.ModTime()
	previous, changed, err = Apply(cfg, list)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("an unchanged list should not be rewritten")
	}
	if previous != Render(cfg, list) {
		t.Errorf("previous = %q", previous)
	}
	if info, err = os.Stat(ListPath(cfg)); err != nil || !info.ModTime().Equal(before) {
		t.Errorf("modification time changed: %v, %v", err, info.ModTime())
	}
	// The content a change replaces comes back, so the caller can keep it as a
	// version the operator can return to.
	previous, changed, err = Apply(cfg, append(list, server.Mod{WorkshopID: "222222", Position: 1, Enabled: true, Paks: []string{"Second.pak"}}))
	if err != nil || !changed {
		t.Fatalf("second write: %v, %v", changed, err)
	}
	if previous != Render(cfg, list) {
		t.Errorf("previous content lost: %q", previous)
	}
}

func TestPakNamesListsOnlyPaks(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	item := ItemDir(cfg, "111111")
	if err := os.MkdirAll(filepath.Join(item, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Beta.pak", "Alpha.PAK", "readme.txt"} {
		if err := os.WriteFile(filepath.Join(item, name), []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	paks, err := PakNames(item)
	if err != nil {
		t.Fatal(err)
	}
	if len(paks) != 2 || paks[0] != "Alpha.PAK" || paks[1] != "Beta.pak" {
		t.Fatalf("unexpected paks: %v", paks)
	}
}
