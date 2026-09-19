// conan-recover is deliberately offline: stop the manager and Conan first.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/example/conan-manager/internal/ramstate"
	"github.com/example/conan-manager/internal/server"
	"github.com/example/conan-manager/internal/sessionlock"
)

func main() {
	id := flag.String("server", "", "server ID from session manifest")
	install := flag.String("install-dir", "", "absolute server installation directory")
	ram := flag.String("ram-dir", "", "absolute RAM directory from server configuration")
	source := flag.String("source", "", "ram or baseline (baseline discards session progress)")
	flag.Parse()
	if err := run(*id, *install, *ram, *source); err != nil {
		fmt.Fprintln(os.Stderr, "Recovery failed:", err)
		os.Exit(1)
	}
	fmt.Println("Recovery complete: SQLite integrity OK, WAL checkpointed, Saved restored; session archived.")
}

func run(id, install, ram, source string) error {
	if id == "" || !filepath.IsAbs(install) || !filepath.IsAbs(ram) {
		return fmt.Errorf("server, absolute install-dir and ram-dir are required")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return fmt.Errorf("run offline as root on Linux")
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		exe, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot inspect process %d: %w", pid, err)
		}
		name := strings.ToLower(filepath.Base(exe))
		if strings.Contains(name, "conan-manager") || strings.Contains(name, "conansandboxserver") {
			return fmt.Errorf("stop manager and Conan before recovery: process %d (%s)", pid, name)
		}
	}
	cfg := server.Config{ID: id, InstallDir: filepath.Clean(install), RamdiskPath: filepath.Clean(ram), RamdiskEnabled: true}
	lease, err := sessionlock.Acquire(cfg.InstallDir, cfg.RamdiskPath)
	if err != nil {
		return err
	}
	defer lease.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return (ramstate.Manager{}).Recover(ctx, cfg, source)
}
