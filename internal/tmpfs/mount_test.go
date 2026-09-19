package tmpfs

import (
	"runtime"
	"testing"
)

func TestMountInfoPlatform(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("mountinfo is covered on Ubuntu smoke tests")
	}
	if _, err := (Manager{}).IsMounted("/tmp"); err == nil {
		t.Fatal("expected mountinfo error outside Linux")
	}
}
