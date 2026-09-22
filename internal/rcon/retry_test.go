package rcon

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// flakyServer refuses the first `refuse` logins even with the right password,
// as Conan now and then does.
func flakyServer(t *testing.T, refuse int32) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var logins atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				for {
					p, err := read(conn)
					if err != nil {
						return
					}
					switch p.kind {
					case typeAuth:
						id := p.id
						if logins.Add(1) <= refuse {
							id = -1
						}
						_ = write(conn, id, typeAuthResp, "")
					case typeCommand:
						_ = write(conn, p.id, typeAuthResp, "ok")
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String(), &logins
}

func TestDialRetryGetsPastARefusedRightPassword(t *testing.T) {
	addr, logins := flakyServer(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := DialRetry(ctx, addr, "pw", 3, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if n := logins.Load(); n != 3 {
		t.Fatalf("logins = %d, want 3", n)
	}
}

func TestDialRetryGivesUp(t *testing.T) {
	addr, logins := flakyServer(t, 100)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := DialRetry(ctx, addr, "pw", 3, 10*time.Millisecond); !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
	if n := logins.Load(); n != 3 {
		t.Fatalf("logins = %d, want exactly 3, to stay below RconMaxKarma", n)
	}
}
