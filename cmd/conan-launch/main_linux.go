package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/Myp3uK/ConanLinuxManager/internal/bindmount"
	"github.com/Myp3uK/ConanLinuxManager/internal/ramstate"
	"github.com/Myp3uK/ConanLinuxManager/internal/server"
	"github.com/Myp3uK/ConanLinuxManager/internal/sessionlock"
	"golang.org/x/sys/unix"
)

type configuration struct {
	ServerID   string   `json:"server_id"`
	Install    string   `json:"install_dir"`
	RAM        string   `json:"ram_dir"`
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
	UID        uint32   `json:"uid"`
	GID        uint32   `json:"gid"`
}

func checkedPath(path string, directory bool) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, fmt.Errorf("absolute clean path required: %q", path)
	}
	flags := uint64(unix.O_RDONLY | unix.O_CLOEXEC)
	if directory {
		flags |= unix.O_DIRECTORY
	}
	return unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: flags, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
}

func loadConfig(path string) (configuration, error) {
	var c configuration
	fd, err := checkedPath(path, false)
	if err != nil {
		return c, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return c, err
	}
	if st.Uid != 0 || st.Mode&0022 != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return c, fmt.Errorf("config must be a root-owned regular file without group/other write access")
	}
	// Parent replacement must also require root authority.
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		if err := unix.Stat(p, &st); err != nil {
			return c, err
		}
		if st.Uid != 0 || st.Mode&0022 != 0 {
			return c, fmt.Errorf("untrusted config parent: %s", p)
		}
		if p == "/" {
			break
		}
	}
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return c, fmt.Errorf("trailing configuration data")
	}
	if c.ServerID == "" || c.UID == 0 || c.GID == 0 {
		return c, fmt.Errorf("server_id and non-root uid/gid required")
	}
	return c, nil
}

func run(path string, worker bool) error {
	// Linux parent-death signals track the spawning OS thread, not merely
	// the Go process. Keep that thread alive for each child's full lifetime.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if os.Getuid() != 0 || os.Geteuid() != 0 {
		return fmt.Errorf("root only; setuid invocation unsupported")
	}
	c, err := loadConfig(path)
	if err != nil {
		return err
	}
	if !worker {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		cmd := exec.Command(exe, "--worker", "--config", path)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNS | unix.CLONE_NEWPID, Pdeathsig: syscall.SIGKILL}
		signals := make(chan os.Signal, 8)
		signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
		defer signal.Stop(signals)
		if err := cmd.Start(); err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		for {
			select {
			case err := <-done:
				return err
			case s := <-signals:
				_ = cmd.Process.Signal(s)
			}
		}
	}
	if os.Getpid() != 1 {
		return fmt.Errorf("worker must be PID 1 in its dedicated namespace")
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return err
	}
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	cfg := server.Config{ID: c.ServerID, InstallDir: c.Install, RamdiskPath: c.RAM, RamdiskEnabled: true}
	layout := ramstate.LayoutFor(cfg)
	lease, err := sessionlock.Acquire(c.Install, c.RAM)
	if err != nil {
		return err
	}
	defer lease.Close()
	// Directory locks avoid replaceable lock files and cover aliases by inode.
	var locks []int
	defer func() {
		for _, fd := range locks {
			unix.Close(fd)
		}
	}()
	for _, p := range []string{c.Install, c.RAM, layout.PersistentSaved, layout.RamDatabase} {
		fd, err := checkedPath(p, true)
		if err != nil {
			return err
		}
		locks = append(locks, fd)
		var fs unix.Statfs_t
		if err := unix.Fstatfs(fd, &fs); err != nil {
			return err
		}
		isRAM := p == c.RAM || p == layout.RamDatabase
		if (fs.Type == unix.TMPFS_MAGIC) != isRAM {
			return fmt.Errorf("unexpected filesystem for %s", p)
		}
	}
	fd, err := checkedPath(c.Executable, false)
	if err != nil {
		return err
	}
	unix.Close(fd)
	select {
	case <-signals:
		return fmt.Errorf("cancelled before session creation")
	default:
	}
	if err := (ramstate.Manager{}).LoadBind(cfg); err != nil {
		return err
	}
	for _, name := range []string{"game_0.db", "game_0.db-wal", "game_0.db-shm"} {
		if err := os.Chown(filepath.Join(layout.RamDatabase, name), int(c.UID), int(c.GID)); err != nil {
			return err
		}
	}
	if err := bindmount.Run("mount", layout.PersistentSaved, layout.RamDatabase); err != nil {
		return err
	}
	// Give the game a procfs scoped to this PID namespace. Remove it after
	// reaping children so bindmount can check against the host PID 1 again.
	if err := unix.Mount("proc", "/proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return err
	}
	// The child cannot regain privilege through setuid binaries or file caps.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	cmd := exec.Command(c.Executable, c.Args...)
	cmd.Dir = c.Install
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "HOME=" + c.Install}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL,
		Credential: &syscall.Credential{Uid: c.UID, Gid: c.GID, Groups: []uint32{}}}
	startErr := cmd.Start()
	var gameErr error
	intentionalStop := false
	forcedStop := false
	if startErr == nil {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var timeout <-chan time.Time
		var timer *time.Timer
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
	wait:
		for {
			select {
			case gameErr = <-done:
				break wait
			case s := <-signals:
				intentionalStop = true
				if timer == nil {
					sig := syscall.SIGTERM
					if s == syscall.SIGINT {
						sig = syscall.SIGINT
					}
					_ = unix.Kill(-cmd.Process.Pid, sig)
					timer = time.NewTimer(180 * time.Second)
					timeout = timer.C
				}
			case <-timeout:
				forcedStop = true
				_ = unix.Kill(-1, unix.SIGKILL)
				timeout = nil
			}
		}
	}
	// Kill and reap ALL remaining namespace processes, including setsid escapees,
	// before opening SQLite or copying RAM. PID 1 is excluded by kill(-1).
	if err := unix.Kill(-1, unix.SIGKILL); err != nil && err != unix.ESRCH {
		return err
	}
	for {
		_, err := unix.Wait4(-1, nil, 0, nil)
		if err == unix.ECHILD {
			break
		}
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
	}
	if err := unix.Unmount("/proc", 0); err != nil {
		return err
	}
	if err := bindmount.Run("unmount", layout.PersistentSaved, layout.RamDatabase); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := (ramstate.Manager{}).PersistBind(ctx, cfg); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Session persisted: SQLite integrity OK; RAM cleaned.")
	if startErr != nil {
		return startErr
	}
	if gameErr != nil {
		if intentionalStop && !forcedStop && expectedStopExit(gameErr) {
			return nil
		}
		return fmt.Errorf("game exited unsuccessfully (database persisted): %w", gameErr)
	}
	return nil
}

// expectedStopExit accepts only conventional exits caused by a signal that
// this launcher deliberately sent. Persist errors are returned before this
// function is reached and therefore can never be hidden.
func expectedStopExit(err error) bool {
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ProcessState == nil {
		return false
	}
	status, ok := exit.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		return false
	}
	if status.Signaled() {
		return status.Signal() == syscall.SIGTERM || status.Signal() == syscall.SIGINT || status.Signal() == syscall.SIGKILL
	}
	return status.Exited() && (status.ExitStatus() == 128+int(syscall.SIGTERM) || status.ExitStatus() == 128+int(syscall.SIGINT))
}
