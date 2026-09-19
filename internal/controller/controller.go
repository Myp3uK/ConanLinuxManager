package controller

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/example/conan-manager/internal/launchservice"
	"github.com/example/conan-manager/internal/logreader"
	"github.com/example/conan-manager/internal/process"
	"github.com/example/conan-manager/internal/ramstate"
	"github.com/example/conan-manager/internal/server"
	"github.com/example/conan-manager/internal/steamcmd"
	"github.com/example/conan-manager/internal/tmpfs"
	"github.com/example/conan-manager/internal/watchdog"
)

type Controller struct {
	operationMu        sync.Mutex
	Repo               *server.Repository
	Processes          *process.Manager
	Launcher           *launchservice.Manager
	Logs               logreader.Reader
	Tmpfs              tmpfs.Manager
	RAM                ramstate.Manager
	Steam              steamcmd.Service
	jobs               map[string]Job
	jobsMu             sync.RWMutex
	lifecycleMu        sync.Mutex
	closing            bool
	active             map[string]*reservation
	health             map[string]Health
	AvailabilityPolicy watchdog.AvailabilityPolicy
	QueryHost          string
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
	return &Controller{Repo: repo, Processes: process.NewManager(), jobs: make(map[string]Job), active: make(map[string]*reservation), health: make(map[string]Health), AvailabilityPolicy: watchdog.DefaultAvailabilityPolicy(), QueryHost: "127.0.0.1"}
}

func (c *Controller) Start(ctx context.Context, id string) error {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	return c.start(ctx, id)
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
	watch := watchdog.New(watchdog.Paths(cfg.InstallDir))
	if reason, err := watch.Check(); err != nil || reason != "" {
		return fmt.Errorf("log preflight: %s (%v)", reason, err)
	}
	if c.Launcher.Enabled(id) {
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
		// Even a failed systemctl call may have queued a job. Monitor until a
		// definitive terminal state rather than releasing on transport errors.
		go c.monitor(cfg, watch)
		return err
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
		if cfg.RamdiskEnabled {
			keep = true
			c.persist(cfg)
		}
		return err
	}
	keep = true
	c.transition(id, "running", nil)
	go c.monitor(cfg, watch)
	return nil
}

// One monitor owns draining and final flush, including unexpected process exit.
func (c *Controller) monitor(cfg server.Config, watch *watchdog.Logs) {
	done := make(chan struct{})
	go func() {
		_ = c.waitProcess(context.Background(), cfg.ID)
		close(done)
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	queryCtx, cancelQuery := context.WithCancel(context.Background())
	queryDone := make(chan struct{})
	go func() { defer close(queryDone); c.observeAvailability(queryCtx, cfg) }()
	defer cancelQuery()
	for {
		select {
		case <-done:
			cancelQuery()
			<-queryDone
			c.persist(cfg)
			if c.Lifecycle(cfg.ID).State == "idle" {
				c.restartAfterWatchdog(cfg.ID)
			}
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
			if cfg.RamdiskEnabled && !c.Launcher.Enabled(cfg.ID) {
				if _, err := c.RAM.DrainBackups(cfg); err != nil {
					c.lifecycleMu.Lock()
					c.active[cfg.ID].Error = fmt.Sprintf("drain RAM backups: %v", err)
					c.lifecycleMu.Unlock()
				}
			}
		}
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
	}
	c.release(cfg.ID)
}

func (c *Controller) Stop(ctx context.Context, id string) error {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if _, err := c.Repo.Get(ctx, id); err != nil {
		return err
	}
	held := c.active[id]
	if h, ok := c.health[id]; ok {
		h.RestartPending = false
		c.health[id] = h
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
		held.State = "stopping"
	default:
		return fmt.Errorf("cannot stop server while lifecycle is %s: %s", held.State, held.Error)
	}
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

func (c *Controller) Job(id string) (Job, bool) {
	c.jobsMu.RLock()
	defer c.jobsMu.RUnlock()
	job, ok := c.jobs[id]
	return job, ok
}

func (c *Controller) TailLog(ctx context.Context, id, path string, limit int) ([]string, error) {
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, fmt.Errorf("log path is required")
	}
	base, err := filepath.Abs(cfg.InstallDir)
	if err != nil {
		return nil, err
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(base, clean)
	}
	target, err := filepath.Abs(clean)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(base, target)
	if err != nil || relative == ".." || len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		return nil, fmt.Errorf("log path must be inside server install directory")
	}
	return c.Logs.Tail(target, limit)
}
