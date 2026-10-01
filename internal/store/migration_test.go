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
	// A row that cannot satisfy the new CHECK must abort the rebuild.
	_, err = s.DB().Exec(`DELETE FROM manager_migrations;
DROP TABLE schedules;
CREATE TABLE schedules(server_id TEXT,action TEXT,enabled INTEGER,mode TEXT,interval_seconds INTEGER,daily_time TEXT,cron_expression TEXT,timezone TEXT,failure_count INTEGER,next_run TEXT,updated_at TEXT);
INSERT INTO schedules VALUES('orphan','stop',1,'invalid',60,NULL,NULL,'UTC',0,'next','updated');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyStep(s.DB(), steps[1]); err == nil {
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
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='schedules_v2'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("temporary table retained: %d %v", count, err)
	}
}

// The production database on the acceptance host predates the scheduler: it has
// servers and backup tables but no schedules at all.
func TestUpgradeFromPreSchedulerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DELETE FROM manager_migrations;
DROP TABLE schedules;
DROP TABLE scheduler_runs;
INSERT INTO servers(id,name,engine,runtime,install_dir,steamcmd_dir,game_port,query_port,ramdisk_path,created_at,updated_at)
VALUES('ue5-test','Conan UE5 Test','ue5','native','/game','/steam',37777,37015,'/run/x','2026-09-19','2026-09-19');`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen after upgrade: %v", err)
	}
	defer s.Close()
	var id string
	if err := s.DB().QueryRow(`SELECT id FROM servers`).Scan(&id); err != nil || id != "ue5-test" {
		t.Fatalf("server row lost: %s %v", id, err)
	}
	// A cron schedule must be storable without inventing an interval.
	if _, err := s.DB().Exec(`INSERT INTO schedules(server_id,action,enabled,mode,cron_expression,timezone,next_run,updated_at)
VALUES('ue5-test','restart',1,'cron','0 5 * * *','Europe/Moscow','2026-09-21T05:00:00Z','2026-09-20T00:00:00Z')`); err != nil {
		t.Fatalf("cron schedule without interval rejected: %v", err)
	}
	if _, err := s.DB().Exec(`INSERT INTO scheduler_runs(server_id,action,status,started_at) VALUES('ue5-test','restart','interrupted','2026-09-20T00:00:00Z')`); err != nil {
		t.Fatalf("interrupted status rejected: %v", err)
	}
}

func TestIntervalScheduleStillRequiresInterval(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(`INSERT INTO servers(id,name,engine,runtime,install_dir,steamcmd_dir,game_port,query_port,ramdisk_path,created_at,updated_at)
VALUES('one','One','ue5','native','/game','/steam',7777,27015,'','2026-09-20','2026-09-20')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO schedules(server_id,action,enabled,mode,interval_seconds,next_run,updated_at)
VALUES('one','restart',1,'interval',30,'2026-09-21T00:00:00Z','2026-09-20T00:00:00Z')`); err == nil {
		t.Fatal("interval below the minimum was accepted")
	}
}

func TestMigrationsAreRecordedOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.db")
	for i := 0; i < 3; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var count int
		if err := s.DB().QueryRow(`SELECT COUNT(*) FROM manager_migrations`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != len(steps) {
			t.Fatalf("open %d recorded %d migrations, want %d", i, count, len(steps))
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// A deployed manager has servers whose rows predate the backup-cadence column.
// They must keep running with the game's own cadence rather than silently
// getting one the operator never chose.
func TestUpgradeAddsPeriodicBackupIntervalWithoutChangingBehaviour(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DELETE FROM manager_migrations;
ALTER TABLE servers DROP COLUMN periodic_backup_interval_seconds;
INSERT INTO servers(id,name,engine,runtime,install_dir,steamcmd_dir,game_port,query_port,ramdisk_path,created_at,updated_at)
VALUES('legacy','Legacy','ue5','native','/game','/steam',37777,37015,'/run/x','2026-09-19','2026-09-19');`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen after upgrade: %v", err)
	}
	defer s.Close()
	var seconds int
	if err := s.DB().QueryRow(`SELECT periodic_backup_interval_seconds FROM servers WHERE id='legacy'`).Scan(&seconds); err != nil {
		t.Fatalf("column missing after upgrade: %v", err)
	}
	if seconds != 0 {
		t.Fatalf("existing server gained a cadence it never had: %d", seconds)
	}
	// Running the step twice must not fail on the already-present column.
	if err := migrate(s.DB()); err != nil {
		t.Fatalf("re-running migrations: %v", err)
	}
}

// Channels stored while Discord was the only kind survive the rebuild that
// lets Telegram and HTTP channels in.
func TestChannelKindsUpgradeKeepsChannels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB().Exec(`DELETE FROM manager_migrations;
DROP TABLE notification_channels;
CREATE TABLE notification_channels (id TEXT PRIMARY KEY, name TEXT NOT NULL, kind TEXT NOT NULL CHECK (kind IN ('discord')),
 secret TEXT NOT NULL, server_id TEXT, min_severity TEXT NOT NULL DEFAULT 'info', event_types TEXT NOT NULL DEFAULT '[]',
 language TEXT NOT NULL DEFAULT 'ru', mention TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1,
 last_sent_at TEXT, last_error TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
INSERT INTO notification_channels(id,name,kind,secret,created_at,updated_at) VALUES('c1','Admins','discord','sealed','2026-09-20','2026-09-20');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var name, settings string
	if err := s.DB().QueryRow(`SELECT name, settings FROM notification_channels WHERE id='c1'`).Scan(&name, &settings); err != nil || name != "Admins" || settings != "{}" {
		t.Fatalf("channel lost: %q %q %v", name, settings, err)
	}
	if _, err := s.DB().Exec(`INSERT INTO notification_channels(id,name,kind,secret,created_at,updated_at) VALUES('c2','TG','telegram','sealed','x','x')`); err != nil {
		t.Fatalf("telegram refused: %v", err)
	}
}

// A channel that watched one server keeps watching it once channels can watch
// a set, and a channel that watched all of them keeps watching all.
func TestChannelServerUpgradeKeepsWhoWatchesWhat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB().Exec(`DELETE FROM manager_migrations;
DROP TABLE IF EXISTS notification_channel_servers;
DROP TABLE notification_channels;
CREATE TABLE notification_channels (id TEXT PRIMARY KEY, name TEXT NOT NULL,
 kind TEXT NOT NULL CHECK (kind IN ('discord', 'telegram', 'http')), secret TEXT NOT NULL,
 settings TEXT NOT NULL DEFAULT '{}', server_id TEXT REFERENCES servers(id) ON DELETE CASCADE,
 min_severity TEXT NOT NULL DEFAULT 'info', event_types TEXT NOT NULL DEFAULT '[]',
 language TEXT NOT NULL DEFAULT 'ru', mention TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1,
 last_sent_at TEXT, last_error TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
INSERT INTO servers(id,name,engine,runtime,install_dir,steamcmd_dir,ramdisk_path,game_port,query_port,created_at,updated_at)
 VALUES('alpha','Alpha','ue5','native','/srv/a','/srv/s','',7777,27015,'2026-09-20','2026-09-20');
INSERT INTO notification_channels(id,name,kind,secret,server_id,created_at,updated_at)
 VALUES('one','Alpha only','discord','sealed','alpha','2026-09-20','2026-09-20');
INSERT INTO notification_channels(id,name,kind,secret,server_id,created_at,updated_at)
 VALUES('all','Everything','discord','sealed',NULL,'2026-09-20','2026-09-20');
INSERT INTO notification_channels(id,name,kind,secret,server_id,created_at,updated_at)
 VALUES('two','Alpha again','discord','sealed','alpha','2026-09-20','2026-09-20');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	watched := func(channel string) []string {
		rows, err := s.DB().Query(`SELECT server_id FROM notification_channel_servers WHERE channel_id=? ORDER BY server_id`, channel)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		return ids
	}
	// Dropping the old table cascades into the new one unless the pairs are
	// parked first, so this is the assertion that catches that.
	if got := watched("one"); len(got) != 1 || got[0] != "alpha" {
		t.Errorf("the one-server channel lost its server: %v", got)
	}
	if got := watched("all"); len(got) != 0 {
		t.Errorf("the all-servers channel should name none: %v", got)
	}
	// Two channels on the same server each keep their own row.
	if got := watched("two"); len(got) != 1 || got[0] != "alpha" {
		t.Errorf("the second channel lost its server: %v", got)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM notification_channels`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("channels lost: %d, %v", count, err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM pragma_table_info('notification_channels') WHERE name='server_id'`).Scan(&count); err != nil || count != 0 {
		t.Errorf("the old column is still there: %d, %v", count, err)
	}
}
