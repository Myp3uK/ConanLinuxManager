package controller

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/example/conan-manager/internal/backup"
	"github.com/example/conan-manager/internal/events"
	"github.com/example/conan-manager/internal/gameconfig"
	"github.com/example/conan-manager/internal/hooks"
	"github.com/example/conan-manager/internal/launchservice"
	"github.com/example/conan-manager/internal/logreader"
	"github.com/example/conan-manager/internal/process"
	"github.com/example/conan-manager/internal/ramstate"
	"github.com/example/conan-manager/internal/scheduler"
	"github.com/example/conan-manager/internal/server"
	"github.com/example/conan-manager/internal/steamcmd"
	"github.com/example/conan-manager/internal/tmpfs"
	"github.com/example/conan-manager/internal/unitservice"
	"github.com/example/conan-manager/internal/watchdog"
)

type Controller struct {
	// opLocks serialise start, stop and kill per server. They used to be one
	// global mutex, so a slow hook or save on one server held up every other.
	// Servers that share directories are kept apart by reservations instead.
	opLocksMu          sync.Mutex
	opLocks            map[string]*sync.Mutex
	Repo               *server.Repository
	Processes          *process.Manager
	Launcher           *launchservice.Manager
	Logs               logreader.Reader
	Tmpfs              tmpfs.Manager
	RAM                ramstate.Manager
	Steam              Steam
	Logger             *slog.Logger
	jobs               map[string]Job
	jobsMu             sync.RWMutex
	lifecycleMu        sync.Mutex
	closing            bool
	active             map[string]*reservation
	health             map[string]Health
	AvailabilityPolicy watchdog.AvailabilityPolicy
	QueryHost          string
	BackupRetention    int
	// DrainInterval controls how often backups the game wrote into the RAM disk
	// are moved back to SSD. It only reads the RAM directory, so it is cheap and
	// must stay frequent: the RAM disk is sized for the active database alone,
	// and a large save produces backups that fill it within minutes.
	DrainInterval time.Duration
	// MaintenanceInterval controls how often old backups are pruned. That walks
	// the whole Saved tree, so it is kept off the frequent ticks.
	MaintenanceInterval time.Duration
	// RamdiskUsageLimit is the fraction of the RAM disk that, once exceeded,
	// stops the server rather than letting tmpfs fill under an active database.
	RamdiskUsageLimit float64
	// Units runs servers as systemd units that outlive the manager. Nil keeps
	// every server a child process of the manager.
	Units *unitservice.Manager
	// Events receives lifecycle notices. Nil discards them.
	Events *events.Bus
	// CrashRestartLimit crash restarts are attempted within CrashRestartWindow
	// before a server is left down; each waits CrashRestartDelay times the
	// attempt number.
	CrashRestartLimit  int
	CrashRestartWindow time.Duration
	CrashRestartDelay  time.Duration
	// StopTimeout is how long a server may take to stop before it is killed.
	// Its database is checked after every kill.
	StopTimeout time.Duration
	// Hooks runs operator scripts around lifecycle events. Nil disables them.
	Hooks *hooks.Runner
	// latest caches the newest build per SteamCMD directory and branch.
	latest latestCache
	// updating marks servers with an update job in progress.
	updating map[string]bool
	// backingUp marks servers with a backup in progress.
	backingUp map[string]bool
	// settingsMu serialises changes made to a server's manager settings while
	// it runs, which bypass the lifecycle reservation.
	settingsMu sync.Mutex
	// BackupDir holds the manager's own backups, one directory per server.
	// Empty puts them next to each server's install directory.
	BackupDir string
	// BackupKeep is how many manager backups each server keeps; 0 keeps all.
	BackupKeep int
	// detached is closed when the manager shuts down while unit servers keep
	// running, and tells their monitors to let go without touching them.
	detached   chan struct{}
	detachOnce sync.Once
}

// Steam is the part of SteamCMD the controller uses; tests replace it.
type Steam interface {
	InstallServer(ctx context.Context, cfg server.Config) error
	LatestBuild(ctx context.Context, steamcmdDir, branch string) (string, error)
	UpdateServer(ctx context.Context, steamcmdDir, installDir, branch string, validate bool, output io.Writer) error
}

