package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/example/conan-manager/internal/config"
	"github.com/example/conan-manager/internal/controller"
	"github.com/example/conan-manager/internal/eventlog"
	"github.com/example/conan-manager/internal/events"
	"github.com/example/conan-manager/internal/hooks"
	"github.com/example/conan-manager/internal/launchservice"
	"github.com/example/conan-manager/internal/notify"
	"github.com/example/conan-manager/internal/scheduler"
	"github.com/example/conan-manager/internal/security"
	"github.com/example/conan-manager/internal/server"
	"github.com/example/conan-manager/internal/store"
	"github.com/example/conan-manager/internal/unitservice"
)

// api holds everything the handlers share. Keeping it in one place lets error
// rendering apply a single policy across every route.
type api struct {
	repo     *server.Repository
	control  *controller.Controller
	logger   *slog.Logger
	detail   bool
	history  *eventlog.Log
	channels *notify.Repository
	notifier *notify.Dispatcher
}

func New(db *store.Store, logger *slog.Logger) http.Handler {
	handler, _ := NewManaged(db, logger, config.Load())
	return handler
}

// NewManaged exposes the controller to the application's shutdown owner. The
// configuration is passed in so the process reads its environment exactly once.
func NewManaged(db *store.Store, logger *slog.Logger, cfg config.Config) (http.Handler, *controller.Controller) {
	repo := server.NewRepository(db.DB())
	control := controller.New(repo)
	control.Launcher = launchservice.New(cfg.LauncherIDs)
	control.Logger = logger
	control.AvailabilityPolicy = cfg.Availability
	control.QueryHost = cfg.QueryHost
	control.BackupRetention = cfg.BackupRetention
	control.BackupDir = cfg.BackupDir
	control.BackupKeep = cfg.BackupKeep
	control.Events = events.NewBus()
	history := eventlog.New(db.DB())
	control.Events.Subscribe("history", func(e events.Event) {
		if err := history.Record(context.Background(), e); err != nil {
			logger.Error("store event", "type", e.Type, "error", err)
		}
	})
	// Without a secret key there is nowhere safe to keep webhook URLs, so
	// notifications stay off rather than storing them in clear.
	var box *security.SecretBox
	if cfg.SecretKey != "" {
		if sealed, err := security.NewSecretBox(cfg.SecretKey); err == nil {
			box = sealed
		}
	}
	channels := notify.NewRepository(db.DB(), box)
	notifier := notify.NewDispatcher(channels, logger)
	if box != nil {
		control.Events.Subscribe("discord", notifier.Handle)
	}
	control.StopTimeout = cfg.StopTimeout
	control.Hooks = hooks.New(cfg.HooksDir, cfg.HookTimeout)
	control.Hooks.Logger = logger
	control.Hooks.Record = repo.RecordHookRun
	control.Hooks.OnFailure = func(server server.Config, failure *hooks.Failure) {
		control.Events.Publish(events.Event{Type: events.HookFailed, ServerID: server.ID, ServerName: server.Name,
			Message: failure.Error(), Details: map[string]string{"event": failure.Event, "script": failure.Script}})
	}
	if cfg.ServerUnits {
		control.Units = unitservice.New(cfg.UnitDir)
	}
	control.AdoptLaunchers(context.Background())
	control.AdoptUnits(context.Background())

	a := &api{repo: repo, control: control, logger: logger, detail: cfg.ExposeErrorDetail,
		history: history, channels: channels, notifier: notifier}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Ping(); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/v1/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"service": "conan-manager", "version": "0.1.0"})
	})
	mux.HandleFunc("GET /api/v1/jobs/", a.job)
	mux.HandleFunc("GET /api/v1/events", a.events)
	mux.HandleFunc("GET /api/v1/hooks", a.hookScripts)
	mux.HandleFunc("/api/v1/notifications", a.notificationChannels)
	mux.HandleFunc("/api/v1/notifications/", a.notificationChannel)
	mux.HandleFunc("/api/v1/servers/", a.routeServer)
	mux.HandleFunc("/api/v1/servers", a.servers)

	handler := logging(mux, logger)
	handler = requestProtection(handler, cfg.AllowedOrigin)
	handler = BasicAuth(cfg.AdminUsername, cfg.AdminPassword)(handler)
	return handler, control
}

