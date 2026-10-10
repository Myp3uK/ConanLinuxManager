package version

import "testing"

func TestNewerOrdersReleases(t *testing.T) {
	cases := []struct {
		candidate, current string
		want               bool
	}{
		{"v0.2.0", "v0.1.0", true},
		{"v0.1.1", "v0.1.0", true},
		{"v1.0.0", "v0.9.9", true},
		{"v0.1.0", "v0.1.0", false},
		{"v0.1.0", "v0.2.0", false},
		// A missing part is a zero, so these name the same release.
		{"v0.2", "v0.2.0", false},
		{"v0.2.0", "v0.2", false},
		{"v0.2.1", "v0.2", true},
		// Ten is after nine, which string comparison gets wrong.
		{"v0.10.0", "v0.9.0", true},
		{"v0.9.0", "v0.10.0", false},
		// Nothing is newer than a build the manager cannot place.
		{"v1.0.0", "dev", false},
		{"dev", "v1.0.0", false},
		{"v1.0.0-rc1", "v0.9.0", false},
		{"", "v1.0.0", false},
		{"v1.0.0", "", false},
		{"not-a-tag", "v1.0.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.candidate, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.candidate, c.current, got, c.want)
		}
	}
}

func TestReleasedOnlyForATaggedBuild(t *testing.T) {
	saved := Version
	defer func() { Version = saved }()
	for tag, want := range map[string]bool{
		"v0.1.0":       true,
		"0.1.0":        true,
		"dev":          false,
		"":             false,
		"v0.1.0-dirty": false,
		// git describe on a commit after the tag; not a release either.
		"v0.1.0-3-gabc1234": false,
		"abc1234":           false,
	} {
		Version = tag
		if got := Released(); got != want {
			t.Errorf("Released() with %q = %v, want %v", tag, got, want)
		}
	}
}

func TestStringKeepsWhatWasStamped(t *testing.T) {
	saved := [3]string{Version, Commit, Date}
	defer func() { Version, Commit, Date = saved[0], saved[1], saved[2] }()
	Version, Commit, Date = "v0.1.0", "abc1234", "2026-10-10"
	if got, want := String(), "v0.1.0 (abc1234, 2026-10-10)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	Date = ""
	if got, want := String(), "v0.1.0 (abc1234)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	Commit = ""
	if got, want := String(), "v0.1.0"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