type Job struct {
	ID        string    `json:"id"`
	ServerID  string    `json:"server_id"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func New(repo *server.Repository) *Controller {
	return &Controller{
		Repo:                repo,
		Processes:           process.NewManager(),
		Steam:               steamcmd.Service{},
		Logger:              slog.New(slog.DiscardHandler),
		jobs:                make(map[string]Job),
		opLocks:             make(map[string]*sync.Mutex),
		active:              make(map[string]*reservation),
		health:              make(map[string]Health),
		AvailabilityPolicy:  watchdog.DefaultAvailabilityPolicy(),
		QueryHost:           "127.0.0.1",
		DrainInterval:       5 * time.Second,
		MaintenanceInterval: time.Minute,
		RamdiskUsageLimit:   0.9,
		CrashRestartLimit:   3,
		CrashRestartWindow:  30 * time.Minute,
		CrashRestartDelay:   15 * time.Second,
		StopTimeout:         DefaultStopTimeout,
		detached:            make(chan struct{}),
	}
}

// Start is an operator or schedule request. It records that the server should
// be running, which is what brings it back after a host reboot.
func (c *Controller) Start(ctx context.Context, id string) error {
	defer c.lockServer(id)()
	if err := c.Repo.SetDesiredState(ctx, id, "running"); err != nil {
		return err
	}
	c.resetCrashes(id)
	return c.start(ctx, id)
}

// restartStopTimeout bounds how long a restart waits for the world to be saved.
// It is longer than the stop supervisor's hard limit, which ends any stop that
// takes longer; a shorter wait gave up on large worlds and left the server
// stopped, because the stop had already been recorded as the operator's wish.
const restartStopTimeout = 40 * time.Minute

func (c *Controller) Restart(ctx context.Context, id string) error {
	if err := c.Stop(ctx, id); err != nil {
		return err
	}
	deadline := time.NewTimer(restartStopTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if c.Lifecycle(id).State == "idle" {
			if err := c.Start(ctx, id); err != nil {
				return err
			}
			if cfg, err := c.Repo.Get(ctx, id); err == nil {
				c.emit(events.ServerRestarted, cfg, "Server restarted", c.announce(id, func(m server.DiscordMessages) string { return m.Restarted }))
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("restart timeout waiting for server %q to stop", id)
		case <-ticker.C:
		}
	}
}

// StartScheduler runs the schedule loop until ctx is cancelled. The returned
// function blocks until every action it started has finished, so shutdown can
// wait for a scheduled stop to persist its database.
func (c *Controller) StartScheduler(ctx context.Context) func() {
	runner := &scheduler.Runner{Schedules: c.Repo, Logger: c.Logger, Execute: c.executeSchedule}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.Run(ctx)
	}()
	return func() { <-done }
}

// tooFreshToRestart applies the minimum uptime: a server started shortly
// before a scheduled restart, by hand or after a crash, is left alone.
func (c *Controller) tooFreshToRestart(cfg server.Config) (bool, time.Duration) {
	if cfg.RestartMinUptimeSeconds <= 0 || c.Lifecycle(cfg.ID).State != "running" {
		return false, 0
	}
	started := c.Snapshot(cfg.ID).StartedAt
	if started.IsZero() {
		return false, 0
	}
	uptime := time.Since(started)
	return uptime < time.Duration(cfg.RestartMinUptimeSeconds)*time.Second, uptime
}

func (c *Controller) executeSchedule(ctx context.Context, item scheduler.Schedule) error {
	cfg, err := c.Repo.Get(ctx, item.ServerID)
	if err != nil {
		// A deleted server cannot come back by retrying.
		return scheduler.Permanent(err)
	}
	switch item.Action {
	case "start":
		err = c.Start(ctx, item.ServerID)
	case "stop":
		if err = c.warnPlayers(ctx, cfg, "stop"); err == nil {
			err = c.Stop(ctx, item.ServerID)
		}
	case "restart":
		if skip, uptime := c.tooFreshToRestart(cfg); skip {
			c.emit(events.ScheduleSkipped, cfg, "Scheduled restart skipped: the server has not run long enough",
				map[string]string{"uptime": uptime.Round(time.Second).String(), "minimum": (time.Duration(cfg.RestartMinUptimeSeconds) * time.Second).String()})
			return nil
		}
		if err = c.warnPlayers(ctx, cfg, "restart"); err == nil {
			err = c.Restart(ctx, item.ServerID)
		}
	default:
		return scheduler.Permanent(fmt.Errorf("unsupported schedule action %q", item.Action))
	}
	if err != nil {
		c.emit(events.ScheduleFailed, cfg, "Scheduled "+item.Action+" failed", map[string]string{"error": err.Error()})
	}
	return err
}

func (c *Controller) start(ctx context.Context, id string) error {
	cfg, err := c.acquire(ctx, id, "starting")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			c.release(id)
		}
	}()
	if err := rotateProcessLog(cfg.InstallDir); err != nil {
		c.Logger.Warn("rotate process log", "server", id, "error", err)
	}
	watch := watchdog.New(watchdog.Paths(cfg.InstallDir))
	if reason, err := watch.Check(); err != nil || reason != "" {
		return fmt.Errorf("log preflight: %s (%v)", reason, err)
	}
	// The game reads its INI files once at startup and rewrites them at
	// shutdown, so this is the only point at which a managed setting sticks.
	if settings := gameconfig.SettingsFor(cfg); settings.Managed() {
		if err := gameconfig.Apply(cfg, settings); err != nil {
			return fmt.Errorf("write game configuration: %w", err)
		}
		c.Logger.Info("applied game configuration", "server", id,
			"path", gameconfig.Path(cfg), "settings", gameconfig.Describe(settings))
	}
	// pre_start may refuse the start, for instance a script that checks free
	// disk space or pulls a configuration.
	if err := c.runHooks(ctx, hooks.Gate, "pre_start", cfg, nil); err != nil {
		c.emit(events.ServerStartFailed, cfg, "A pre_start hook refused the start", map[string]string{"error": err.Error()})
		return fmt.Errorf("pre_start hook: %w", err)
	}
	backend := c.backendFor(cfg)
	c.setBackend(id, backend)
	if backend == backendLauncher {
		if err := c.Launcher.Validate(cfg); err != nil {
			return err
		}
		// Handoff: launcher must acquire the same locks itself. An intervening
		// external operation makes its start fail closed; the in-memory lease
		// continues to exclude concurrent HTTP operations.
		c.lifecycleMu.Lock()
		c.active[id].lease.Close()
		c.active[id].lease = nil
		c.lifecycleMu.Unlock()
		keep = true
		err := c.Launcher.Start(ctx, cfg)
		c.transition(id, "running", err)
		if err == nil {
			_ = c.runHooks(ctx, hooks.Async, "post_start", cfg, nil)
		}
		// Even a failed systemctl call may have queued a job. Monitor until a
		// definitive terminal state rather than releasing on transport errors.
		go c.monitor(cfg, watch, backendLauncher)
		return err
	}
	if backend == backendUnit {
		if err := c.Units.Start(ctx, cfg); err != nil {
			// A failed systemctl call may still have started the unit. Only a
			// definite answer that it is not running releases the reservation.
			s, inspectErr := c.Units.Inspect(ctx, id)
			if inspectErr == nil && s.State != process.StateRunning && s.State != process.StateStopping {
				c.emit(events.ServerStartFailed, cfg, "Server failed to start", map[string]string{"error": err.Error()})
				return err
			}
			keep = true
			c.transition(id, "running", err)
			go c.monitor(cfg, watch, backendUnit)
			return err
		}
		keep = true
		c.transition(id, "running", nil)
		c.emit(events.ServerStarted, cfg, "Server process started", nil)
		_ = c.runHooks(ctx, hooks.Async, "post_start", cfg, nil)
		go c.monitor(cfg, watch, backendUnit)
		return nil
	}
	if cfg.RamdiskEnabled {
		if err := c.Tmpfs.Prepare(cfg.RamdiskPath, cfg.RamdiskSizeMB); err != nil {
			return err
		}
		mounted, err := c.Tmpfs.IsMounted(cfg.RamdiskPath)
		if err != nil {
			return err
		}
		if !mounted {
			return fmt.Errorf("RAM disk path %q is not mounted as tmpfs", cfg.RamdiskPath)
		}
		if err := c.RAM.Load(cfg); err != nil {
			// Load can fail after partially changing links; preserve the lease.
			keep = true
			c.transition(id, "recovery_required", err)
			return err
		}
	}
	if err := c.Processes.Start(ctx, cfg); err != nil {
		c.emit(events.ServerStartFailed, cfg, "Server failed to start", map[string]string{"error": err.Error()})
		if cfg.RamdiskEnabled {
			keep = true
			c.persist(cfg)
		}
		return err
	}
	keep = true
	c.transition(id, "running", nil)
	c.emit(events.ServerStarted, cfg, "Server process started", nil)
	_ = c.runHooks(ctx, hooks.Async, "post_start", cfg, nil)
	go c.monitor(cfg, watch, backendProcess)
	return nil
}

// One monitor owns draining and final flush, including unexpected process exit.
func (c *Controller) monitor(cfg server.Config, watch *watchdog.Logs, backend string) {
	waitCtx, cancelWait := context.WithCancel(context.Background())
	defer cancelWait()
	done := make(chan struct{})
	go func() {
		_ = c.waitProcess(waitCtx, cfg.ID, backend)
		close(done)
	}()
	// Only a unit survives the manager, so only its monitor may simply let go.
	var detached <-chan struct{}
	if backend == backendUnit {
		detached = c.detached
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	drainEvery := c.DrainInterval
	if drainEvery <= 0 {
		drainEvery = 5 * time.Second
	}
	drain := time.NewTicker(drainEvery)
	defer drain.Stop()
	maintenanceEvery := c.MaintenanceInterval
	if maintenanceEvery <= 0 {
		maintenanceEvery = time.Minute
	}
	maintenance := time.NewTicker(maintenanceEvery)
	defer maintenance.Stop()
	queryCtx, cancelQuery := context.WithCancel(context.Background())
	queryDone := make(chan struct{})
	go func() { defer close(queryDone); c.observeAvailability(queryCtx, cfg) }()
	defer cancelQuery()
	for {
		select {
		case <-detached:
			cancelQuery()
			<-queryDone
			return
		case <-done:
			cancelQuery()
			<-queryDone
			crashed := c.Lifecycle(cfg.ID).State == "running"
			snapshot := c.snapshotOf(backend, cfg.ID)
			forced := c.wasForced(cfg.ID) || snapshot.Forced
			// A kill or a crash skipped the game's own shutdown. Check the world
			// before anything, including a crash restart, can open it again.
			if (crashed || forced) && !cfg.RamdiskEnabled {
				why := "a crash"
				if forced {
					why = "a kill"
				}
				_ = c.verifyAfterUncleanExit(cfg, why)
			}
			c.persist(cfg)
			c.afterExit(cfg, crashed, snapshot.LastExit)
			return
		case <-ticker.C:
			if c.Lifecycle(cfg.ID).State == "running" {
				reason, err := watch.Check()
				if err != nil {
					reason = "log_monitor_error: " + err.Error()
				}
				if reason != "" {
					c.trip(cfg.ID, reason)
				}
			}
		case <-drain.C:
			c.drainRAM(cfg)
		case <-maintenance.C:
			c.maintain(cfg)
		}
	}
}

// drainRAM moves game-written backups off the RAM disk and stops the server if
// the RAM disk is filling up anyway. SQLite resolves the symlinked database to
// its RAM path, so the game writes its backup siblings there; with a multi
// hundred megabyte save those fill tmpfs quickly. The bind mount launcher does
// not have this problem, because there is no symlink left for SQLite to follow.
func (c *Controller) drainRAM(cfg server.Config) {
	if !cfg.RamdiskEnabled || c.Launcher.Enabled(cfg.ID) {
		return
	}
	if _, err := c.RAM.DrainBackups(cfg); err != nil {
		c.noteError(cfg.ID, fmt.Sprintf("drain RAM backups: %v", err))
	}
	limit := c.RamdiskUsageLimit
	if limit <= 0 || limit > 1 {
		limit = 0.9
	}
	used, err := c.Tmpfs.UsedRatio(cfg.RamdiskPath)
	if err != nil {
		c.noteError(cfg.ID, fmt.Sprintf("RAM disk usage: %v", err))
		return
	}
	if used < limit {
		return
	}
	// A full tmpfs fails the next SQLite write. Stopping while the database can
	// still be flushed is better than letting the save run out of space.
	c.Logger.Error("RAM disk is nearly full; stopping server",
		"server", cfg.ID, "path", cfg.RamdiskPath, "used", used, "limit", limit)
	c.trip(cfg.ID, fmt.Sprintf("ram_disk_exhausted: %.0f%% of %s used", used*100, cfg.RamdiskPath))
}

// maintain prunes expired backups. It walks the whole Saved tree, so it runs on
// its own slower schedule rather than on the drain tick.
func (c *Controller) maintain(cfg server.Config) {
	if c.BackupRetention > 0 {
		if _, err := (backup.Manager{}).PruneConanBackups(ramstate.LayoutFor(cfg).PersistentSaved, c.BackupRetention); err != nil {
			c.noteError(cfg.ID, fmt.Sprintf("backup retention: %v", err))
		}
	}
}

// noteError records a non-fatal maintenance failure on the reservation, if the
// server still holds one.
func (c *Controller) noteError(id, message string) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if held := c.active[id]; held != nil {
		held.Error = message
	}
}

func (c *Controller) persist(cfg server.Config) {
	if c.Launcher.Enabled(cfg.ID) {
		c.launcherPersist(cfg)
		return
	}
	c.transition(cfg.ID, "flushing", nil)
	if cfg.RamdiskEnabled {
		if _, err := c.RAM.DrainBackups(cfg); err != nil {
			c.transition(cfg.ID, "recovery_required", fmt.Errorf("drain RAM backups: %w", err))
			return
		}
		if _, err := c.RAM.Flush(cfg); err != nil {
			c.transition(cfg.ID, "recovery_required", fmt.Errorf("persist RAM database: %w", err))
			return
		}
		// The database is on SSD now, so failing to free tmpfs is a warning and
		// not a recovery condition: nothing is at risk, only RAM stays occupied.
		if err := c.RAM.ReleaseRAM(cfg); err != nil {
			c.Logger.Warn("RAM disk not freed after persist", "server", cfg.ID, "error", err)
			c.noteError(cfg.ID, fmt.Sprintf("release RAM: %v", err))
		}
	}
	c.release(cfg.ID)
}

func (c *Controller) Stop(ctx context.Context, id string) error {
	defer c.lockServer(id)()
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil {
		return err
	}
	// pre_stop runs before the signal and outside lifecycleMu: a script that
	// warns players may take minutes, and nothing else should wait on it. Its
	// failure is reported but never prevents the stop.
	if c.Lifecycle(id).State == "running" {
		_ = c.runHooks(ctx, hooks.Wait, "pre_stop", cfg, nil)
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	held := c.active[id]
	if h, ok := c.health[id]; ok {
		h.RestartPending = false
		c.health[id] = h
	}
	// Recorded first: even a server that is already down, or waiting for an
	// automatic restart, must stay down after this request.
	if err := c.Repo.SetDesiredState(ctx, id, "stopped"); err != nil {
		return err
	}
	if held == nil {
		return nil
	}
	switch held.State {
	case "stopping", "flushing":
		return nil
	case "running":
		if err := c.stopProcess(ctx, id); err != nil {
			return err
		}
		c.beginStopping(id)
		c.emit(events.ServerStopping, cfg, "Server is stopping and saving the world", nil)
	default:
		return fmt.Errorf("cannot stop server while lifecycle is %s: %s", held.State, held.Error)
	}
	return nil
}

// Kill ends a server that is not responding to a normal stop. The world keeps
// only what the game had already saved.
func (c *Controller) Kill(ctx context.Context, id string) error {
	defer c.lockServer(id)()
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil {
		return err
	}
	held := c.active[id]
	if held == nil || (held.State != "running" && held.State != "stopping") {
		return fmt.Errorf("server %q is not running", id)
	}
	if h, ok := c.health[id]; ok {
		h.RestartPending = false
		c.health[id] = h
	}
	if err := c.Repo.SetDesiredState(ctx, id, "stopped"); err != nil {
		return err
	}
	held.forced = true
	if err := c.killProcess(ctx, id); err != nil {
		return err
	}
	held.State = "stopping"
	c.emit(events.ServerKilled, cfg, "Server was killed without a final save", nil)
	return nil
}

func (c *Controller) Install(ctx context.Context, id string) (Job, error) {
	cfg, err := c.acquire(ctx, id, "installing")
	if err != nil {
		return Job{}, err
	}
	now := time.Now().UTC()
	job := Job{ID: fmt.Sprintf("install-%d", now.UnixNano()), ServerID: id, Kind: "install", Status: "queued", CreatedAt: now, UpdatedAt: now}
	c.jobsMu.Lock()
	c.jobs[job.ID] = job
	c.jobsMu.Unlock()
	go func(job Job) {
		defer c.release(id)
		job.Status = "running"
		job.UpdatedAt = time.Now().UTC()
		c.jobsMu.Lock()
		c.jobs[job.ID] = job
		c.jobsMu.Unlock()
		err := c.Steam.InstallServer(context.Background(), cfg)
		job.UpdatedAt = time.Now().UTC()
		if err != nil {
			job.Status = "failed"
			job.Error = err.Error()
		} else {
			job.Status = "completed"
		}
		c.jobsMu.Lock()
		c.jobs[job.ID] = job
		c.jobsMu.Unlock()
	}(job)
	return job, nil
}

// lockServer takes the operation lock of one server and returns its release.
func (c *Controller) lockServer(id string) func() {
	c.opLocksMu.Lock()
	lock := c.opLocks[id]
	if lock == nil {
		lock = &sync.Mutex{}
		c.opLocks[id] = lock
	}
	c.opLocksMu.Unlock()
	lock.Lock()
	return lock.Unlock
}

// RestartJob runs Restart in the background and reports it as a job. With
// warn, players are warned first as for a scheduled restart.
func (c *Controller) RestartJob(ctx context.Context, id string, warn bool) (Job, error) {
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	now := time.Now().UTC()
	job := Job{ID: fmt.Sprintf("restart-%d", now.UnixNano()), ServerID: id, Kind: "restart", Status: "running", CreatedAt: now, UpdatedAt: now}
	c.setJob(job)
	// The goroutine gets its own copy: the caller returns job concurrently.
	go func(job Job) {
		var err error
		if warn {
			err = c.warnPlayers(context.Background(), cfg, "restart")
		}
		if err == nil {
			err = c.Restart(context.Background(), id)
		}
		job.UpdatedAt = time.Now().UTC()
		job.Status = "completed"
		if err != nil {
			job.Status, job.Error = "failed", err.Error()
			if cfg, getErr := c.Repo.Get(context.Background(), id); getErr == nil {
				c.emit(events.ServerStartFailed, cfg, "Restart failed", map[string]string{"error": err.Error()})
			}
		}
		c.setJob(job)
	}(job)
	return job, nil
}

func (c *Controller) setJob(job Job) {
	c.jobsMu.Lock()
	defer c.jobsMu.Unlock()
	c.jobs[job.ID] = job
}

func (c *Controller) Job(id string) (Job, bool) {
	c.jobsMu.RLock()
	defer c.jobsMu.RUnlock()
	job, ok := c.jobs[id]
	return job, ok
}

// LogNames are the logs an operator can ask for by name instead of by path.
var LogNames = map[string]string{
	// The game's own log, rewritten at every start.
	"game": filepath.Join("ConanSandbox", "Saved", "Logs", "ConanSandbox.log"),
	// What the process printed, kept across starts by the manager.
	"process": "conan-manager-process.log",
}

// TailLog returns the last lines of a server log, optionally only those
// containing filter. The log is a name from LogNames or a path inside the
// install directory; an empty one means the game log.
func (c *Controller) TailLog(ctx context.Context, id, log string, limit int, filter string) ([]string, error) {
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if log == "" {
		log = "game"
	}
	if name, ok := LogNames[log]; ok {
		log = name
	}
	base, err := canonicalPath(cfg.InstallDir)
	if err != nil {
		return nil, err
	}
	clean := filepath.Clean(log)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(cfg.InstallDir, clean)
	}
	// Symlinks are resolved first: the game can write links into its own
	// directory, and one must not lead the API to a file elsewhere.
	target, err := canonicalPath(clean)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(base, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("log path must be inside server install directory")
	}
	return c.Logs.Search(target, limit, filter)
}
