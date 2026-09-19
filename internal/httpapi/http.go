package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/example/conan-manager/internal/config"
	"github.com/example/conan-manager/internal/controller"
	"github.com/example/conan-manager/internal/launchservice"
	"github.com/example/conan-manager/internal/scheduler"
	"github.com/example/conan-manager/internal/server"
	"github.com/example/conan-manager/internal/store"
	"os"
)

func New(db *store.Store, logger *slog.Logger) http.Handler {
	handler, _ := NewManaged(db, logger)
	return handler
}

// NewManaged exposes the controller to the application's shutdown owner.
func NewManaged(db *store.Store, logger *slog.Logger) (http.Handler, *controller.Controller) {
	mux := http.NewServeMux()
	repo := server.NewRepository(db.DB())
	control := controller.New(repo)
	control.Launcher = launchservice.New(os.Getenv("CONAN_MANAGER_LAUNCHER_IDS"))
	cfg := config.Load()
	control.AvailabilityPolicy = cfg.Availability
	control.QueryHost = cfg.QueryHost
	control.BackupRetention = cfg.BackupRetention
	control.AdoptLaunchers(context.Background())
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
	mux.HandleFunc("GET /api/v1/jobs/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/jobs/")
		if id == "" || strings.Contains(id, "/") {
			writeError(w, http.StatusNotFound, nil)
			return
		}
		job, ok := control.Job(id)
		if !ok {
			writeError(w, http.StatusNotFound, nil)
			return
		}
		writeJSON(w, http.StatusOK, job)
	})
	mux.HandleFunc("/api/v1/servers/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/schedule") {
			scheduleHandler(repo)(w, r)
			return
		}
		serverHandler(repo, control)(w, r)
	})
	mux.HandleFunc("/api/v1/servers", serversHandler(repo))
	handler := logging(mux, logger)
	handler = requestProtection(handler, cfg.AllowedOrigin)
	handler = BasicAuth(cfg.AdminUsername, cfg.AdminPassword)(handler)
	return handler, control
}

func scheduleHandler(repo *server.Repository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/servers/"), "/schedule")
		if id == "" || strings.Contains(id, "/") {
			writeError(w, http.StatusNotFound, nil)
			return
		}
		switch r.Method {
		case http.MethodGet:
			value, err := repo.GetSchedule(r.Context(), id)
			if err != nil {
				writeError(w, http.StatusNotFound, err)
				return
			}
			writeJSON(w, http.StatusOK, value)
		case http.MethodPut:
			var value struct {
				Action          string    `json:"action"`
				Enabled         bool      `json:"enabled"`
				IntervalSeconds int       `json:"interval_seconds"`
				NextRun         time.Time `json:"next_run"`
			}
			if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			schedule := scheduler.Schedule{ServerID: id, Action: value.Action, Enabled: value.Enabled, IntervalSeconds: value.IntervalSeconds, NextRun: value.NextRun}
			if err := repo.SetSchedule(r.Context(), schedule); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, schedule)
		case http.MethodDelete:
			if err := repo.DeleteSchedule(r.Context(), id); err != nil {
				writeError(w, http.StatusNotFound, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Allow", "GET, PUT, DELETE")
			writeError(w, http.StatusMethodNotAllowed, nil)
		}
	}
}

func serversHandler(repo *server.Repository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			items, err := repo.List(r.Context())
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, items)
		case http.MethodPost:
			var value server.Config
			if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if err := repo.Create(r.Context(), value); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			created, err := repo.Get(r.Context(), value.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusCreated, created)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeError(w, http.StatusMethodNotAllowed, nil)
		}
	}
}

func serverHandler(repo *server.Repository, control *controller.Controller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/servers/")
		parts := strings.Split(strings.Trim(path, "/"), "/")
		id := parts[0]
		if id == "" || len(parts) > 2 {
			writeError(w, http.StatusNotFound, nil)
			return
		}
		if r.Method == http.MethodGet && len(parts) == 2 && parts[1] == "logs" {
			path := r.URL.Query().Get("path")
			limit := 200
			if value := r.URL.Query().Get("limit"); value != "" {
				parsed, err := strconv.Atoi(value)
				if err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}
				limit = parsed
			}
			lines, err := control.TailLog(r.Context(), id, path, limit)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"path": path, "lines": lines})
			return
		}
		if len(parts) == 2 {
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, nil)
				return
			}
			var err error
			switch parts[1] {
			case "start":
				err = control.Start(r.Context(), id)
			case "stop":
				err = control.Stop(r.Context(), id)
			case "install":
				job, installErr := control.Install(r.Context(), id)
				if installErr != nil {
					writeError(w, http.StatusConflict, installErr)
					return
				}
				writeJSON(w, http.StatusAccepted, job)
				return
			case "logs":
				writeError(w, http.StatusBadRequest, fmt.Errorf("use GET for logs"))
				return
			default:
				writeError(w, http.StatusNotFound, nil)
				return
			}
			if err != nil {
				writeError(w, http.StatusConflict, err)
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "process": control.Snapshot(id), "lifecycle": control.Lifecycle(id)})
			return
		}
		switch r.Method {
		case http.MethodGet:
			value, err := repo.Get(r.Context(), id)
			if err != nil {
				writeError(w, http.StatusNotFound, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"server": value, "process": control.Snapshot(id), "lifecycle": control.Lifecycle(id), "health": control.Health(id)})
		case http.MethodDelete:
			if err := control.Delete(r.Context(), id); err != nil {
				writeError(w, http.StatusConflict, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Allow", "GET, DELETE")
			writeError(w, http.StatusMethodNotAllowed, nil)
		}
	}
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
				writeError(w, http.StatusForbidden, fmt.Errorf("origin is not allowed"))
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

func writeError(w http.ResponseWriter, status int, err error) {
	message := http.StatusText(status)
	if err != nil {
		message = err.Error()
	}
	writeJSON(w, status, map[string]string{"error": message})
}
