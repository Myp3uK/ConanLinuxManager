package recoveryalert

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/example/conan-manager/internal/server"
)

type Alert struct {
	Version   int       `json:"version"`
	ServerID  string    `json:"server_id"`
	State     string    `json:"state"`
	Error     string    `json:"error"`
	CreatedAt time.Time `json:"created_at"`
}

func Path(cfg server.Config) string {
	return filepath.Join(filepath.Dir(sessionPath(cfg)), ".conan-manager-recovery_required.json")
}

func Write(cfg server.Config, cause error) error {
	if cause == nil {
		return fmt.Errorf("recovery alert error is required")
	}
	data, err := json.MarshalIndent(Alert{Version: 1, ServerID: cfg.ID, State: "recovery_required", Error: cause.Error(), CreatedAt: time.Now().UTC()}, "", "  ")
	if err != nil {
		return err
	}
	path := Path(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o640); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func Clear(cfg server.Config) error {
	if err := os.Remove(Path(cfg)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func sessionPath(cfg server.Config) string {
	return filepath.Join(cfg.InstallDir, "ConanSandbox", ".conan-manager-ram-session")
}
