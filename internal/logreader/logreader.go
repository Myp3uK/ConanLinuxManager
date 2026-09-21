package logreader

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
)

type Reader struct{}

// maxScan bounds how far back Tail reads looking for matching lines, so a
// filter that matches nothing in a large log does not read all of it.
const maxScan = 64 << 20

const chunk = 64 << 10

// Tail returns the last limit lines of a log. It reads from the end
// backwards, so the cost follows the lines asked for rather than the size of
// the log; a Conan log grows to hundreds of megabytes over a long uptime.
func (r Reader) Tail(path string, limit int) ([]string, error) {
	return r.Search(path, limit, "")
}

// Search is Tail restricted to lines containing filter, compared without
// regard to case. An empty filter matches every line.
func (Reader) Search(path string, limit int, filter string) ([]string, error) {
	if limit < 1 || limit > 10000 {
		return nil, fmt.Errorf("limit must be between 1 and 10000")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("read log: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	needle := strings.ToLower(filter)
	// Lines are collected newest first and reversed at the end.
	var found []string
	var carry []byte // the start of a line whose beginning is further back
	offset := info.Size()
	scanned := int64(0)
	take := func(line []byte) bool {
		text := strings.TrimSuffix(string(line), "\r")
		if needle == "" || strings.Contains(strings.ToLower(text), needle) {
			found = append(found, text)
		}
		return len(found) >= limit
	}
	// A log that ends with a newline has no empty last line.
	skipEmptyLast := true
	for offset > 0 && scanned < maxScan {
		size := int64(chunk)
		if size > offset {
			size = offset
		}
		offset -= size
		scanned += size
		buf := make([]byte, size, size+int64(len(carry)))
		if _, err := file.ReadAt(buf, offset); err != nil && err != io.EOF {
			return nil, fmt.Errorf("read log: %w", err)
		}
		buf = append(buf, carry...)
		for {
			i := bytes.LastIndexByte(buf, '\n')
			if i < 0 {
				break
			}
			line := buf[i+1:]
			buf = buf[:i]
			if skipEmptyLast {
				skipEmptyLast = false
				if len(line) == 0 {
					continue
				}
			}
			if take(line) {
				return reverse(found), nil
			}
		}
		skipEmptyLast = false
		carry = append([]byte(nil), buf...)
	}
	if offset == 0 && len(carry) > 0 {
		take(carry)
	}
	return reverse(found), nil
}

func reverse(lines []string) []string {
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	if lines == nil {
		lines = []string{}
	}
	return lines
}
