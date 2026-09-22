package panel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// A session is a browser login. The cookie carries a random token; the
// database keeps only its hash, so a copy of the database logs nobody in.
type Session struct {
	// ID is a short public name of the session, safe to show and to end it by.
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
	ExpiresAt time.Time `json:"expires_at"`
	// CSRF must come back in a header with every change the browser makes.
	CSRF string `json:"-"`
	mark string
	key  string
}

var ErrSessionNotFound = errors.New("session not found")

// Sessions keeps browser logins in SQLite, so they survive a manager restart.
type Sessions struct {
	db     *sql.DB
	admins *Admins
	// IdleFor ends a session nobody used for that long; MaxAge ends every
	// session, used or not.
	IdleFor time.Duration
	MaxAge  time.Duration

	mu    sync.Mutex
	cache map[string]cachedSession
}

type cachedSession struct {
	session Session
	checked time.Time
}

// recheckAfter is how long a looked-up session is trusted before the
// database and the administrator's password are consulted again. The
// interface polls every few seconds.
const recheckAfter = 30 * time.Second

func NewSessions(db *sql.DB, admins *Admins) *Sessions {
	return &Sessions{db: db, admins: admins, IdleFor: 12 * time.Hour, MaxAge: 7 * 24 * time.Hour,
		cache: map[string]cachedSession{}}
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Create starts a session for an administrator whose password was checked.
func (s *Sessions) Create(ctx context.Context, username, ip, userAgent string) (string, Session, error) {
	mark, ok := s.admins.Mark(ctx, username)
	if !ok {
		return "", Session{}, ErrAdminNotFound
	}
	token, err := randomToken()
	if err != nil {
		return "", Session{}, err
	}
	csrf, err := randomToken()
	if err != nil {
		return "", Session{}, err
	}
	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	now := time.Now().UTC()
	key := tokenKey(token)
	sess := Session{ID: key[:16], Username: username, IP: ip, UserAgent: userAgent, CreatedAt: now, LastSeen: now,
		ExpiresAt: now.Add(s.MaxAge), CSRF: csrf, mark: mark, key: key}
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions (id, username, csrf, mark, ip, user_agent, created_at, last_seen, expires_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, key, username, csrf, mark, ip, userAgent,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), sess.ExpiresAt.Format(time.RFC3339Nano))
	if err != nil {
		return "", Session{}, err
	}
	s.prune(ctx)
	return token, sess, nil
}

// Lookup returns the live session for a cookie token. A session ends when it
// expires, sits idle too long, or its administrator was removed or given a
// new password.
func (s *Sessions) Lookup(ctx context.Context, token string) (Session, bool) {
	if token == "" || len(token) > 100 {
		return Session{}, false
	}
	key := tokenKey(token)
	now := time.Now().UTC()
	s.mu.Lock()
	c, ok := s.cache[key]
	s.mu.Unlock()
	if ok && now.Sub(c.checked) < recheckAfter && now.Before(c.session.ExpiresAt) {
		return c.session, true
	}
	var sess Session
	var created, seen, expires string
	err := s.db.QueryRowContext(ctx, `SELECT username, csrf, mark, ip, user_agent, created_at, last_seen, expires_at
		FROM sessions WHERE id=?`, key).Scan(&sess.Username, &sess.CSRF, &sess.mark, &sess.IP, &sess.UserAgent, &created, &seen, &expires)
	if err != nil {
		s.forgetKey(key)
		return Session{}, false
	}
	sess.ID, sess.key = key[:16], key
	sess.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	sess.LastSeen, _ = time.Parse(time.RFC3339Nano, seen)
	sess.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	mark, exists := s.admins.Mark(ctx, sess.Username)
	if !exists || subtle.ConstantTimeCompare([]byte(mark), []byte(sess.mark)) != 1 ||
		!now.Before(sess.ExpiresAt) || (s.IdleFor > 0 && now.Sub(sess.LastSeen) > s.IdleFor) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, key)
		s.forgetKey(key)
		return Session{}, false
	}
	// last_seen is written at most once per recheck, not on every poll.
	sess.LastSeen = now
	_, _ = s.db.ExecContext(ctx, `UPDATE sessions SET last_seen=? WHERE id=?`, now.Format(time.RFC3339Nano), key)
	s.mu.Lock()
	s.cache[key] = cachedSession{session: sess, checked: now}
	s.mu.Unlock()
	return sess, true
}

func (s *Sessions) forgetKey(key string) {
	s.mu.Lock()
	delete(s.cache, key)
	s.mu.Unlock()
}

// Forget drops every cached session, after the administrators changed.
func (s *Sessions) Forget() {
	s.mu.Lock()
	s.cache = map[string]cachedSession{}
	s.mu.Unlock()
}

// End ends the session with the given public ID.
func (s *Sessions) End(ctx context.Context, id string) error {
	if len(id) != 16 {
		return ErrSessionNotFound
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE substr(id, 1, 16)=?`, id)
	if err != nil {
		return err
	}
	s.Forget()
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// EndOthers ends every session of the user except the one with keepID.
func (s *Sessions) EndOthers(ctx context.Context, username, keepID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE username=? AND substr(id, 1, 16)<>?`, username, keepID)
	if err != nil {
		return 0, err
	}
	s.Forget()
	n, _ := res.RowsAffected()
	return n, nil
}

// List returns the live sessions, the most recently used first.
func (s *Sessions) List(ctx context.Context) ([]Session, error) {
	s.prune(ctx)
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, ip, user_agent, created_at, last_seen, expires_at
		FROM sessions ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Session
	for rows.Next() {
		var sess Session
		var created, seen, expires string
		if err := rows.Scan(&sess.key, &sess.Username, &sess.IP, &sess.UserAgent, &created, &seen, &expires); err != nil {
			return nil, err
		}
		sess.ID = sess.key[:16]
		sess.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		sess.LastSeen, _ = time.Parse(time.RFC3339Nano, seen)
		sess.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
		list = append(list, sess)
	}
	return list, rows.Err()
}

// prune removes expired and idle sessions.
func (s *Sessions) prune(ctx context.Context) {
	now := time.Now().UTC()
	idle := now.Add(-s.IdleFor)
	if s.IdleFor <= 0 {
		idle = time.Time{}
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ? OR last_seen < ?`,
		now.Format(time.RFC3339Nano), idle.Format(time.RFC3339Nano))
}
