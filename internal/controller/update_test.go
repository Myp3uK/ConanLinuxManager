package controller

import "testing"

// TestAnUninstalledGameIsNotAnUpdate: a server with no game at all filled the
// event log with "Найдено обновление игры", which pointed at the wrong button.
// Whether the build is current is the caller's question; this one is only
// whether there is an installation to speak of.
func TestAnUninstalledGameIsNotAnUpdate(t *testing.T) {
	cases := map[string]struct {
		status UpdateStatus
		want   bool
	}{
		"installed and behind": {UpdateStatus{Installed: "25000000", Latest: "25639945", Available: true}, true},
		"never installed":      {UpdateStatus{Installed: "", Latest: "25639945", Available: true}, false},
	}
	for name, c := range cases {
		if got := worthAnnouncing(c.status); got != c.want {
			t.Errorf("%s: worthAnnouncing = %v, want %v", name, got, c.want)
		}
	}
}
