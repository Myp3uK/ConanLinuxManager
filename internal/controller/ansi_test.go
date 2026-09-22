package controller

import "testing"

func TestStripANSI(t *testing.T) {
	for in, want := range map[string]string{
		"\x1b[0mWaiting for client config...\x1b[0mOK": "Waiting for client config...OK",
		"\x1b[1;31mERROR!\x1b[0m Failed":               "ERROR! Failed",
		"plain line":                                   "plain line",
		"lone \x1b escape":                             "lone  escape",
	} {
		if got := stripANSI(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
