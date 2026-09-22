package controller

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/example/conan-manager/internal/server"
)

const (
	// crashTail is how much of each log end is read after a crash; the lines
	// that explain it are among the last the game wrote.
	crashTail = 256 << 10
	// crashLines and crashChars keep the excerpt to what fits a Discord field.
	crashLines = 8
	crashChars = 900
)

// crashLine picks the lines that tell why the game went down: Unreal errors
// and fatal messages, assertions and the crash handler's signal report.
var crashLine = regexp.MustCompile(`(?i)(\bError:|\bFatal\b|Critical error|Assertion failed|Unhandled Exception|Signal \d+|SIGSEGV|SIGABRT|Segmentation fault|CommonUnixCrashHandler|Out of memory)`)

// crashExcerpt returns the last lines of the game's and the process's logs
// that explain a crash, or "" when there are none.
func crashExcerpt(cfg server.Config) string {
	var picked []string
	for _, name := range []string{"game", "process"} {
		lines, err := tailLines(filepath.Join(cfg.InstallDir, LogNames[name]), crashTail)
		if err != nil {
			continue
		}
		for _, line := range lines {
			if crashLine.MatchString(line) {
				picked = append(picked, strings.TrimSpace(line))
			}
		}
	}
	if len(picked) > crashLines {
		picked = picked[len(picked)-crashLines:]
	}
	text := strings.Join(picked, "\n")
	if len(text) > crashChars {
		// The newest lines are kept; the cut lands on a line start.
		text = text[len(text)-crashChars:]
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	return text
}

func tailLines(path string, size int64) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	offset := info.Size() - size
	if offset < 0 {
		offset = 0
	}
	buf := make([]byte, info.Size()-offset)
	if _, err := f.ReadAt(buf, offset); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if offset > 0 {
		// The first line is cut in the middle.
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	return strings.Split(strings.ReplaceAll(string(buf), "\r", ""), "\n"), nil
}
