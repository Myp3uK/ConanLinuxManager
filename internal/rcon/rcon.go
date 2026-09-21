// Package rcon speaks the Source RCON protocol that Conan Exiles' RconPlugin
// implements: a TCP connection, a password packet, then one command packet per
// request. Text is UTF-8 in both directions, so Cyrillic and spaces pass as is.
//
// Conan's implementation departs from Source in two ways, seen on a live UE5
// server: an answer carries the id of the previous request rather than its
// own (login with id 7 is answered with id 0, the next command with id 7),
// and answers have type 2 rather than 0. So answers cannot be matched to
// requests by id; the client sends one request at a time and takes what
// comes back as its answer. Only id -1, a refused login, is meaningful.
package rcon

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	typeResponse = 0
	typeCommand  = 2
	typeAuthResp = 2
	typeAuth     = 3

	// maxBody bounds a packet read from the server; the protocol allows 4096
	// bytes, and a larger size field means the stream is not RCON.
	maxBody = 1 << 16
	// maxCommand keeps a request within one packet, which is all Conan reads.
	maxCommand = 4000
	// splitAt is the packet size from which an answer may continue in the next
	// packet; the protocol cuts answers at 4096 bytes. A shorter packet is the
	// whole answer, so no time is spent waiting for more.
	splitAt = 4000
)

// ErrAuth means the server refused the password.
var ErrAuth = errors.New("rcon: wrong password")

// Client is one authenticated connection. It is safe for concurrent use;
// commands are sent one at a time.
type Client struct {
	mu     sync.Mutex
	conn   net.Conn
	nextID int32
	// settle is how long Exec waits for the next packet of an answer that was
	// cut at the packet limit. Conan sends nothing to mark the end of an answer.
	settle time.Duration
}

// Dial connects and logs in. The context bounds both.
func Dial(ctx context.Context, address, password string) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("rcon: connect %s: %w", address, err)
	}
	c := &Client{conn: conn, nextID: 1, settle: 1500 * time.Millisecond}
	if err := c.auth(ctx, password); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

// Close ends the connection.
func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) deadline(ctx context.Context) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	_ = c.conn.SetDeadline(deadline)
}

func (c *Client) auth(ctx context.Context, password string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deadline(ctx)
	id := c.id()
	if err := write(c.conn, id, typeAuth, password); err != nil {
		return err
	}
	// Source servers send an empty response before the auth answer; Conan
	// does not. Read until the auth answer arrives.
	for {
		p, err := read(c.conn)
		if err != nil {
			return fmt.Errorf("rcon: login: %w", err)
		}
		if p.kind != typeAuthResp {
			continue
		}
		if p.id == -1 {
			return ErrAuth
		}
		return nil
	}
}

// Exec runs one console command and returns the server's answer.
func (c *Client) Exec(ctx context.Context, command string) (string, error) {
	if strings.ContainsAny(command, "\x00\r\n") {
		return "", fmt.Errorf("rcon: a command must be a single line")
	}
	if len(command) > maxCommand {
		return "", fmt.Errorf("rcon: command longer than %d bytes", maxCommand)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deadline(ctx)
	if err := write(c.conn, c.id(), typeCommand, command); err != nil {
		return "", err
	}
	var answer strings.Builder
	p, err := read(c.conn)
	if err != nil {
		return "", fmt.Errorf("rcon: %q: %w", command, err)
	}
	answer.WriteString(p.body)
	// A long answer arrives as several full packets. A packet that fills the
	// limit may be followed by more; one exactly at the limit is not, which
	// only the silence that follows can tell.
	for p.size >= splitAt {
		_ = c.conn.SetReadDeadline(time.Now().Add(c.settle))
		p, err = read(c.conn)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				break
			}
			return "", fmt.Errorf("rcon: %q: %w", command, err)
		}
		answer.WriteString(p.body)
	}
	_ = c.conn.SetReadDeadline(time.Time{})
	return answer.String(), nil
}

func (c *Client) id() int32 {
	id := c.nextID
	c.nextID++
	if c.nextID <= 0 {
		c.nextID = 1
	}
	return id
}

type packet struct {
	size int32
	id   int32
	kind int32
	body string
}

func write(w io.Writer, id, kind int32, body string) error {
	var b bytes.Buffer
	size := int32(4 + 4 + len(body) + 2)
	_ = binary.Write(&b, binary.LittleEndian, size)
	_ = binary.Write(&b, binary.LittleEndian, id)
	_ = binary.Write(&b, binary.LittleEndian, kind)
	b.WriteString(body)
	b.Write([]byte{0, 0})
	if _, err := w.Write(b.Bytes()); err != nil {
		return fmt.Errorf("rcon: send: %w", err)
	}
	return nil
}

func read(r io.Reader) (packet, error) {
	var size int32
	if err := binary.Read(r, binary.LittleEndian, &size); err != nil {
		return packet{}, err
	}
	if size < 10 || size > maxBody {
		return packet{}, fmt.Errorf("invalid packet size %d", size)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return packet{}, err
	}
	p := packet{
		size: size,
		id:   int32(binary.LittleEndian.Uint32(buf[0:4])),
		kind: int32(binary.LittleEndian.Uint32(buf[4:8])),
	}
	// The body ends with two NULs; tolerate servers that send only one.
	p.body = strings.TrimRight(string(buf[8:]), "\x00")
	return p, nil
}
