package panel

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	hashIterations = 210000
	// verifiedFor is how long a checked login is trusted without hashing it
	// again. The interface polls every few seconds; hashing each request would
	// cost a core.
	verifiedFor = 10 * time.Minute
	// MinPasswordLength keeps panel passwords out of reach of guessing.
	MinPasswordLength = 10
)

var (
	ErrAdminExists   = errors.New("administrator already exists")
	ErrAdminNotFound = errors.New("administrator not found")
	ErrLastAdmin     = errors.New("the last administrator cannot be removed")
	usernamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{1,31}$`)
)

// Admin is an administrator kept in the database; the one from the
// environment file is Builtin and is not stored.
type Admin struct {
	Username          string    `json:"username"`
	Builtin           bool      `json:"builtin"`
	CreatedAt         time.Time `json:"created_at"`
	PasswordChangedAt time.Time `json:"password_changed_at"`
}

// Admins checks logins against the environment administrator and the stored
// ones.
type Admins struct {
	db          *sql.DB
	envUser     string
	envPassword string

	mu       sync.Mutex
	verified map[[32]byte]verified
	// hashing bounds concurrent password hashing, so a flood of wrong
	// passwords cannot occupy every core.
	hashing chan struct{}
}

type verified struct {
	username string
	until    time.Time
}

func NewAdmins(db *sql.DB, envUser, envPassword string) *Admins {
	return &Admins{db: db, envUser: envUser, envPassword: envPassword,
		verified: map[[32]byte]verified{}, hashing: make(chan struct{}, 2)}
}

// Configured reports whether anyone can log in at all.
func (a *Admins) Configured(ctx context.Context) bool {
	if a.envUser != "" && a.envPassword != "" {
		return true
	}
	var n int
	return a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&n) == nil && n > 0
}

// Check returns whether the login is valid.
func (a *Admins) Check(ctx context.Context, username, password string) bool {
	if a.envUser != "" && a.envPassword != "" &&
		subtle.ConstantTimeCompare([]byte(username), []byte(a.envUser)) == 1 &&
		subtle.ConstantTimeCompare([]byte(password), []byte(a.envPassword)) == 1 {
		return true
	}
	if username == "" || password == "" || username == a.envUser {
		return false
	}
	key := sha256.Sum256([]byte(username + "\x00" + password))
	a.mu.Lock()
	v, ok := a.verified[key]
	a.mu.Unlock()
	if ok && v.username == username && time.Now().Before(v.until) {
		return true
	}
	var stored string
	if err := a.db.QueryRowContext(ctx, `SELECT password_hash FROM admins WHERE username=?`, username).Scan(&stored); err != nil {
		return false
	}
	select {
	case a.hashing <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	good := verifyPassword(stored, password)
	<-a.hashing
	if good {
		a.mu.Lock()
		a.verified[key] = verified{username: username, until: time.Now().Add(verifiedFor)}
		a.mu.Unlock()
	}
	return good
}

// Mark fingerprints an administrator's current password. A session keeps
// the mark it was created with, so a new password or a removed administrator
// ends the sessions at once.
func (a *Admins) Mark(ctx context.Context, username string) (string, bool) {
	if username == "" {
		return "", false
	}
	if a.envUser != "" && a.envPassword != "" && username == a.envUser {
		sum := sha256.Sum256([]byte("env|" + a.envUser + "|" + a.envPassword))
		return base64.RawStdEncoding.EncodeToString(sum[:12]), true
	}
	var stored string
	if err := a.db.QueryRowContext(ctx, `SELECT password_hash FROM admins WHERE username=?`, username).Scan(&stored); err != nil {
		return "", false
	}
	sum := sha256.Sum256([]byte(stored))
	return base64.RawStdEncoding.EncodeToString(sum[:12]), true
}

// forget drops every trusted login, after any change to the administrators.
func (a *Admins) forget() {
	a.mu.Lock()
	a.verified = map[[32]byte]verified{}
	a.mu.Unlock()
}

// List returns the environment administrator first, then the stored ones.
func (a *Admins) List(ctx context.Context) ([]Admin, error) {
	var list []Admin
	if a.envUser != "" && a.envPassword != "" {
		list = append(list, Admin{Username: a.envUser, Builtin: true})
	}
	rows, err := a.db.QueryContext(ctx, `SELECT username, created_at, password_changed_at FROM admins ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ad Admin
		var created, changed string
		if err := rows.Scan(&ad.Username, &created, &changed); err != nil {
			return nil, err
		}
		ad.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		ad.PasswordChangedAt, _ = time.Parse(time.RFC3339Nano, changed)
		list = append(list, ad)
	}
	return list, rows.Err()
}

// Add stores a new administrator.
func (a *Admins) Add(ctx context.Context, username, password string) error {
	if !usernamePattern.MatchString(username) {
		return errors.New("username must be 2-32 latin letters, digits, dot, dash or underscore")
	}
	if strings.EqualFold(username, a.envUser) {
		return ErrAdminExists
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := a.db.ExecContext(ctx, `INSERT INTO admins (username, password_hash, created_at, password_changed_at)
		SELECT ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM admins WHERE username = ? COLLATE NOCASE)`, username, hash, now, now, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAdminExists
	}
	return nil
}

// SetPassword changes a stored administrator's password.
func (a *Admins) SetPassword(ctx context.Context, username, password string) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	res, err := a.db.ExecContext(ctx, `UPDATE admins SET password_hash=?, password_changed_at=? WHERE username=?`,
		hash, time.Now().UTC().Format(time.RFC3339Nano), username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAdminNotFound
	}
	a.forget()
	return nil
}

// Remove deletes a stored administrator. Without an environment administrator
// the last stored one stays, or nobody could log in again.
func (a *Admins) Remove(ctx context.Context, username string) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if a.envUser == "" || a.envPassword == "" {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&n); err != nil {
			return err
		}
		if n <= 1 {
			return ErrLastAdmin
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM admins WHERE username=?`, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAdminNotFound
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.forget()
	return nil
}

// ValidatePassword applies the panel's password rules.
func ValidatePassword(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	if len(password) > 256 {
		return errors.New("password is too long")
	}
	return nil
}

func hashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, hashIterations, 32)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", hashIterations, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

func verifyPassword(stored, password string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 1 || iterations > 10_000_000 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}
