package httpapi

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/example/conan-manager/internal/panel"
)

// me tells the interface who is logged in.
func (a *api) me(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"username": currentUser(r), "can_restart": a.control.RestartManager != nil}
	if s := currentSession(r); s != nil {
		out["csrf"], out["session"] = s.CSRF, s.ID
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) adminList(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := a.admins.List(r.Context())
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost:
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := decodeStrict(r, &in); err != nil {
			a.fail(w, r, http.StatusBadRequest, err)
			return
		}
		in.Username = strings.TrimSpace(in.Username)
		if err := a.admins.Add(r.Context(), in.Username, in.Password); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, panel.ErrAdminExists) {
				status = http.StatusConflict
			}
			a.fail(w, r, status, err)
			return
		}
		a.logger.Info("administrator added", "by", currentUser(r), "username", in.Username)
		writeJSON(w, http.StatusCreated, map[string]string{"username": in.Username})
	default:
		w.Header().Set("Allow", "GET, POST")
		a.fail(w, r, http.StatusMethodNotAllowed, nil)
	}
}

// admin handles /admins/{name} (DELETE) and /admins/{name}/password (PUT).
func (a *api) admin(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/admins/"), "/")
	name, sub, _ := strings.Cut(rest, "/")
	switch {
	case sub == "" && r.Method == http.MethodDelete:
		if err := a.admins.Remove(r.Context(), name); err != nil {
			a.adminError(w, r, err)
			return
		}
		a.auth.sessions.Forget()
		a.logger.Info("administrator removed", "by", currentUser(r), "username", name)
		w.WriteHeader(http.StatusNoContent)
	case sub == "password" && r.Method == http.MethodPut:
		var in struct {
			Password string `json:"password"`
		}
		if err := decodeStrict(r, &in); err != nil {
			a.fail(w, r, http.StatusBadRequest, err)
			return
		}
		if err := a.admins.SetPassword(r.Context(), name, in.Password); err != nil {
			a.adminError(w, r, err)
			return
		}
		// The new password ends every session made with the old one,
		// including the caller's own, which is renewed here.
		a.auth.sessions.Forget()
		if name == currentUser(r) {
			a.renewOwnSession(w, r)
		}
		a.logger.Info("administrator password changed", "by", currentUser(r), "username", name)
		w.WriteHeader(http.StatusNoContent)
	default:
		a.fail(w, r, http.StatusNotFound, nil)
	}
}

func (a *api) adminError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, panel.ErrAdminNotFound):
		status = http.StatusNotFound
	case errors.Is(err, panel.ErrLastAdmin):
		status = http.StatusConflict
	}
	a.fail(w, r, status, err)
}

type settingView struct {
	panel.Setting
	// Value is what applies once the manager starts with the saved settings;
	// Active is what the running manager uses.
	Value  string  `json:"value"`
	Active string  `json:"active"`
	Env    string  `json:"env"`
	Stored *string `json:"stored"`
	Source string  `json:"source"`
}

func (a *api) settingsView(r *http.Request) ([]settingView, bool, error) {
	stored, err := a.settings.Values(r.Context())
	if err != nil {
		return nil, false, err
	}
	pending := false
	views := make([]settingView, 0, len(panel.Settings))
	for _, s := range panel.Settings {
		v := settingView{Setting: s, Env: s.Display(os.Getenv(s.Key)), Source: "default"}
		value := s.Default
		if env := os.Getenv(s.Key); env != "" {
			value, v.Source = env, "env"
		}
		if x, ok := stored[s.Key]; ok {
			value, v.Source = x, "panel"
			shown := s.Display(x)
			v.Stored = &shown
		}
		active := a.startup[s.Key]
		if active == "" {
			active = s.Default
		}
		if s.Live {
			active = value
		} else if value != active {
			pending = true
		}
		v.Value, v.Active = s.Display(value), s.Display(active)
		views = append(views, v)
	}
	return views, pending, nil
}

func (a *api) panelSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
	case http.MethodPut:
		// null resets a setting to the environment file.
		var in map[string]*string
		if err := decodeStrict(r, &in); err != nil {
			a.fail(w, r, http.StatusBadRequest, err)
			return
		}
		stored, err := a.settings.Values(r.Context())
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, err)
			return
		}
		// Check everything first, so a bad value leaves nothing half saved.
		for key, value := range in {
			s, ok := panel.Lookup(key)
			if !ok {
				a.fail(w, r, http.StatusBadRequest, errors.New(key+" cannot be changed from the panel"))
				return
			}
			if value == nil {
				continue
			}
			// A masked proxy coming back unchanged keeps the stored credentials.
			if old, ok := stored[key]; ok && *value == s.Display(old) {
				in[key] = &old
				continue
			}
			if _, err := s.Normalize(*value); err != nil {
				a.fail(w, r, http.StatusBadRequest, errors.New(key+": "+err.Error()))
				return
			}
		}
		for key, value := range in {
			var err error
			if value == nil {
				err = a.settings.Reset(r.Context(), key)
			} else {
				_, err = a.settings.Set(r.Context(), key, *value)
			}
			if err != nil {
				a.fail(w, r, http.StatusInternalServerError, err)
				return
			}
			a.logger.Info("panel setting changed", "by", currentUser(r), "key", key, "reset", value == nil)
			if value == nil {
				noteAudit(r, "", key+" reset")
			} else {
				noteAudit(r, "", key)
			}
		}
		if _, touched := in["CONAN_MANAGER_NOTIFY_PROXY"]; touched {
			values, err := a.settings.Values(r.Context())
			if err == nil {
				err = a.notifier.UseProxy(panel.Overlay(values, os.Getenv)("CONAN_MANAGER_NOTIFY_PROXY"))
			}
			if err != nil {
				a.fail(w, r, http.StatusInternalServerError, err)
				return
			}
		}
	default:
		w.Header().Set("Allow", "GET, PUT")
		a.fail(w, r, http.StatusMethodNotAllowed, nil)
		return
	}
	views, pending, err := a.settingsView(r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": views, "restart_pending": pending,
		"can_restart": a.control.RestartManager != nil, "started_at": a.started})
}

// restartManager stops the manager so its service starts it again with the
// saved settings. Servers run in their own units and keep running.
func (a *api) restartManager(w http.ResponseWriter, r *http.Request) {
	if a.control.RestartManager == nil {
		a.fail(w, r, http.StatusConflict, errors.New("this manager cannot restart itself; restart the service"))
		return
	}
	a.logger.Warn("manager restart requested", "by", currentUser(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "restarting"})
	go func() {
		// Let the answer reach the browser first.
		time.Sleep(500 * time.Millisecond)
		a.control.RestartManager()
	}()
}
