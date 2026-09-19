package controller

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Shutdown seals admission and waits for the monitors to persist every database.
// Process Stop sends a signal without waiting, so servers stop concurrently.
func (c *Controller) Shutdown(ctx context.Context) error {
	c.lifecycleMu.Lock()
	c.closing = true
	for id, h := range c.health {
		h.RestartPending = false
		c.health[id] = h
	}
	c.lifecycleMu.Unlock()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		pending := false
		var failures []error
		c.lifecycleMu.Lock()
		for id, held := range c.active {
			switch held.State {
			case "recovery_required":
				failures = append(failures, fmt.Errorf("server %s requires recovery: %s", id, held.Error))
			case "running":
				pending = true
				if err := c.stopProcess(ctx, id); err != nil {
					failures = append(failures, fmt.Errorf("stop %s: %w", id, err))
				} else {
					held.State = "stopping"
				}
			default:
				pending = true
			}
		}
		c.lifecycleMu.Unlock()
		if !pending {
			return errors.Join(failures...)
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), errors.Join(failures...))
		case <-ticker.C:
		}
	}
}
