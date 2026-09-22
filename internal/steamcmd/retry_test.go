package steamcmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSteamError(t *testing.T) {
	out := "Loading Steam API...OK\r\n Update state (0x3) reconfiguring, progress: 0.00 (0 / 0)\n" +
		"ERROR! Failed to install app '443030' (Missing configuration)\n\n"
	if got := steamError(out); got != "ERROR! Failed to install app '443030' (Missing configuration)" {
		t.Fatalf("%q", got)
	}
	if !transient(steamError(out)) || transient("ERROR! Failed to install app '443030' (Disk write failure)") {
		t.Fatal("transient")
	}
	if steamError("Success! App '443030' fully installed.\n") != "" {
		t.Fatal("success read as error")
	}
}

func TestTailBuffer(t *testing.T) {
	b := &tailBuffer{max: 5}
	b.Write([]byte("abc"))
	b.Write([]byte("defg"))
	if b.String() != "cdefg" {
		t.Fatal(b.String())
	}
}

// fakeSteam writes a SteamCMD that fails the first `fails` runs with the
// given line and succeeds afterwards.
func fakeSteam(t *testing.T, fails int, line string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	script := "#!/bin/sh\nn=$(cat " + count + " 2>/dev/null || echo 0)\nn=$((n+1))\necho $n > " + count +
		"\nif [ $n -le " + string(rune('0'+fails)) + " ]; then echo \"" + line + "\"; exit 8; fi\necho \"Success! App '443030' fully installed.\"\n"
	binary := filepath.Join(dir, "steamcmd.sh")
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary, count
}

func TestRunUpdateRetriesMissingConfiguration(t *testing.T) {
	retryPause = 0
	binary, count := fakeSteam(t, 1, "ERROR! Failed to install app '443030' (Missing configuration)")
	var out bytes.Buffer
	if err := runUpdate(context.Background(), binary, filepath.Dir(binary), nil, &out); err != nil {
		t.Fatal(err, out.String())
	}
	if n, _ := os.ReadFile(count); strings.TrimSpace(string(n)) != "2" {
		t.Fatalf("runs: %s", n)
	}
	if !strings.Contains(out.String(), "trying again (2 of 3)") {
		t.Fatal(out.String())
	}
}

func TestRunUpdateReportsTheReason(t *testing.T) {
	retryPause = 0
	binary, count := fakeSteam(t, 9, "ERROR! Failed to install app '443030' (Disk write failure)")
	err := runUpdate(context.Background(), binary, filepath.Dir(binary), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "Disk write failure") {
		t.Fatal(err)
	}
	// Not a failure that a second run fixes.
	if n, _ := os.ReadFile(count); strings.TrimSpace(string(n)) != "1" {
		t.Fatalf("runs: %s", n)
	}
	binary, count = fakeSteam(t, 9, "ERROR! Failed to install app '443030' (Missing configuration)")
	if err := runUpdate(context.Background(), binary, filepath.Dir(binary), nil, nil); err == nil {
		t.Fatal("endless failure accepted")
	}
	if n, _ := os.ReadFile(count); strings.TrimSpace(string(n)) != "3" {
		t.Fatalf("runs: %s", n)
	}
}
