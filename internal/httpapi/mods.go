package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Myp3uK/ConanLinuxManager/internal/controller"
	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

// maxModsInRequest bounds a list a client may send; the repository enforces its
// own limit too, this one keeps a nonsense body from being decoded at all.
const maxModsInRequest = 500

// mods serves /api/v1/servers/{id}/mods[/check|/install|/collection].
func (a *api) mods(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/servers/"), "/"), "/")
	if len(parts) < 2 || len(parts) > 3 || parts[1] != "mods" || parts[0] == "" {
		a.fail(w, r, http.StatusNotFound, nil)
		return
	}
	id := parts[0]
	action := ""
	if len(parts) == 3 {
		action = parts[2]
	}
	switch {
	case action == "" && r.Method == http.MethodGet:
		// Reading does not ask Steam: the panel opens the tab often, and the
		// answer it needs is what the manager already knows.
		list, err := a.control.Mods(r.Context(), id)
		if err != nil {
			a.fail(w, r, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, modsResponse(list, ""))
	case action == "" && r.Method == http.MethodPut:
		var body struct {
			Mods []struct {
				WorkshopID string `json:"workshop_id"`
				Enabled    bool   `json:"enabled"`
			} `json:"mods"`
		}
		if err := decodeStrict(r, &body); err != nil {
			a.fail(w, r, http.StatusBadRequest, err)
			return
		}
		if len(body.Mods) > maxModsInRequest {
			a.fail(w, r, http.StatusBadRequest, fmt.Errorf("too many mods in one request"))
			return
		}
		// The order of the array is the load order, so it is taken as sent.
		list := make([]server.Mod, 0, len(body.Mods))
		for _, m := range body.Mods {
			list = append(list, server.Mod{WorkshopID: m.WorkshopID, Enabled: m.Enabled})
		}
		saved, err := a.control.SetMods(r.Context(), id, list)
		if err != nil {
			a.failMods(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, modsResponse(saved, ""))
	case action == "check" && r.Method == http.MethodPost:
		status, err := a.control.CheckMods(r.Context(), id)
		if err != nil {
			a.fail(w, r, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	case action == "collection" && r.Method == http.MethodPost:
		var body struct {
			Collection string `json:"collection"`
		}
		if err := decodeStrict(r, &body); err != nil {
			a.fail(w, r, http.StatusBadRequest, err)
			return
		}
		saved, err := a.control.AddCollection(r.Context(), id, body.Collection)
		if err != nil {
			a.failMods(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, modsResponse(saved, ""))
	case action == "install" && r.Method == http.MethodPost:
		var body controller.ModsOptions
		if r.ContentLength != 0 {
			if err := decodeStrict(r, &body); err != nil {
				a.fail(w, r, http.StatusBadRequest, err)
				return
			}
		}
		// Downloading a modpack outlasts any request, so it runs as a job the
		// client follows, like an update of the game files.
		job, err := a.control.ModsJob(r.Context(), id, body)
		if err != nil {
			a.fail(w, r, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusAccepted, job)
	default:
		a.fail(w, r, http.StatusNotFound, nil)
	}
}

// failMods tells a client's mistake from the manager's trouble: a bad Workshop
// id is the operator's to fix, an unreachable Steam is not.
func (a *api) failMods(w http.ResponseWriter, r *http.Request, err error) {
	var invalid server.ErrInvalidConfig
	if errors.As(err, &invalid) {
		a.fail(w, r, http.StatusBadRequest, err)
		return
	}
	a.fail(w, r, http.StatusConflict, err)
}

func modsResponse(list []server.Mod, failure string) controller.ModsStatus {
	status := controller.ModsStatus{Mods: list, Error: failure}
	for _, m := range list {
		if m.Enabled && m.Pending() {
			status.Pending++
		}
	}
	return status
}
