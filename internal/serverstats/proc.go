package serverstats

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// clockTicks is USER_HZ, the unit of CPU times in /proc; it is 100 on every
// Linux build that matters here.
const clockTicks = 100

// Process is what Linux reports about the server process.
type Process struct {
	At         time.Time `json:"at"`
	RSS        uint64    `json:"rss_bytes"`
	Threads    int       `json:"threads"`
	CPUPercent float64   `json:"cpu_percent"`
	// CPUKnown is false on the first sample, which has nothing to compare to.
	CPUKnown bool `json:"cpu_known"`
	// cpuTicks is the process's user and system time, for the next sample.
	cpuTicks uint64
}

// Host is the machine as a whole.
type Host struct {
	CPUs         int     `json:"cpus"`
	Load1        float64 `json:"load1"`
	MemTotal     uint64  `json:"mem_total_bytes"`
	MemAvailable uint64  `json:"mem_available_bytes"`
}

// SampleProcess reads /proc for pid. CPUPercent is the share of one core
// since previous, so 250 means two and a half cores; it is zero on the first
// sample.
func SampleProcess(pid int, previous *Process) (Process, error) {
	if pid <= 0 {
		return Process{}, errors.New("no process")
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return Process{}, err
	}
	// The command name is in parentheses and may hold spaces; the fields
	// after the last ")" are fixed.
	s := string(stat)
	close := strings.LastIndexByte(s, ')')
	if close < 0 {
		return Process{}, errors.New("unreadable /proc stat")
	}
	fields := strings.Fields(s[close+1:])
	// fields[0] is field 3 (state); utime is field 14, stime 15, threads 20,
	// rss (pages) 24.
	if len(fields) < 22 {
		return Process{}, errors.New("short /proc stat")
	}
	utime, _ := strconv.ParseUint(fields[11], 10, 64)
	stime, _ := strconv.ParseUint(fields[12], 10, 64)
	threads, _ := strconv.Atoi(fields[17])
	pages, _ := strconv.ParseUint(fields[21], 10, 64)
	p := Process{At: time.Now(), RSS: pages * uint64(os.Getpagesize()), Threads: threads, cpuTicks: utime + stime}
	if previous != nil && !previous.At.IsZero() && p.cpuTicks >= previous.cpuTicks {
		if elapsed := p.At.Sub(previous.At).Seconds(); elapsed > 0 {
			p.CPUPercent = float64(p.cpuTicks-previous.cpuTicks) / clockTicks / elapsed * 100
			p.CPUKnown = true
		}
	}
	return p, nil
}

// SampleHost reads the host's memory and load.
func SampleHost() Host {
	h := Host{CPUs: runtime.NumCPU()}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			key, rest, _ := strings.Cut(scanner.Text(), ":")
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				continue
			}
			kb, _ := strconv.ParseUint(fields[0], 10, 64)
			switch key {
			case "MemTotal":
				h.MemTotal = kb * 1024
			case "MemAvailable":
				h.MemAvailable = kb * 1024
			}
		}
		f.Close()
	}
	if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
		if fields := strings.Fields(string(raw)); len(fields) > 0 {
			h.Load1, _ = strconv.ParseFloat(fields[0], 64)
		}
	}
	return h
}
