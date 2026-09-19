package tmpfs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Manager struct{}

// Prepare creates a mount point. Mounting is kept outside HTTP handlers and is
// intended to be performed by a privileged systemd helper.
func (Manager) Prepare(path string, sizeMB int) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("tmpfs path must be absolute")
	}
	if sizeMB < 128 {
		return fmt.Errorf("tmpfs size must be at least 128 MB")
	}
	return os.MkdirAll(path, 0o750)
}

func (Manager) IsMounted(path string) (bool, error) {
	clean, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, fmt.Errorf("read mountinfo: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		separator := strings.Index(line, " - ")
		if separator < 0 {
			continue
		}
		fields := strings.Fields(line[:separator])
		if len(fields) < 5 {
			continue
		}
		mountPoint := strings.ReplaceAll(fields[4], `\040`, " ")
		mountPoint = strings.ReplaceAll(mountPoint, `\011`, "\t")
		if mountPoint != clean {
			continue
		}
		post := strings.Fields(line[separator+3:])
		return len(post) > 0 && post[0] == "tmpfs", nil
	}
	return false, nil
}
