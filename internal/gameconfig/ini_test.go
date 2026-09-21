package gameconfig

import (
	"strings"
	"testing"
)

const serverSettings = ";METADATA=(Diff=true, UseCommands=true)\n" +
	"[ServerSettings]\n" +
	"MaxNudity=0\n" +
	"; a comment the operator wrote\n" +
	"PlayerDamageMultiplier=1\n" +
	"BuildingPVPWhitelist=(\"80901\",\"80111\")\n" +
	"\n" +
	"[/Script/Engine.GameSession]\n" +
	"+Banned=one\n" +
	"MaxPlayers=40\n"

func TestEditChangesOnlyWhatWasAsked(t *testing.T) {
	out, changes, err := Edit(serverSettings, map[string]map[string]string{
		"ServerSettings": {"PlayerDamageMultiplier": "2.5", "NewKey": "7"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(serverSettings, "PlayerDamageMultiplier=1\n", "PlayerDamageMultiplier=2.5\n", 1)
	want = strings.Replace(want, "BuildingPVPWhitelist=(\"80901\",\"80111\")\n", "BuildingPVPWhitelist=(\"80901\",\"80111\")\nNewKey=7\n", 1)
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
	if len(changes) != 2 {
		t.Fatalf("changes: %+v", changes)
	}
}

func TestEditMatchesKeysWithoutRegardToCase(t *testing.T) {
	out, changes, err := Edit(serverSettings, map[string]map[string]string{"ServerSettings": {"maxnudity": "2"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "MaxNudity=2") || strings.Contains(out, "maxnudity") || changes[0].Old != "0" {
		t.Fatalf("case-insensitive replace failed:\n%s %+v", out, changes)
	}
}

func TestEditAddsAMissingSection(t *testing.T) {
	out, _, err := Edit(serverSettings, map[string]map[string]string{"/Script/ConanSandbox.ConanGameMode": {"PeriodicBackupInterval": "2400"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "[/Script/ConanSandbox.ConanGameMode]\nPeriodicBackupInterval=2400\n") {
		t.Fatalf("section not appended:\n%q", out)
	}
	if !strings.Contains(out, "MaxPlayers=40\n\n[/Script/ConanSandbox") {
		t.Fatalf("no blank line before the new section:\n%q", out)
	}
}

func TestEditRemovesKeys(t *testing.T) {
	out, changes, err := Edit(serverSettings, nil, map[string][]string{"ServerSettings": {"MaxNudity"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "MaxNudity") || len(changes) != 1 || !changes[0].Removed {
		t.Fatalf("remove failed:\n%s %+v", out, changes)
	}
}

// Setting one value of an array would silently drop the others.
func TestEditRefusesToGuessAtArrays(t *testing.T) {
	content := "[S]\nItem=a\nItem=b\n"
	if _, _, err := Edit(content, map[string]map[string]string{"S": {"Item": "c"}}, nil); err == nil {
		t.Fatal("array value replaced")
	}
}

func TestEditRejectsInjection(t *testing.T) {
	for _, set := range []map[string]map[string]string{
		{"ServerSettings": {"Key": "1\n[Other]\nAdminPassword=x"}},
		{"ServerSettings": {"Bad=Key": "1"}},
		{"Bad]Section": {"Key": "1"}},
	} {
		if _, _, err := Edit(serverSettings, set, nil); err == nil {
			t.Errorf("accepted %v", set)
		}
	}
}

func TestEditKeepsWindowsLineEndings(t *testing.T) {
	content := "[S]\r\nA=1\r\n"
	out, _, err := Edit(content, map[string]map[string]string{"S": {"A": "2", "B": "3"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "[S]\r\nA=2\r\nB=3\r\n" {
		t.Fatalf("line endings: %q", out)
	}
}

func TestParseListsSectionsInOrder(t *testing.T) {
	sections := Parse(serverSettings)
	if len(sections) != 2 || sections[0].Name != "ServerSettings" || len(sections[0].Entries) != 3 || sections[1].Entries[0].Key != "+Banned" {
		t.Fatalf("parse: %+v", sections)
	}
}
