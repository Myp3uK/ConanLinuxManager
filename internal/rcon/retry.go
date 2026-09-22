package rcon

import (
	"context"
	"time"
)

// Conan now and then refuses the right password or drops a fresh connection.
// MooRCON, written against the same servers, connects up to three times a
// second apart, a refused password included; so does DialRetry. Three tries
// stay far below RconMaxKarma, so a wrong password is not banned for it.
const (
	DefaultAttempts   = 3
	DefaultRetryDelay = time.Second
)

// DialRetry is Dial repeated up to attempts times.
func DialRetry(ctx context.Context, address, password string, attempts int, delay time.Duration) (*Client, error) {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, err
			case <-timer.C:
			}
		}
		var c *Client
		if c, err = Dial(ctx, address, password); err == nil {
			return c, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, err
}
