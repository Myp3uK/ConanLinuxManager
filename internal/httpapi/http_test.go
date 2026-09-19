package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/example/conan-manager/internal/store"
)

func TestServerCRUDAPI(t *testing.T) {
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
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status: %d, body: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/servers/test", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("get status: %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/v1/servers/test", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status: %d", response.Code)
	}
}
