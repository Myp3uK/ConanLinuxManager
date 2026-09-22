package controller

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/example/conan-manager/internal/server"
)

// steamLogMax rotates the SteamCMD log: a full install prints tens of
// thousands of progress lines.
const steamLogMax = 4 << 20

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// openSteamLog opens the server's SteamCMD log for one run, marked with what
// runs and when. The log is a convenience: if it cannot be written, SteamCMD
// runs anyway.
func openSteamLog(cfg server.Config, what string) io.WriteCloser {
	path := filepath.Join(cfg.InstallDir, LogNames["steamcmd"])
	if err := os.MkdirAll(cfg.InstallDir, 0o750); err != nil {
		return nopWriteCloser{io.Discard}
	}
	if info, err := os.Stat(path); err == nil && info.Size() > steamLogMax {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nopWriteCloser{io.Discard}
	}
	fmt.Fprintf(f, "\n=== conan-manager: %s at %s ===\n", what, time.Now().UTC().Format(time.RFC3339))
	return lineWriter{f}
}

// lineWriter turns SteamCMD's carriage-return progress updates into lines,
// so each shows up in the log view.
type lineWriter struct{ f *os.File }

func (w lineWriter) Write(p []byte) (int, error) {
	if _, err := w.f.Write(bytes.ReplaceAll(p, []byte("\r"), []byte("\n"))); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w lineWriter) Close() error { return w.f.Close() }
