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
	// CPUPercent is how much of all the machine's cores were busy between two
	// readings of CPUTotal, so 100 means every core was saturated. CPUKnown is
	// false until there are two readings to compare.
	CPUPercent float64 `json:"cpu_percent"`
	CPUKnown   bool    `json:"cpu_known"`
}

// CPUTotal is the CPU time the whole machine has accumulated, from the first
// line of /proc/stat. It only means something as a difference between two
// readings: the counters run from boot.
type CPUTotal struct {
	At time.Time
	// Busy is every counter except idle and iowait, which are the time the
	// cores had nothing to run.
	Busy  uint64
	Total uint64
}

// SampleCPUTotal reads the machine's CPU counters.
func SampleCPUTotal() (CPUTotal, error) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return CPUTotal{}, err
	}
	return parseCPUTotal(string(raw))
}

func parseCPUTotal(raw string) (CPUTotal, error) {
	line, _, _ := strings.Cut(raw, "\n")
	fields := strings.Fields(line)
	// The aggregate line is "cpu" with no number; "cpu0" is one core.
	if len(fields) < 5 || fields[0] != "cpu" {
		return CPUTotal{}, errors.New("unreadable /proc/stat")
	}
	c := CPUTotal{At: time.Now()}
	for i, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return CPUTotal{}, errors.New("unreadable /proc/stat")
		}
		c.Total += value
		// The counters are user, nice, system, idle, iowait, and so on.
		if i != 3 && i != 4 {
			c.Busy += value
		}
	}
	return c, nil
}

// BusySince reports the share of the machine's CPUs that was busy between
// previous and c, from 0 to 100; ok is false without a usable pair, which
// includes the counters having been reset by a reboot.
func (c CPUTotal) BusySince(previous CPUTotal) (percent float64, ok bool) {
	if previous.At.IsZero() || c.Total <= previous.Total || c.Busy < previous.Busy {
		return 0, false
	}
	return float64(c.Busy-previous.Busy) / float64(c.Total-previous.Total) * 100, true
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
