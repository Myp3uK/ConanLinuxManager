package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/store"
)

func TestServerCRUDAPI(t *testing.T) {
	t.Setenv("CONAN_MANAGER_ADMIN_USERNAME", "admin")
	t.Setenv("CONAN_MANAGER_ADMIN_PASSWORD", "test-password")
	db, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := New(db, slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)))

	payload := map[string]any{
		"id": "test", "name": "Test", "engine": "ue5", "runtime": "native",
		"install_dir": "/srv/test", "steamcmd_dir": "/srv/steamcmd",
		"game_port": 7777, "query_port": 27015, "ramdisk_enabled": false,
	}
	body, _ := json.Marshal(payload)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(body))
	request.SetBasicAuth("admin", "test-password")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status: %d, body: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/servers/test", nil)
	request.SetBasicAuth("admin", "test-password")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("get status: %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/v1/servers/test", nil)
	request.SetBasicAuth("admin", "test-password")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status: %d", response.Code)
	}
}

func TestScheduleAPI(t *testing.T) {
	t.Setenv("CONAN_MANAGER_ADMIN_USERNAME", "admin")
	t.Setenv("CONAN_MANAGER_ADMIN_PASSWORD", "test-password")
	db, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := New(db, slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)))
	payload := []byte(`{"id":"test","name":"Test","engine":"ue5","runtime":"native","install_dir":"/srv/test","steamcmd_dir":"/srv/steamcmd","game_port":7777,"query_port":27015,"ramdisk_enabled":false}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(payload))
	request.SetBasicAuth("admin", "test-password")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status: %d", response.Code)
	}

	body, _ := json.Marshal(map[string]any{"action": "stop", "enabled": true, "interval_seconds": 3600, "next_run": time.Now().UTC().Add(time.Hour)})
	request = httptest.NewRequest(http.MethodPut, "/api/v1/servers/test/schedule", bytes.NewReader(body))
	request.SetBasicAuth("admin", "test-password")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("schedule put status: %d, body: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/servers/test/schedule", nil)
	request.SetBasicAuth("admin", "test-password")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"action":"stop"`)) {
		t.Fatalf("schedule get: %d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/servers/test/schedule/runs", nil)
	request.SetBasicAuth("admin", "test-password")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("schedule runs status: %d", response.Code)
	}
}

func TestAPIRequiresCredentialsButHealthIsPublic(t *testing.T) {
	t.Setenv("CONAN_MANAGER_ADMIN_USERNAME", "")
	t.Setenv("CONAN_MANAGER_ADMIN_PASSWORD", "")
	db, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := New(db, slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)))

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("health status: %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/info", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured auth status: %d", response.Code)
	}
}

func TestAPIRejectsCrossOriginMutation(t *testing.T) {
	t.Setenv("CONAN_MANAGER_ADMIN_USERNAME", "admin")
	t.Setenv("CONAN_MANAGER_ADMIN_PASSWORD", "test-password")
	db, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := New(db, slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)))
	request := httptest.NewRequest(http.MethodPost, "http://manager.test/api/v1/servers", bytes.NewReader([]byte(`{}`)))
	request.SetBasicAuth("admin", "test-password")
	request.Header.Set("Origin", "https://evil.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin mutation status: %d", response.Code)
	}
}

