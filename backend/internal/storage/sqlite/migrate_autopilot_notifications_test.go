package sqlite

import "testing"

// The Autopilot migration rebuilds notifications to admit autopilot_parked. It
// must keep every earlier kind: a usage_limit notification written before the
// upgrade has to survive it, and survive a rollback too.
func TestAutopilotMigrationKeepsUsageLimitNotifications(t *testing.T) {
	const before, autopilot = int64(20261117093000000), int64(20261124120000000)
	db := openMigratedDatabaseCopy(t, before)
	const at = "2026-11-22T08:00:00Z"
	mustExec(t, db, `
INSERT INTO sessions (id, num, kind, activity_last_at, created_at, updated_at)
VALUES ('standalone-1', 1, 'worker', ?, ?, ?);
INSERT INTO notifications (id, session_id, project_id, pr_url, type, title, body, status, created_at, source_key)
VALUES ('limit-1', 'standalone-1', NULL, '', 'usage_limit', 'Limit', 'Hit the 5-hour limit', 'unread', ?, 'limit|acct|5h');`,
		at, at, at, at)

	countOf := func(typ string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM notifications WHERE type = ?`, typ).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	upTo(t, db, autopilot)
	if countOf("usage_limit") != 1 {
		t.Fatal("the usage_limit notification was lost by the upgrade")
	}
	// Both kinds are accepted after the upgrade.
	mustExec(t, db, `INSERT INTO notifications (id, session_id, type, title, status, created_at, source_key)
VALUES ('parked-1', 'standalone-1', 'autopilot_parked', 'Parked', 'unread', ?, 'parked|1');
INSERT INTO notifications (id, session_id, type, title, status, created_at, source_key)
VALUES ('limit-2', 'standalone-1', 'usage_limit', 'Limit', 'unread', ?, 'limit|acct|weekly');`, at, at)

	downTo(t, db, before)
	if countOf("usage_limit") != 2 {
		t.Fatalf("usage_limit notifications after rollback = %d, want 2", countOf("usage_limit"))
	}
	if countOf("autopilot_parked") != 0 {
		t.Fatal("a parked-item notification survived the rollback")
	}
	if _, err := db.Exec(`INSERT INTO notifications (id, session_id, type, title, status, created_at, source_key)
VALUES ('limit-3', 'standalone-1', 'usage_limit', 'Limit', 'unread', ?, 'limit|acct|other')`, at); err != nil {
		t.Fatalf("the rolled-back schema must still accept usage_limit: %v", err)
	}

	upTo(t, db, autopilot)
	if countOf("usage_limit") != 3 {
		t.Fatalf("usage_limit notifications after re-upgrade = %d, want 3", countOf("usage_limit"))
	}
}
