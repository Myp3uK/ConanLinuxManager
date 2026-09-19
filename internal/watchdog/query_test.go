package watchdog

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"
)

func infoPacket() []byte {
	p := []byte{255, 255, 255, 255, 0x49, 17}
	p = append(p, []byte("Test\x00Map\x00conan\x00Conan Exiles\x00")...)
	p = append(p, 0, 0, 0, 40, 0, 'd', 'l', 0, 1)
	return append(p, []byte("1.0\x00")...)
}

func TestQueryDirectChallengeAndMalformed(t *testing.T) {
	for _, kind := range []string{"direct", "challenge", "malformed", "truncated", "split"} {
		t.Run(kind, func(t *testing.T) {
			conn, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			result := make(chan error, 1)
			go func() { result <- ProbeQuery(context.Background(), conn.LocalAddr().String(), time.Second) }()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			buf := make([]byte, 1024)
			n, peer, err := conn.ReadFrom(buf)
			if err != nil {
				t.Fatal(err)
			}
			initial := append([]byte(nil), buf[:n]...)
			if !bytes.Equal(initial, append([]byte{255, 255, 255, 255, 0x54}, []byte("Source Engine Query\x00")...)) {
				t.Fatal("invalid query request")
			}
			if kind == "challenge" {
				_, _ = conn.WriteTo([]byte{255, 255, 255, 255, 0x41, 1, 2, 3, 4}, peer)
				n, peer, err = conn.ReadFrom(buf)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(buf[:n], append(initial, 1, 2, 3, 4)) {
					t.Fatal("challenge not echoed")
				}
			}
			response := infoPacket()
			switch kind {
			case "malformed":
				response = []byte("not a query reply")
			case "truncated":
				response = response[:10]
			case "split":
				response[0] = 254
			}
			_, _ = conn.WriteTo(response, peer)
			err = <-result
			valid := kind == "direct" || kind == "challenge"
			if (err == nil) != valid {
				t.Fatalf("valid=%v err=%v", valid, err)
			}
		})
	}
}

func TestQueryTimeoutAndCancellation(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	if err := ProbeQuery(context.Background(), conn.LocalAddr().String(), 50*time.Millisecond); err == nil {
		t.Fatal("missing timeout")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout not bounded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- ProbeQuery(ctx, conn.LocalAddr().String(), time.Minute) }()
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancellation ignored")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("probe did not cancel")
	}
}
