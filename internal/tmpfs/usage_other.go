//go:build !linux

package tmpfs

import "fmt"

// UsedRatio is unavailable off Linux, where the RAM disk is not supported.
func (Manager) UsedRatio(path string) (float64, error) {
	return 0, fmt.Errorf("RAM disk usage is only available on Linux")
}
