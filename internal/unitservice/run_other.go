//go:build !linux

package unitservice

import "fmt"

// RunServer needs exec(2) and systemd, so it exists only on Linux.
func RunServer(id, specDir string) error {
	return fmt.Errorf("run-server is only supported on Linux")
}
