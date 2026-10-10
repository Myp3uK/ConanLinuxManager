package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Myp3uK/ConanLinuxManager/internal/controller"
	"github.com/Myp3uK/ConanLinuxManager/internal/store"
)

func modsAPI(t *testing.T) http.Handler {
	t.Helper()
	t.Setenv("CONAN_MANAGER_ADMIN_USERNAME", "admin")
	t.Setenv("CONAN_MANAGER_ADMIN_PASSWORD", "test-password")
	db, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	handler := New(db, slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)))
	payload := []byte(`{"id":"test","name":"Test","engine":"ue5","runtime":"native","install_dir":"` +
		filepath.ToSlash(filepath.Join(t.TempDir(), "game")) +
		`","steamcmd_dir":"/srv/steamcmd","game_port":7777,"query_port":27015,"ramdisk_enabled":false}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(payload))
	request.SetBasicAuth("admin", "test-password")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create server: %d, %s", response.Code, response.Body)
	}
	return handler
}

func modsCall(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	request := httptest.NewRequest(method, path, reader)
	request.SetBasicAuth("admin", "test-password")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestModsAPIKeepsTheOrderItWasSent(t *testing.T) {
	handler := modsAPI(t)
	response := modsCall(t, handler, http.MethodPut, "/api/v1/servers/test/mods",
		`{"mods":[{"workshop_id":"222222","enabled":true},{"workshop_id":"111111","enabled":false}]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("put: %d, %s", response.Code, response.Body)
	}
	var status controller.ModsStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	// The array's order is the load order, so it must come back as sent.
	if len(status.Mods) != 2 || status.Mods[0].WorkshopID != "222222" || status.Mods[1].WorkshopID != "111111" {
		t.Fatalf("unexpected list: %+v", status.Mods)
	}
	if status.Mods[1].Enabled {
		t.Errorf("mod 111111 should be disabled: %+v", status.Mods[1])
	}
	// Only the enabled mod is waiting to be installed.
	if status.Pending != 1 {
		t.Errorf("pending = %d", status.Pending)
	}
	if response = modsCall(t, handler, http.MethodGet, "/api/v1/servers/test/mods", ""); response.Code != http.StatusOK {
		t.Fatalf("get: %d, %s", response.Code, response.Body)
	}
	var again controller.ModsStatus
	if err := json.Unmarshal(response.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if len(again.Mods) != 2 || again.Mods[0].WorkshopID != "222222" {
		t.Fatalf("list not kept: %+v", again.Mods)
	}
}

func TestModsAPIAcceptsALinkAndRefusesNonsense(t *testing.T) {
	handler := modsAPI(t)
	response := modsCall(t, handler, http.MethodPut, "/api/v1/servers/test/mods",
		`{"mods":[{"workshop_id":"https://steamcommunity.com/sharedfiles/filedetails/?id=880454836","enabled":true}]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("a Workshop link should be accepted: %d, %s", response.Code, response.Body)
	}
	var status controller.ModsStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Mods) != 1 || status.Mods[0].WorkshopID != "880454836" {
		t.Fatalf("link not read: %+v", status.Mods)
	}
	// A mistyped id is the operator's to fix, so it is a 400 and not a 409.
	response = modsCall(t, handler, http.MethodPut, "/api/v1/servers/test/mods", `{"mods":[{"workshop_id":"Pippi","enabled":true}]}`)
	if response.Code != http.StatusBadRequest {
		t.Errorf("bad id: %d, %s", response.Code, response.Body)
	}
	response = modsCall(t, handler, http.MethodPut, "/api/v1/servers/test/mods",
		`{"mods":[{"workshop_id":"111111","enabled":true},{"workshop_id":"111111","enabled":true}]}`)
	if response.Code != http.StatusBadRequest {
		t.Errorf("duplicate: %d, %s", response.Code, response.Body)
	}
	response = modsCall(t, handler, http.MethodPut, "/api/v1/servers/test/mods", `{"mods":[{"workshop_id":"111111","unknown":1}]}`)
	if response.Code != http.StatusBadRequest {
		t.Errorf("unknown field: %d, %s", response.Code, response.Body)
	}
}

func TestModsAPIRejectsWrongMethodsAndUnknownServers(t *testing.T) {
	handler := modsAPI(t)
	if response := modsCall(t, handler, http.MethodDelete, "/api/v1/servers/test/mods", ""); response.Code != http.StatusNotFound {
		t.Errorf("delete: %d", response.Code)
	}
	if response := modsCall(t, handler, http.MethodGet, "/api/v1/servers/test/mods/check", ""); response.Code != http.StatusNotFound {
		t.Errorf("GET on check: %d", response.Code)
	}
	if response := modsCall(t, handler, http.MethodGet, "/api/v1/servers/nope/mods", ""); response.Code != http.StatusNotFound {
		t.Errorf("unknown server: %d", response.Code)
	}
	if response := modsCall(t, handler, http.MethodGet, "/api/v1/servers/test/mods/nonsense", ""); response.Code != http.StatusNotFound {
		t.Errorf("unknown action: %d", response.Code)
	}
}
