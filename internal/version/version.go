// Package version reports which build of the manager is running, and compares
// release tags so the panel can tell whether what is published is newer than
// what is installed.
package version

import (
	"strconv"
	"strings"
)

// These are stamped at build time by scripts/build.sh. A plain "go build"
// leaves them as they are, and the build then calls itself "dev": the panel
// offers no self-update for such a build, because there is nothing to compare
// it against.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// Released reports whether this build names a release tag. A build from a
// working tree says "dev", or a tag with "-dirty" appended, and neither can be
// compared with what is published.
func Released() bool {
	return Version != "" && Version != "dev" && !strings.HasSuffix(Version, "-dirty") && Parse(Version) != nil
}

// String names the build in one line, with whatever parts were stamped.
func String() string {
	out := Version
	switch {
	case Commit != "" && Date != "":
		out += " (" + Commit + ", " + Date + ")"
	case Commit != "":
		out += " (" + Commit + ")"
	}
	return out
}

// Parse reads a tag such as "v1.2.3" or "1.2" into its numbers. Anything that
// is not a plain number sequence - "dev", "v1.2.3-rc1", a commit hash - gives
// nil, which keeps the manager from guessing at an order it cannot know.
func Parse(tag string) []int {
	tag = strings.TrimSpace(tag)
	tag = strings.TrimPrefix(tag, "v")
	if tag == "" {
		return nil
	}
	parts := strings.Split(tag, ".")
	if len(parts) > 4 {
		return nil
	}
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// Newer reports whether candidate is a later release than current. Either one
// being unreadable gives false: an update is offered only when the manager is
// sure, because the answer sends it to replace its own binary.
func Newer(candidate, current string) bool {
	a, b := Parse(candidate), Parse(current)
	if a == nil || b == nil {
		return false
	}
	// A missing part is a zero, so v1.2 and v1.2.0 are the same release.
	for i := 0; i < len(a) || i < len(b); i++ {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}
