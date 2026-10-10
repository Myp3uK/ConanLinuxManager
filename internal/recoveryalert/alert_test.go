package recoveryalert

import (
	"errors"
	"os"
	"testing"

	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

func TestWriteAndClear(t *testing.T) {
	cfg := server.Config{ID: "test", InstallDir: t.TempDir()}
	if err := Write(cfg, errors.New("disk full")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(Path(cfg))
	if err != nil || len(data) == 0 {
		t.Fatalf("alert file: %v", err)
	}
	if err := Clear(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Path(cfg)); !os.IsNotExist(err) {
		t.Fatalf("alert was not cleared")
	}
}
