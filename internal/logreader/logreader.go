package logreader

import (
	"bufio"
	"fmt"
	"os"
)

type Reader struct{}

// Tail reads logs only when called; it does not keep a stream or watcher open.
func (Reader) Tail(path string, limit int) ([]string, error) {
	if limit < 1 || limit > 10000 {
		return nil, fmt.Errorf("limit must be between 1 and 10000")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	defer file.Close()
	lines := make([]string, 0, limit)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if len(lines) == limit {
			copy(lines, lines[1:])
			lines[len(lines)-1] = scanner.Text()
		} else {
			lines = append(lines, scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read log: %w", err)
	}
	return lines, nil
}
