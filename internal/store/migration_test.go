package store

import (
	"path/filepath"
	"testing"
)

func TestDailyConstraintUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the daily release schema, including its restrictive CHECK.
	_, err = s.DB().Exec(`
 DELETE FROM manager_migrations;
 DROP TABLE schedules;
 CREATE TABLE schedules (
 server_id TEXT PRIMARY KEY,
 action TEXT NOT NULL,
 enabled INTEGER NOT NULL,
 mode TEXT NOT NULL CHECK(mode IN ('interval','daily')),
 interval_seconds INTEGER NOT NULL,
 daily_time TEXT,
 next_run TEXT NOT NULL,
 updated_at TEXT NOT NULL);
 INSERT INTO servers(id,name,engine,runtime,install_dir,steamcmd_dir,game_port,query_port,ramdisk_path,created_at,updated_at)
 VALUES('one','One','ue5','native','/game','/steam',7777,27015,'','2026-09-20','2026-09-20');
 INSERT INTO schedules VALUES('one','stop',1,'daily',86400,'03:00','2026-09-21T03:00:00Z','2026-09-20T03:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var daily, next string
		if err := s.DB().QueryRow(`SELECT daily_time,next_run FROM schedules WHERE server_id='one'`).Scan(&daily, &next); err != nil {
			t.Fatal(err)
		}
		if daily != "03:00" || next != "2026-09-21T03:00:00Z" {
			t.Fatalf("lost schedule: %s %s", daily, next)
		}
		if _, err := s.DB().Exec(`UPDATE schedules SET mode='cron',cron_expression='0 3 * * *' WHERE server_id='one'`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().Exec(`UPDATE schedules SET mode='invalid' WHERE server_id='one'`); err == nil {
			t.Fatal("lost mode constraint")
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScheduleMigrationRollback(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.DB().Exec(`DELETE FROM manager_migrations;
DROP TABLE schedules;
CREATE TABLE schedules(server_id TEXT,action TEXT,enabled INTEGER,mode TEXT,interval_seconds INTEGER,daily_time TEXT,cron_expression TEXT,timezone TEXT,next_run TEXT,updated_at TEXT);
INSERT INTO schedules VALUES('orphan','stop',1,'invalid',60,NULL,NULL,'UTC','next','updated');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateScheduleModes(s.DB()); err == nil {
		t.Fatal("invalid data migration succeeded")
	}
	var mode string
	if err := s.DB().QueryRow(`SELECT mode FROM schedules WHERE server_id='orphan'`).Scan(&mode); err != nil || mode != "invalid" {
		t.Fatalf("original data lost: %s %v", mode, err)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM manager_migrations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration marked complete: %d %v", count, err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='schedules_cron_upgrade'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("temporary table retained: %d %v", count, err)
	}
}