// fail renders an error response. Internal failures carry absolute filesystem
// paths and lock state, so they are logged in full but reported to the client
// only as a status unless the deployment opts into detail.
func (a *api) fail(w http.ResponseWriter, r *http.Request, status int, err error) {
	if err != nil {
		level := slog.LevelWarn
		if status >= http.StatusInternalServerError {
			level = slog.LevelError
		}
		a.logger.Log(r.Context(), level, "api error",
			"method", r.Method, "path", r.URL.Path, "status", status, "error", err)
	}
	message := http.StatusText(status)
	if err != nil && (a.detail || status < http.StatusInternalServerError) {
		message = err.Error()
	}
	writeJSON(w, status, map[string]string{"error": message})
}

// hookScripts lists the hook directory, so a client can offer the scripts that
// can be used and explain why the others cannot.
func (a *api) hookScripts(w http.ResponseWriter, r *http.Request) {
	if a.control.Hooks == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	scripts, err := a.control.Hooks.Available()
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "dir": a.control.Hooks.Dir, "timeout_seconds": int(a.control.Hooks.Timeout / time.Second),
		"events": server.HookEvents, "scripts": scripts,
	})
}

func (a *api) hookRuns(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/servers/"), "/hooks/runs")
	if id == "" || strings.Contains(id, "/") || r.Method != http.MethodGet {
		a.fail(w, r, http.StatusNotFound, nil)
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			a.fail(w, r, http.StatusBadRequest, fmt.Errorf("limit must be a number"))
			return
		}
		limit = parsed
	}
	runs, err := a.repo.HookRuns(r.Context(), id, limit)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

// events returns the newest lifecycle notices, optionally for one server.
func (a *api) events(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			a.fail(w, r, http.StatusBadRequest, fmt.Errorf("limit must be between 1 and 500"))
			return
		}
		limit = parsed
	}
	var before int64
	if raw := r.URL.Query().Get("before"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1 {
			a.fail(w, r, http.StatusBadRequest, fmt.Errorf("before must be an event id"))
			return
		}
		before = parsed
	}
	items, err := a.history.List(r.Context(), eventlog.Query{ServerID: r.URL.Query().Get("server"), Before: before, Limit: limit})
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *api) notificationChannels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := a.channels.List(r.Context())
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var in notify.Input
		if err := decodeStrict(r, &in); err != nil {
			a.fail(w, r, http.StatusBadRequest, err)
			return
		}
		created, err := a.channels.Create(r.Context(), in)
		if err != nil {
			a.fail(w, r, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	default:
		w.Header().Set("Allow", "GET, POST")
		a.fail(w, r, http.StatusMethodNotAllowed, nil)
	}
}

