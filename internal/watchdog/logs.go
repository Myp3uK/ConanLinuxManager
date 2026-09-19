// Package watchdog inspects bounded log windows; query replies are deliberately
// not used as evidence that the game thread is healthy.
package watchdog

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const MaxLogBytes int64 = 256 << 20
const ReadBudget int64 = 1 << 20

type cursor struct {
	info   os.FileInfo
	offset int64
	tail   []byte
}

type Logs struct{ files map[string]*cursor }

func New(paths []string) *Logs {
	w := &Logs{files: make(map[string]*cursor)}
	for _, path := range paths {
		c := &cursor{}
		if info, err := os.Stat(path); err == nil {
			c.info, c.offset = info, info.Size()
		}
		w.files[path] = c
	}
	return w
}

func Paths(install string) []string {
	return []string{filepath.Join(install, "conan-manager-process.log"), filepath.Join(install, "ConanSandbox", "Saved", "Logs", "ConanSandbox.log")}
}

// Check never reads more than ReadBudget per file. If the producer outruns the
// budget, fail closed rather than silently skipping potential hang messages.
func (w *Logs) Check() (reason string, err error) {
	for path, c := range w.files {
		f, err := os.Open(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		reason, err := checkFile(f, path, c)
		_ = f.Close()
		if reason != "" || err != nil {
			return reason, err
		}
	}
	return "", nil
}

func checkFile(f *os.File, path string, c *cursor) (string, error) {
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("watchdog log is not a regular file: %s", path)
	}
	if info.Size() >= MaxLogBytes {
		return "log_size_limit: " + path, nil
	}
	if c.info == nil || !os.SameFile(c.info, info) || info.Size() < c.offset {
		c.offset, c.tail = 0, nil
	}
	c.info = info
	if info.Size()-c.offset > ReadBudget {
		return "log_flood: " + path, nil
	}
	data := make([]byte, info.Size()-c.offset)
	n, err := f.ReadAt(data, c.offset)
	if err != nil && err != io.EOF {
		return "", err
	}
	c.offset += int64(n)
	data = append(c.tail, data[:n]...)
	if bytes.Contains(data, []byte("Hang detected on GameThread")) {
		return "game_thread_hang: " + path, nil
	}
	if len(data) > 128 {
		data = data[len(data)-128:]
	}
	c.tail = append([]byte(nil), data...)
	return "", nil
}
