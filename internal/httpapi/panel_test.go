package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/conan-manager/internal/store"
)

func panelHandler(t *testing.T) http.Handler {
	t.Helper()
	t.Setenv("CONAN_MANAGER_ADMIN_USERNAME", "admin")
	t.Setenv("CONAN_MANAGER_ADMIN_PASSWORD", "test-password")
	t.Setenv("CONAN_MANAGER_NOTIFY_PROXY", "")
	db, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func call(t *testing.T, h http.Handler, user, pass, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	request := httptest.NewRequest(method, path, reader)
	request.SetBasicAuth(user, pass)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}

func TestSecondAdministrator(t *testing.T) {
	h := panelHandler(t)
	if r := call(t, h, "admin", "test-password", http.MethodPost, "/api/v1/admins", map[string]string{"username": "moderator", "password": "short"}); r.Code != http.StatusBadRequest {
		t.Fatalf("short password: %d %s", r.Code, r.Body)
	}
	if r := call(t, h, "admin", "test-password", http.MethodPost, "/api/v1/admins", map[string]string{"username": "moderator", "password": "a-long-password"}); r.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", r.Code, r.Body)
	}
	if r := call(t, h, "admin", "test-password", http.MethodPost, "/api/v1/admins", map[string]string{"username": "Moderator", "password": "a-long-password"}); r.Code != http.StatusConflict {
		t.Fatalf("duplicate: %d", r.Code)
	}
	if r := call(t, h, "admin", "test-password", http.MethodPost, "/api/v1/admins", map[string]string{"username": "admin", "password": "a-long-password"}); r.Code != http.StatusConflict {
		t.Fatalf("environment name: %d", r.Code)
	}
	r := call(t, h, "moderator", "a-long-password", http.MethodGet, "/api/v1/me", nil)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"moderator"`) {
		t.Fatalf("second admin login: %d %s", r.Code, r.Body)
	}
	if r := call(t, h, "moderator", "wrong-password", http.MethodGet, "/api/v1/me", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", r.Code)
	}
	if r := call(t, h, "moderator", "a-long-password", http.MethodPut, "/api/v1/admins/moderator/password", map[string]string{"password": "another-password"}); r.Code != http.StatusNoContent {
		t.Fatalf("change password: %d %s", r.Code, r.Body)
	}
	// The trusted login is dropped with the old password.
	if r := call(t, h, "moderator", "a-long-password", http.MethodGet, "/api/v1/me", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("old password still works: %d", r.Code)
	}
	if r := call(t, h, "admin", "test-password", http.MethodDelete, "/api/v1/admins/admin", nil); r.Code != http.StatusNotFound {
		t.Fatalf("environment admin removed: %d", r.Code)
	}
	if r := call(t, h, "admin", "test-password", http.MethodDelete, "/api/v1/admins/moderator", nil); r.Code != http.StatusNoContent {
		t.Fatalf("remove: %d", r.Code)
	}
	if r := call(t, h, "moderator", "another-password", http.MethodGet, "/api/v1/me", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("removed admin still logs in: %d", r.Code)
	}
}

func TestPanelSettings(t *testing.T) {
	h := panelHandler(t)
	put := func(body map[string]any) *httptest.ResponseRecorder {
		return call(t, h, "admin", "test-password", http.MethodPut, "/api/v1/panel/settings", body)
	}
	if r := put(map[string]any{"CONAN_MANAGER_HOOKS_DIR": "/tmp"}); r.Code != http.StatusBadRequest {
		t.Fatalf("hooks dir accepted: %d", r.Code)
	}
	if r := put(map[string]any{"CONAN_MANAGER_STOP_TIMEOUT": "soon", "CONAN_MANAGER_BACKUP_KEEP": "5"}); r.Code != http.StatusBadRequest {
		t.Fatalf("bad duration accepted: %d", r.Code)
	}
	r := put(map[string]any{"CONAN_MANAGER_NOTIFY_PROXY": "socks5://user:secret@proxy.example:1080", "CONAN_MANAGER_BACKUP_KEEP": "5"})
	if r.Code != http.StatusOK {
		t.Fatalf("save: %d %s", r.Code, r.Body)
	}
	var out struct {
		Settings []struct {
			Key, Value, Source string
		}
		RestartPending bool `json:"restart_pending"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.Body.String(), "secret") {
		t.Fatalf("proxy password shown: %s", r.Body)
	}
	if !out.RestartPending {
		t.Fatal("backup keep change should wait for a restart")
	}
	for _, s := range out.Settings {
		if s.Key == "CONAN_MANAGER_BACKUP_KEEP" && (s.Value != "5" || s.Source != "panel") {
			t.Fatalf("backup keep: %+v", s)
		}
	}
	// Sending the masked proxy back keeps the stored password.
	masked := "socks5://user:xxxxx@proxy.example:1080"
	if r := put(map[string]any{"CONAN_MANAGER_NOTIFY_PROXY": masked}); r.Code != http.StatusOK {
		t.Fatalf("masked resend: %d %s", r.Code, r.Body)
	}
	if r := put(map[string]any{"CONAN_MANAGER_BACKUP_KEEP": nil}); r.Code != http.StatusOK || strings.Contains(r.Body.String(), `"restart_pending":true`) {
		t.Fatalf("reset: %d %s", r.Code, r.Body)
	}
}