func (a *api) notificationChannel(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/notifications/"), "/")
	id, action, _ := strings.Cut(rest, "/")
	if id == "" || strings.Contains(action, "/") {
		a.fail(w, r, http.StatusNotFound, nil)
		return
	}
	switch {
	case action == "test" && r.Method == http.MethodPost:
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		if err := a.notifier.Test(ctx, id); err != nil {
			a.fail(w, r, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "delivered"})
	case action == "" && r.Method == http.MethodPatch:
		var in notify.Input
		if err := decodeStrict(r, &in); err != nil {
			a.fail(w, r, http.StatusBadRequest, err)
			return
		}
		updated, err := a.channels.Update(r.Context(), id, in)
		if err != nil {
			a.fail(w, r, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	case action == "" && r.Method == http.MethodDelete:
		if err := a.channels.Delete(r.Context(), id); err != nil {
			a.fail(w, r, http.StatusNotFound, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		a.fail(w, r, http.StatusNotFound, nil)
	}
}

// backups serves /api/v1/servers/{id}/backups[/{backup}[/restore]].
func (a *api) backups(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/servers/"), "/"), "/")
	if len(parts) < 2 || len(parts) > 4 || parts[1] != "backups" || parts[0] == "" {
		a.fail(w, r, http.StatusNotFound, nil)
		return
	}
	id := parts[0]
	if len(parts) == 2 {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			a.fail(w, r, http.StatusMethodNotAllowed, nil)
			return
		}
		list, err := a.control.Backups(r.Context(), id)
		if err != nil {
			a.fail(w, r, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
		return
	}
	backupID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		a.fail(w, r, http.StatusNotFound, fmt.Errorf("backup id must be a number"))
		return
	}
	switch {
	case len(parts) == 3 && r.Method == http.MethodDelete:
		if err := a.control.DeleteBackup(r.Context(), id, backupID); err != nil {
			a.fail(w, r, http.StatusNotFound, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 4 && parts[3] == "restore" && r.Method == http.MethodPost:
		// A restore copies and checks the whole world, which outlasts a
		// request on a large world, so it runs as a job.
		job, err := a.control.RestoreJob(r.Context(), id, backupID)
		if err != nil {
			a.fail(w, r, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusAccepted, job)
	default:
		a.fail(w, r, http.StatusNotFound, nil)
	}
}

// gameConfig serves /api/v1/servers/{id}/config/{file}[/versions[/{v}[/restore]]].
func (a *api) gameConfig(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/servers/"), "/"), "/")
	if len(parts) < 3 || parts[1] != "config" || parts[0] == "" {
		a.fail(w, r, http.StatusNotFound, nil)
		return
	}
	id, file, rest := parts[0], parts[2], parts[3:]
	fail := func(err error) {
		status := http.StatusConflict
		var invalid server.ErrInvalidConfig
		if errors.As(err, &invalid) {
			status = http.StatusBadRequest
		}
		a.fail(w, r, status, err)
	}
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		view, err := a.control.ReadConfig(r.Context(), id, file)
		if err != nil {
			a.fail(w, r, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	case len(rest) == 0 && r.Method == http.MethodPatch:
		var body struct {
			Set    map[string]map[string]string `json:"set"`
			Remove map[string][]string          `json:"remove"`
		}
		if err := decodeStrict(r, &body); err != nil {
			a.fail(w, r, http.StatusBadRequest, err)
			return
		}
		changes, err := a.control.EditConfig(r.Context(), id, file, body.Set, body.Remove)
		if err != nil {
			fail(err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"changes": changes})
	case len(rest) == 0 && r.Method == http.MethodPut:
		var body struct {
			Content string `json:"content"`
		}
		if err := decodeStrict(r, &body); err != nil {
			a.fail(w, r, http.StatusBadRequest, err)
			return
		}
		if err := a.control.ReplaceConfig(r.Context(), id, file, body.Content); err != nil {
			fail(err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case len(rest) == 1 && rest[0] == "versions" && r.Method == http.MethodGet:
		versions, err := a.repo.ConfigVersions(r.Context(), id, file)
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, versions)
	case len(rest) >= 2 && rest[0] == "versions":
		version, err := strconv.ParseInt(rest[1], 10, 64)
		if err != nil {
			a.fail(w, r, http.StatusNotFound, nil)
			return
		}
		switch {
		case len(rest) == 2 && r.Method == http.MethodGet:
			v, err := a.repo.ConfigVersion(r.Context(), id, file, version)
			if err != nil {
				a.fail(w, r, http.StatusNotFound, err)
				return
			}
			writeJSON(w, http.StatusOK, v)
		case len(rest) == 3 && rest[2] == "restore" && r.Method == http.MethodPost:
			if err := a.control.RestoreConfig(r.Context(), id, file, version); err != nil {
				fail(err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			a.fail(w, r, http.StatusNotFound, nil)
		}
	default:
		a.fail(w, r, http.StatusNotFound, nil)
	}
}

// decodeStrict rejects unknown fields, so a misspelt setting is an error
// instead of being ignored.
func decodeStrict(r *http.Request, value any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	return nil
}

func (a *api) job(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/jobs/")
	if id == "" || strings.Contains(id, "/") {
		a.fail(w, r, http.StatusNotFound, nil)
		return
	}
	job, ok := a.control.Job(id)
	if !ok {
		a.fail(w, r, http.StatusNotFound, nil)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (a *api) routeServer(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.Contains(r.URL.Path, "/config/"):
		a.gameConfig(w, r)
	case strings.Contains(r.URL.Path, "/backups"):
		a.backups(w, r)
	case strings.HasSuffix(r.URL.Path, "/hooks/runs"):
		a.hookRuns(w, r)
	case strings.HasSuffix(r.URL.Path, "/schedule/runs"):
		a.scheduleRuns(w, r)
	case strings.HasSuffix(r.URL.Path, "/schedule"):
		a.schedule(w, r)
	default:
		a.server(w, r)
	}
}

func (a *api) scheduleRuns(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/servers/"), "/schedule/runs")
	if id == "" || strings.Contains(id, "/") || r.Method != http.MethodGet {
		a.fail(w, r, http.StatusNotFound, nil)
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			a.fail(w, r, http.StatusBadRequest, fmt.Errorf("limit must be a number"))
			return
		}
		limit = parsed
	}
	items, err := a.repo.ScheduleRuns(r.Context(), id, limit)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *api) schedule(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/servers/"), "/schedule")
	if id == "" || strings.Contains(id, "/") {
		a.fail(w, r, http.StatusNotFound, nil)
		return
	}
	switch r.Method {
	case http.MethodGet:
		value, err := a.repo.GetSchedule(r.Context(), id)
		if err != nil {
			a.fail(w, r, http.StatusNotFound, fmt.Errorf("schedule for server %q not found", id))
			return
		}
		writeJSON(w, http.StatusOK, value)
	case http.MethodPut:
		var value struct {
			Action          string    `json:"action"`
			Enabled         bool      `json:"enabled"`
			Mode            string    `json:"mode"`
			IntervalSeconds int       `json:"interval_seconds"`
			DailyTime       string    `json:"daily_time"`
			CronExpression  string    `json:"cron_expression"`
			Timezone        string    `json:"timezone"`
			NextRun         time.Time `json:"next_run"`
		}
		if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
			a.fail(w, r, http.StatusBadRequest, fmt.Errorf("invalid request body"))
			return
		}
		item := scheduler.Schedule{
			ServerID: id, Action: value.Action, Enabled: value.Enabled, Mode: value.Mode,
			IntervalSeconds: value.IntervalSeconds, DailyTime: value.DailyTime,
			CronExpression: value.CronExpression, Timezone: value.Timezone, NextRun: value.NextRun,
		}
		if err := a.repo.SetSchedule(r.Context(), item); err != nil {
			a.fail(w, r, http.StatusBadRequest, err)
			return
		}
		saved, err := a.repo.GetSchedule(r.Context(), id)
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, saved)
	case http.MethodDelete:
		if err := a.repo.DeleteSchedule(r.Context(), id); err != nil {
			a.fail(w, r, http.StatusNotFound, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		a.fail(w, r, http.StatusMethodNotAllowed, nil)
	}
}

func (a *api) servers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := a.repo.List(r.Context())
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		// Restarting after a crash is what an operator expects unless told
		// otherwise, so a request that omits the field gets it switched on.
		value := server.Config{RestartOnCrash: true}
		if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
			a.fail(w, r, http.StatusBadRequest, fmt.Errorf("invalid request body"))
			return
		}
		if err := a.control.CheckHooks(value); err != nil {
			a.fail(w, r, http.StatusBadRequest, err)
			return
		}
		if err := value.Validate(); err != nil {
			a.fail(w, r, http.StatusBadRequest, err)
			return
		}
		if err := a.control.CheckConflicts(r.Context(), value); err != nil {
			a.fail(w, r, http.StatusConflict, err)
			return
		}
		if err := a.repo.Create(r.Context(), value); err != nil {
			a.fail(w, r, http.StatusBadRequest, err)
			return
		}
		created, err := a.repo.Get(r.Context(), value.ID)
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	default:
		w.Header().Set("Allow", "GET, POST")
		a.fail(w, r, http.StatusMethodNotAllowed, nil)
	}
}

func (a *api) server(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/servers/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	id := parts[0]
	if id == "" || len(parts) > 2 {
		a.fail(w, r, http.StatusNotFound, nil)
		return
	}
	if len(parts) == 2 {
		a.serverAction(w, r, id, parts[1])
		return
	}
	switch r.Method {
	case http.MethodGet:
		value, err := a.repo.Get(r.Context(), id)
		if err != nil {
			a.fail(w, r, http.StatusNotFound, fmt.Errorf("server %q not found", id))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"server":    value,
			"process":   a.control.Snapshot(id),
			"lifecycle": a.control.Lifecycle(id),
			"health":    a.control.Health(id),
		})
	case http.MethodPatch:
		a.patchServer(w, r, id)
	case http.MethodDelete:
		// Files and backups hold the world; they go only when asked by name.
		query := r.URL.Query()
		opts := controller.DeleteOptions{Files: query.Get("files") == "true", Backups: query.Get("backups") == "true"}
		if err := a.control.Delete(r.Context(), id, opts); err != nil {
			a.fail(w, r, http.StatusConflict, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PATCH, DELETE")
		a.fail(w, r, http.StatusMethodNotAllowed, nil)
	}
}

// serverPatch lists exactly the settings an operator may change after creation.
// Absent fields are left alone, so a client need not read-modify-write a whole
// configuration to change one value.
type serverPatch struct {
	Name                          *string `json:"name"`
	Branch                        *string `json:"branch"`
	LaunchArgs                    *string `json:"launch_args"`
	GamePort                      *int    `json:"game_port"`
	QueryPort                     *int    `json:"query_port"`
	RCONPort                      *int    `json:"rcon_port"`
	RamdiskEnabled                *bool   `json:"ramdisk_enabled"`
	RamdiskSizeMB                 *int    `json:"ramdisk_size_mb"`
	SyncIntervalSeconds           *int    `json:"sync_interval_seconds"`
	PeriodicBackupIntervalSeconds *int    `json:"periodic_backup_interval_seconds"`
	RestartOnCrash                *bool   `json:"restart_on_crash"`
	// Hooks replaces the whole hook configuration when present.
	Hooks      *map[string][]string `json:"hooks"`
	AutoUpdate *bool                `json:"auto_update"`
	Warnings   *server.Warnings     `json:"warnings"`
}

func (a *api) patchServer(w http.ResponseWriter, r *http.Request, id string) {
	var patch serverPatch
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&patch); err != nil {
		a.fail(w, r, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	updated, err := a.control.UpdateConfig(r.Context(), id, func(cfg *server.Config) {
		assign(&cfg.Name, patch.Name)
		assign(&cfg.Branch, patch.Branch)
		assign(&cfg.LaunchArgs, patch.LaunchArgs)
		assign(&cfg.GamePort, patch.GamePort)
		assign(&cfg.QueryPort, patch.QueryPort)
		assign(&cfg.RamdiskEnabled, patch.RamdiskEnabled)
		assign(&cfg.RamdiskSizeMB, patch.RamdiskSizeMB)
		assign(&cfg.SyncIntervalSeconds, patch.SyncIntervalSeconds)
		assign(&cfg.PeriodicBackupIntervalSeconds, patch.PeriodicBackupIntervalSeconds)
		assign(&cfg.RestartOnCrash, patch.RestartOnCrash)
		assign(&cfg.Hooks, patch.Hooks)
		assign(&cfg.AutoUpdate, patch.AutoUpdate)
		assign(&cfg.Warnings, patch.Warnings)
		if patch.RCONPort != nil {
			value := *patch.RCONPort
			cfg.RCONPort = &value
		}
	})
	if err != nil {
		// A busy or running server is a conflict; a rejected value is not.
		status := http.StatusConflict
		var invalid server.ErrInvalidConfig
		if errors.As(err, &invalid) {
			status = http.StatusBadRequest
		}
		a.fail(w, r, status, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func assign[T any](target *T, value *T) {
	if value != nil {
		*target = *value
	}
}

func (a *api) serverAction(w http.ResponseWriter, r *http.Request, id, action string) {
	if r.Method == http.MethodGet && action == "logs" {
		path := r.URL.Query().Get("path")
		limit := 200
		if value := r.URL.Query().Get("limit"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				a.fail(w, r, http.StatusBadRequest, fmt.Errorf("limit must be a number"))
				return
			}
			limit = parsed
		}
		if path == "" {
			path = r.URL.Query().Get("name")
		}
		lines, err := a.control.TailLog(r.Context(), id, path, limit, r.URL.Query().Get("filter"))
		if err != nil {
			a.fail(w, r, http.StatusBadRequest, err)
			return
		}
		if path == "" {
			path = "game"
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": path, "lines": lines})
		return
	}
	if action == "backup" && r.Method == http.MethodPost {
		var body struct {
			Reason string `json:"reason"`
		}
		if r.ContentLength != 0 {
			if err := decodeStrict(r, &body); err != nil {
				a.fail(w, r, http.StatusBadRequest, err)
				return
			}
		}
		job, err := a.control.BackupJob(r.Context(), id, body.Reason)
		if err != nil {
			a.fail(w, r, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusAccepted, job)
		return
	}
	if action == "players" && r.Method == http.MethodGet {
		players, err := a.control.Players(r.Context(), id)
		if err != nil {
			a.fail(w, r, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(players), "players": players})
		return
	}
	if action == "rcon" && r.Method == http.MethodPost {
		var body struct {
			Command string `json:"command"`
		}
		if err := decodeStrict(r, &body); err != nil {
			a.fail(w, r, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(body.Command) == "" {
			a.fail(w, r, http.StatusBadRequest, fmt.Errorf("command is required"))
			return
		}
		// A console command can do anything an admin can in game; keep a trace.
		a.logger.Info("rcon command", "server", id, "command", body.Command, "remote", r.RemoteAddr)
		out, err := a.control.Rcon(r.Context(), id, body.Command)
		if err != nil {
			a.fail(w, r, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"response": out})
		return
	}
	if action == "update" && r.Method == http.MethodGet {
		// Asking Steam takes several seconds; the answer is cached briefly.
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		status, err := a.control.CheckUpdate(ctx, id)
		if err != nil {
			a.fail(w, r, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
		return
	}
	if action == "update" && r.Method == http.MethodPost {
		var body controller.UpdateOptions
		if r.ContentLength != 0 {
			if err := decodeStrict(r, &body); err != nil {
				a.fail(w, r, http.StatusBadRequest, err)
				return
			}
		}
		job, err := a.control.UpdateJob(r.Context(), id, body)
		if err != nil {
			a.fail(w, r, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusAccepted, job)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		a.fail(w, r, http.StatusMethodNotAllowed, nil)
		return
	}
	var err error
	switch action {
	case "start":
		err = a.control.Start(r.Context(), id)
	case "stop":
		err = a.control.Stop(r.Context(), id)
	case "kill":
		err = a.control.Kill(r.Context(), id)
	case "verify":
		// integrity_check reads the whole world; on a large one that takes
		// longer than a request may, so it runs detached from the client.
		verifyCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Minute)
		defer cancel()
		if verifyErr := a.control.Verify(verifyCtx, id); verifyErr != nil {
			a.fail(w, r, http.StatusConflict, verifyErr)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "database": "ok", "lifecycle": a.control.Lifecycle(id)})
		return
	case "restart":
		// A restart waits for the world to be saved, which takes minutes on a
		// large world, so it runs as a job instead of holding the request.
		var body struct {
			Warn bool `json:"warn"`
		}
		if r.ContentLength != 0 {
			if err := decodeStrict(r, &body); err != nil {
				a.fail(w, r, http.StatusBadRequest, err)
				return
			}
		}
		job, restartErr := a.control.RestartJob(r.Context(), id, body.Warn)
		if restartErr != nil {
			a.fail(w, r, http.StatusConflict, restartErr)
			return
		}
		writeJSON(w, http.StatusAccepted, job)
		return
	case "install":
		job, installErr := a.control.Install(r.Context(), id)
		if installErr != nil {
			a.fail(w, r, http.StatusConflict, installErr)
			return
		}
		writeJSON(w, http.StatusAccepted, job)
		return
	case "logs":
		a.fail(w, r, http.StatusBadRequest, fmt.Errorf("use GET for logs"))
		return
	default:
		a.fail(w, r, http.StatusNotFound, nil)
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":        id,
		"process":   a.control.Snapshot(id),
		"lifecycle": a.control.Lifecycle(id),
	})
}

func logging(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
	})
}

func requestProtection(next http.Handler, allowedOrigin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")

		// Browser state-changing requests must come from the same origin. CLI
		// and server-to-server clients generally omit Origin and remain valid.
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if origin := r.Header.Get("Origin"); origin != "" && origin != requestOrigin(r) && origin != allowedOrigin {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin is not allowed"})
				return
			}
		}

		if r.Body != nil && r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		}
		next.ServeHTTP(w, r)
	})
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// writeStatus reports a failure that carries no detail worth returning.
func writeStatus(w http.ResponseWriter, status int) {
	writeJSON(w, status, map[string]string{"error": http.StatusText(status)})
}
