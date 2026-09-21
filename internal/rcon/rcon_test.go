package rcon

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeServer answers like Conan's RconPlugin on a live UE5 server: every
// answer has type 2 and the id of the previous request, and a long output is
// split into packets of chunk bytes.
func fakeServer(t *testing.T, password string, chunk int, handler func(string) string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				previous := int32(0)
				for {
					p, err := read(conn)
					if err != nil {
						return
					}
					id := previous
					previous = p.id
					switch p.kind {
					case typeAuth:
						if p.body != password {
							id = -1
						}
						_ = write(conn, id, typeAuthResp, "Authenticated.")
					case typeCommand:
						out := handler(p.body)
						for len(out) > chunk {
							_ = write(conn, id, typeAuthResp, out[:chunk])
							out = out[chunk:]
						}
						_ = write(conn, id, typeAuthResp, out)
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func TestExecPassesCyrillicAndSpacesThrough(t *testing.T) {
	addr := fakeServer(t, "пароль 1", 4096, func(cmd string) string { return "echo: " + cmd })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, addr, "пароль 1")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, cmd := range []string{"broadcast Сервер перезапустится через 5 минут", "listplayers"} {
		got, err := c.Exec(ctx, cmd)
		if err != nil || got != "echo: "+cmd {
			t.Fatalf("%q: got %q %v", cmd, got, err)
		}
	}
}

func TestExecJoinsAnAnswerSplitAcrossPackets(t *testing.T) {
	long := strings.Repeat("строка игрока | 76561198000000000\n", 400)
	addr := fakeServer(t, "p", 4000, func(string) string { return long })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, addr, "p")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got, err := c.Exec(ctx, "listplayers")
	if err != nil || got != long {
		t.Fatalf("got %d bytes, want %d: %v", len(got), len(long), err)
	}
}

func TestDialRejectsAWrongPassword(t *testing.T) {
	addr := fakeServer(t, "right", 4096, func(string) string { return "" })
	_, err := Dial(context.Background(), addr, "wrong")
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("got %v, want ErrAuth", err)
	}
}

func TestExecRefusesMultilineCommands(t *testing.T) {
	addr := fakeServer(t, "p", 4096, func(string) string { return "" })
	c, err := Dial(context.Background(), addr, "p")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Exec(context.Background(), "broadcast a\nquit"); err == nil {
		t.Fatal("a second line would run as its own command")
	}
}

func TestParsePlayers(t *testing.T) {
	out := "Idx | Char name | Player name | User ID | Platform ID | Platform Name\n" +
		"  0 | Конан | Arnold | FCB1 | 76561198000000001 | Steam\n" +
		"  1 | Valeria | val | FCB2 | 76561198000000002 | Steam\n"
	players := ParsePlayers(out)
	if len(players) != 2 || players[0]["Char name"] != "Конан" || players[1]["Platform ID"] != "76561198000000002" {
		t.Fatalf("%+v", players)
	}
	if got := ParsePlayers("Idx | Char name | Player name | User ID | Platform ID | Platform Name\n"); len(got) != 0 {
		t.Fatalf("empty server: %+v", got)
	}
}

func TestFromINI(t *testing.T) {
	s := FromINI(map[string]string{"RconEnabled": "True", "RconPort": "25704", "RconPassword": "example-rcon-pass", "RconMaxKarma": "240"})
	if s.Usable() != nil || s.Address() != "127.0.0.1:25704" || s.Password != "example-rcon-pass" {
		t.Fatalf("%+v %v", s, s.Usable())
	}
	if FromINI(map[string]string{"rconport": "1", "rconpassword": "x"}).Usable() == nil {
		t.Fatal("disabled RCON accepted")
	}
}

// Captured from a live Conan UE5 server with nobody online.
func TestParsePlayersOfAnEmptyLiveServer(t *testing.T) {
	out := "Idx | Char name | Player name | User ID | Platform ID | Platform Name\n"
	if got := ParsePlayers(out); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// A short answer is complete in one packet and must not wait out the pause
// that detects the end of a long one.
func TestShortAnswersDoNotWait(t *testing.T) {
	addr := fakeServer(t, "p", 4096, func(string) string { return "Message has been broadcast." })
	c, err := Dial(context.Background(), addr, "p")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	for i := 0; i < 5; i++ {
		if _, err := c.Exec(context.Background(), "broadcast hi"); err != nil {
			t.Fatal(err)
		}
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("five short commands took %v", took)
	}
}
