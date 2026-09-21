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
	// This file exists to survive the failure it reports, so it is flushed to
	// disk before the rename rather than left in the page cache.
	if err := writeSynced(temporary, append(data, '\n')); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	// The rename itself must be durable, otherwise the alert can disappear in
	// exactly the crash it was written to report.
	return syncDirectory(filepath.Dir(path))
}

func Clear(cfg server.Config) error {
	path := Path(cfg)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return closeErr
	}
	return nil
}

// Pending reports the servers whose alert file is still present. It reads the
// filesystem directly so an operator or a health check can find outstanding
// recovery conditions without asking the manager over HTTP.
func Pending(configs []server.Config) ([]Alert, error) {
	result := make([]Alert, 0)
	for _, cfg := range configs {
		data, err := os.ReadFile(Path(cfg))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return result, err
		}
		var alert Alert
		if err := json.Unmarshal(data, &alert); err != nil {
			// A damaged alert still means the server needs attention.
			alert = Alert{Version: 0, ServerID: cfg.ID, State: "recovery_required", Error: "unreadable alert file: " + err.Error()}
		}
		result = append(result, alert)
	}
	return result, nil
}

func sessionPath(cfg server.Config) string {
	return filepath.Join(cfg.InstallDir, "ConanSandbox", ".conan-manager-ram-session")
}
