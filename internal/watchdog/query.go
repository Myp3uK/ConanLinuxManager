package watchdog

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"time"
)

// ProbeQuery supports single-packet A2S_INFO and the Steam challenge handshake.
// A valid response indicates query responsiveness, not GameThread health.
func ProbeQuery(ctx context.Context, endpoint string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp", endpoint)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	request := append([]byte{255, 255, 255, 255, 0x54}, []byte("Source Engine Query\x00")...)
	buf := make([]byte, 4096)
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := conn.Write(request); err != nil {
			return err
		}
		n, err := conn.Read(buf)
		if err != nil {
			return err
		}
		packet := buf[:n]
		if len(packet) < 5 || !bytes.Equal(packet[:4], []byte{255, 255, 255, 255}) {
			return fmt.Errorf("invalid or unsupported split A2S response")
		}
		if packet[4] == 0x41 && len(packet) == 9 && attempt == 0 {
			request = append(request, packet[5:9]...)
			continue
		}
		return validateInfo(packet)
	}
	return fmt.Errorf("A2S challenge not completed")
}

func validateInfo(packet []byte) error {
	if len(packet) < 6 || packet[4] != 0x49 {
		return fmt.Errorf("expected A2S_INFO response")
	}
	rest := packet[6:]       // skip protocol version
	for i := 0; i < 4; i++ { // server name, map, folder, game
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return fmt.Errorf("truncated A2S_INFO strings")
		}
		rest = rest[end+1:]
	}
	if len(rest) < 9 {
		return fmt.Errorf("truncated A2S_INFO fields")
	}
	if bytes.IndexByte(rest[9:], 0) < 0 {
		return fmt.Errorf("missing A2S_INFO version")
	}
	return nil
}
