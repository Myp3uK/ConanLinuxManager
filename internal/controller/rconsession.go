package controller

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/example/conan-manager/internal/rcon"
	"github.com/example/conan-manager/internal/server"
)

// The manager keeps one RCON connection per running server instead of
// logging in for every command: Conan's login is the step that fails at
// random, and the console, the player list and the warnings all share it.
// listplayers once a minute keeps the connection used and the player list
// fresh.
const (
	rconKeepAlive = time.Minute
	// playersFresh is how old the cached player list may be before a request
	// asks the game again.
	playersFresh = 90 * time.Second
	// rconAuthBackoff pauses automatic logins after a password was refused on
	// every attempt, so a wrong password does not run into RconMaxKarma.
	rconAuthBackoff = 10 * time.Minute
)

type rconSession struct {
	mu        sync.Mutex
	client    *rcon.Client
	key       string
	pid       int
	players   []rcon.Player
	playersAt time.Time
	lastError string
	authFail  time.Time
	usedAt    time.Time
}

// RconState is what the interface shows about a server's RCON connection.
type RconState struct {
	Connected bool          `json:"connected"`
	Players   []rcon.Player `json:"players"`
	PlayersAt time.Time     `json:"players_at,omitempty"`
	Error     string        `json:"error,omitempty"`
}

func (c *Controller) rconSession(id string) *rconSession {
	c.rconMu.Lock()
	defer c.rconMu.Unlock()
	if c.rconSessions == nil {
		c.rconSessions = map[string]*rconSession{}
	}
	s := c.rconSessions[id]
	if s == nil {
		s = &rconSession{}
		c.rconSessions[id] = s
	}
	return s
}

// dropRcon closes a server's connection, when it stops or is deleted.
func (c *Controller) dropRcon(id string) {
	c.rconMu.Lock()
	s := c.rconSessions[id]
	delete(c.rconSessions, id)
	c.rconMu.Unlock()
	if s != nil {
		s.mu.Lock()
		s.close()
		s.mu.Unlock()
	}
}

func (s *rconSession) close() {
	if s.client != nil {
		_ = s.client.Close()
		s.client = nil
	}
}

// exec runs a command on the session, logging in when there is no
// connection. automatic marks the keep-alive, which respects the backoff
// after a refused password.
func (s *rconSession) exec(ctx context.Context, settings rcon.Settings, pid int, command string, automatic bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := settings.Address() + "\x00" + settings.Password
	if s.client != nil && (s.key != key || s.pid != pid) {
		// The password, the port or the process changed: the old login is
		// for something that is gone.
		s.close()
	}
	reused := s.client != nil
	if !reused {
		if automatic && !s.authFail.IsZero() && time.Since(s.authFail) < rconAuthBackoff {
			return "", errors.New(s.lastError)
		}
		if err := s.dial(ctx, settings, key, pid); err != nil {
			return "", err
		}
	}
	out, err := s.client.Exec(ctx, command)
	if err != nil && reused && ctx.Err() == nil {
		// A connection that sat idle may have been dropped by the game; the
		// command most likely never ran, so it is sent once more on a new one.
		s.close()
		if err = s.dial(ctx, settings, key, pid); err == nil {
			out, err = s.client.Exec(ctx, command)
		}
	}
	if err != nil {
		s.close()
		s.lastError = err.Error()
		return "", err
	}
	s.lastError, s.usedAt = "", time.Now()
	return out, nil
}

func (s *rconSession) dial(ctx context.Context, settings rcon.Settings, key string, pid int) error {
	client, err := rcon.DialRetry(ctx, settings.Address(), settings.Password, rcon.DefaultAttempts, rcon.DefaultRetryDelay)
	if err != nil {
		s.lastError = err.Error()
		if errors.Is(err, rcon.ErrAuth) {
			s.authFail = time.Now()
		}
		return err
	}
	s.client, s.key, s.pid, s.authFail = client, key, pid, time.Time{}
	return nil
}

func (s *rconSession) setPlayers(players []rcon.Player) {
	s.mu.Lock()
	s.players, s.playersAt = players, time.Now()
	s.mu.Unlock()
}

func (s *rconSession) cached() ([]rcon.Player, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.players, s.playersAt
}

// RconState reports the connection and the last known players.
func (c *Controller) RconState(id string) RconState {
	c.rconMu.Lock()
	s := c.rconSessions[id]
	c.rconMu.Unlock()
	if s == nil {
		return RconState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return RconState{Connected: s.client != nil, Players: s.players, PlayersAt: s.playersAt, Error: s.lastError}
}

// WatchRcon keeps each running server's connection alive with listplayers,
// which also refreshes the player list, and closes the connections of servers
// that are no longer running.
func (c *Controller) WatchRcon(ctx context.Context) {
	ticker := time.NewTicker(rconKeepAlive)
	defer ticker.Stop()
	for {
		c.keepRconAlive(ctx)
		select {
		case <-ctx.Done():
			c.rconMu.Lock()
			ids := make([]string, 0, len(c.rconSessions))
			for id := range c.rconSessions {
				ids = append(ids, id)
			}
			c.rconMu.Unlock()
			for _, id := range ids {
				c.dropRcon(id)
			}
			return
		case <-ticker.C:
		}
	}
}

func (c *Controller) keepRconAlive(ctx context.Context) {
	configs, err := c.Repo.List(ctx)
	if err != nil {
		return
	}
	for _, cfg := range configs {
		if c.Lifecycle(cfg.ID).State != "running" {
			c.dropRcon(cfg.ID)
			continue
		}
		// A server still loading does not listen for RCON yet.
		if !c.answeredQuery(cfg.ID) {
			continue
		}
		if _, err := c.refreshPlayers(ctx, cfg, true); err != nil {
			c.Logger.Debug("rcon keep-alive failed", "server", cfg.ID, "error", err)
		}
	}
}

func (c *Controller) refreshPlayers(ctx context.Context, cfg server.Config, automatic bool) ([]rcon.Player, error) {
	out, err := c.rconExec(ctx, cfg, "listplayers", automatic)
	if err != nil {
		return nil, err
	}
	players := rcon.ParsePlayers(out)
	c.rconSession(cfg.ID).setPlayers(players)
	return players, nil
}

// answeredQuery reports whether the server has answered a Steam query since
// it started, that is whether it has finished loading.
func (c *Controller) answeredQuery(id string) bool {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return !c.health[id].Availability.LastSuccess.IsZero()
}
