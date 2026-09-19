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