func TestNotificationChannelAPI(t *testing.T) {
	t.Setenv("CONAN_MANAGER_ADMIN_USERNAME", "admin")
	t.Setenv("CONAN_MANAGER_ADMIN_PASSWORD", "test-password")
	token := "abcdefghijklmnopqrstuvwxyz_SECRET_0123456789"
	call := func(handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
		var reader *bytes.Reader
		if body != nil {
			data, _ := json.Marshal(body)
			reader = bytes.NewReader(data)
		} else {
			reader = bytes.NewReader(nil)
		}
		request := httptest.NewRequest(method, path, reader)
		request.SetBasicAuth("admin", "test-password")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	channel := map[string]any{"name": "Admins", "webhook_url": "https://discord.com/api/webhooks/123456789012345678/" + token, "mention": "<@&123456789012345>"}

	// Without a secret key the URL could only be stored in clear, so it is refused.
	t.Setenv("CONAN_MANAGER_SECRET_KEY", "")
	db, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if r := call(New(db, slog.New(slog.DiscardHandler)), http.MethodPost, "/api/v1/notifications", channel); r.Code != http.StatusBadRequest {
		t.Fatalf("created without a secret key: %d %s", r.Code, r.Body.String())
	}

	t.Setenv("CONAN_MANAGER_SECRET_KEY", "a-long-test-master-key")
	handler := New(db, slog.New(slog.DiscardHandler))
	created := call(handler, http.MethodPost, "/api/v1/notifications", channel)
	if created.Code != http.StatusCreated || bytes.Contains(created.Body.Bytes(), []byte(token)) {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var got struct{ ID, Webhook string }
	_ = json.Unmarshal(created.Body.Bytes(), &got)
	if got.ID == "" || got.Webhook == "" {
		t.Fatalf("created channel: %s", created.Body.String())
	}
	if r := call(handler, http.MethodPost, "/api/v1/notifications", map[string]any{"name": "x", "webhook_url": "https://example.com/hook"}); r.Code != http.StatusBadRequest {
		t.Fatalf("foreign webhook accepted: %d", r.Code)
	}
	if r := call(handler, http.MethodPatch, "/api/v1/notifications/"+got.ID, map[string]any{"min_severty": "error"}); r.Code != http.StatusBadRequest {
		t.Fatalf("misspelt field accepted: %d", r.Code)
	}
	if r := call(handler, http.MethodPatch, "/api/v1/notifications/"+got.ID, map[string]any{"min_severity": "error"}); r.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", r.Code, r.Body.String())
	}
	list := call(handler, http.MethodGet, "/api/v1/notifications", nil)
	if list.Code != http.StatusOK || bytes.Contains(list.Body.Bytes(), []byte(token)) || !bytes.Contains(list.Body.Bytes(), []byte(`"min_severity":"error"`)) {
		t.Fatalf("list: %s", list.Body.String())
	}
	if r := call(handler, http.MethodDelete, "/api/v1/notifications/"+got.ID, nil); r.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", r.Code)
	}
	if r := call(handler, http.MethodGet, "/api/v1/events?limit=5", nil); r.Code != http.StatusOK {
		t.Fatalf("events: %d %s", r.Code, r.Body.String())
	}
}

// TestServerDetailCarriesTheNoteAndTheModCount covers what the panel's cards
// and the overview show without opening another tab.
func TestServerDetailCarriesTheNoteAndTheModCount(t *testing.T) {
	t.Setenv("CONAN_MANAGER_ADMIN_USERNAME", "admin")
	t.Setenv("CONAN_MANAGER_ADMIN_PASSWORD", "test-password")
	db, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := New(db, slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)))
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		var reader *bytes.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		} else {
			reader = bytes.NewReader(nil)
		}
		request := httptest.NewRequest(method, path, reader)
		request.SetBasicAuth("admin", "test-password")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	detail := func() map[string]any {
		response := call(http.MethodGet, "/api/v1/servers/test", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("get status: %d, body: %s", response.Code, response.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	dir := t.TempDir()
	created := call(http.MethodPost, "/api/v1/servers", map[string]any{
		"id": "test", "name": "Test", "engine": "ue5", "runtime": "native",
		"install_dir": dir, "steamcmd_dir": filepath.Join(dir, "steamcmd"),
		"game_port": 7777, "query_port": 27015,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status: %d, body: %s", created.Code, created.Body.String())
	}
	if got := detail()["mod_count"]; got != float64(0) {
		t.Errorf("a new server has no mods, got %v", got)
	}

	// Whitespace around a comment is the operator's typing, not content.
	if response := call(http.MethodPatch, "/api/v1/servers/test", map[string]any{"note": "  PVE-C, вайп 1 числа \n"}); response.Code != http.StatusOK {
		t.Fatalf("patch status: %d, body: %s", response.Code, response.Body.String())
	}
	if got := detail()["server"].(map[string]any)["note"]; got != "PVE-C, вайп 1 числа" {
		t.Errorf("note = %q", got)
	}

	if response := call(http.MethodPut, "/api/v1/servers/test/mods", map[string]any{"mods": []any{
		map[string]any{"workshop_id": "880454836", "enabled": true},
		map[string]any{"workshop_id": "1113901982", "enabled": false},
	}}); response.Code != http.StatusOK {
		t.Fatalf("mods status: %d, body: %s", response.Code, response.Body.String())
	}
	// A mod that is switched off is still one the server carries.
	if got := detail()["mod_count"]; got != float64(2) {
		t.Errorf("mod_count = %v, want 2", got)
	}
}

func TestHostEndpointAnswers(t *testing.T) {
	t.Setenv("CONAN_MANAGER_ADMIN_USERNAME", "admin")
	t.Setenv("CONAN_MANAGER_ADMIN_PASSWORD", "test-password")
	db, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := New(db, slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/host", nil)
	request.SetBasicAuth("admin", "test-password")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("host status: %d, body: %s", response.Code, response.Body.String())
	}
	var host struct {
		CPUs     int  `json:"cpus"`
		CPUKnown bool `json:"cpu_known"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &host); err != nil {
		t.Fatal(err)
	}
	if host.CPUs < 1 {
		t.Errorf("the machine has at least one core, got %d", host.CPUs)
	}
	// The first call has no earlier reading to measure against.
	if host.CPUKnown {
		t.Error("the first call cannot know how busy the machine was")
	}
}
