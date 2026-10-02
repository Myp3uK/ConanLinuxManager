package serverstats

import (
	"testing"
	"time"
)

// A real /proc/stat, cut to the lines that matter: the aggregate line first,
// then one line per core.
const procStat = `cpu  101 2 30 860 7 0 5 0 0 0
cpu0 50 1 15 430 3 0 2 0 0 0
cpu1 51 1 15 430 4 0 3 0 0 0
intr 123456 0 0
ctxt 987654
`

func TestParseCPUTotalSeparatesBusyFromIdle(t *testing.T) {
	got, err := parseCPUTotal(procStat)
	if err != nil {
		t.Fatal(err)
	}
	// Everything but idle (860) and iowait (7).
	if want := uint64(101 + 2 + 30 + 5); got.Busy != want {
		t.Errorf("busy = %d, want %d", got.Busy, want)
	}
	if want := uint64(101 + 2 + 30 + 860 + 7 + 5); got.Total != want {
		t.Errorf("total = %d, want %d", got.Total, want)
	}
	if got.At.IsZero() {
		t.Error("a reading needs the time it was taken")
	}
}

func TestParseCPUTotalRejectsWhatIsNotProcStat(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":       "",
		"one core":    "cpu0 50 1 15 430 3 0 2\n",
		"too short":   "cpu 1 2 3\n",
		"not numbers": "cpu  101 2 thirty 860 7\n",
	} {
		if _, err := parseCPUTotal(raw); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestBusySinceIsTheShareOfEveryCore(t *testing.T) {
	before := CPUTotal{At: time.Now().Add(-time.Minute), Busy: 100, Total: 1000}
	// A quarter of the time between the two readings went to work.
	after := CPUTotal{At: time.Now(), Busy: 200, Total: 1400}
	percent, ok := after.BusySince(before)
	if !ok {
		t.Fatal("a pair of readings should give an answer")
	}
	if percent < 24.9 || percent > 25.1 {
		t.Errorf("percent = %v, want 25", percent)
	}
}

func TestBusySinceWithoutAUsablePair(t *testing.T) {
	now := CPUTotal{At: time.Now(), Busy: 200, Total: 1400}
	if _, ok := now.BusySince(CPUTotal{}); ok {
		t.Error("the first reading has nothing to compare to")
	}
	// Counters that went backwards mean the machine rebooted in between.
	if _, ok := now.BusySince(CPUTotal{At: time.Now().Add(-time.Minute), Busy: 300, Total: 2000}); ok {
		t.Error("counters that fell cannot be divided")
	}
	// Two readings of the same instant would divide by zero.
	if _, ok := now.BusySince(CPUTotal{At: now.At, Busy: 200, Total: 1400}); ok {
		t.Error("an empty window has no answer")
	}
}
