package panel

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Myp3uK/ConanLinuxManager/internal/store"
)

func TestSessionsExpire(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	admins := NewAdmins(db.DB(), "admin", "env-password")
	sessions := NewSessions(db.DB(), admins)

	token, s, err := sessions.Create(ctx, "admin", "10.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := sessions.Lookup(ctx, token); !ok || got.ID != s.ID || got.CSRF != s.CSRF {
		t.Fatal("fresh session not found")
	}
	if _, ok := sessions.Lookup(ctx, token+"x"); ok {
		t.Fatal("wrong token accepted")
	}
	// The database keeps only the hash of the token.
	var n int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM sessions WHERE id=? OR csrf=?`, token, token).Scan(&n); err != nil || n != 0 {
		t.Fatal("token stored in clear", err)
	}

	age := func(column string, by time.Duration) {
		t.Helper()
		at := time.Now().UTC().Add(-by).Format(time.RFC3339Nano)
		if _, err := db.DB().Exec(`UPDATE sessions SET `+column+`=?`, at); err != nil {
			t.Fatal(err)
		}
		sessions.Forget()
	}
	age("last_seen", 11*time.Hour)
	if _, ok := sessions.Lookup(ctx, token); !ok {
		t.Fatal("ended before the idle limit")
	}
	age("last_seen", 13*time.Hour)
	if _, ok := sessions.Lookup(ctx, token); ok {
		t.Fatal("idle session still valid")
	}

	token, _, _ = sessions.Create(ctx, "admin", "", "")
	if _, err := db.DB().Exec(`UPDATE sessions SET expires_at=?`, time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	sessions.Forget()
	if _, ok := sessions.Lookup(ctx, token); ok {
		t.Fatal("expired session still valid")
	}

	// A new environment password ends the sessions made with the old one.
	token, _, _ = sessions.Create(ctx, "admin", "", "")
	sessions.admins = NewAdmins(db.DB(), "admin", "changed-password")
	sessions.Forget()
	if _, ok := sessions.Lookup(ctx, token); ok {
		t.Fatal("session outlived the password")
	}
	list, err := sessions.List(ctx)
	if err != nil || len(list) != 0 {
		t.Fatal("ended sessions listed", list, err)
	}
}
