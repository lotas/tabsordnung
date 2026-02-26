package storage

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// testDB creates a temporary database for testing.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB(%q): %v", dbPath, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// intPtr returns a pointer to the given int.
func intPtr(i int) *int {
	return &i
}

func TestSignalsTableExists(t *testing.T) {
	db := testDB(t)

	_, err := db.Exec(`INSERT INTO signals (source, title, preview, source_ts, captured_at)
		VALUES ('gmail', 'Alice', 'hello', '2:30 PM', CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatalf("insert into signals: %v", err)
	}

	// Same source+title+preview+source_ts → unique violation
	_, err = db.Exec(`INSERT INTO signals (source, title, preview, source_ts, captured_at)
		VALUES ('gmail', 'Alice', 'hello', '2:30 PM', CURRENT_TIMESTAMP)`)
	if err == nil {
		t.Fatal("expected unique constraint violation")
	}

	// Different preview → allowed (distinct email subject)
	_, err = db.Exec(`INSERT INTO signals (source, title, preview, source_ts, captured_at)
		VALUES ('gmail', 'Alice', 'different preview', '2:30 PM', CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatalf("insert with different preview: %v", err)
	}

	// Different source_ts → allowed (distinct episode)
	_, err = db.Exec(`INSERT INTO signals (source, title, preview, source_ts, captured_at)
		VALUES ('gmail', 'Alice', 'hello', '3:00 PM', CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatalf("insert with different source_ts: %v", err)
	}
}

func TestOpenDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sub", "dir", "tabsordnung.db")

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB failed: %v", err)
	}
	defer db.Close()

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("database file not found: %v", err)
	}

	// Verify tables exist.
	_, err = db.Exec(`INSERT INTO snapshots (rev, profile, tab_count) VALUES (1, 'default', 5)`)
	if err != nil {
		t.Fatalf("insert into snapshots: %v", err)
	}
}

func TestOpenDB_MigratesOldSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "migrate.db")

	// Create a DB with the old schema (name TEXT UNIQUE NOT NULL, no rev),
	// simulating a pre-migration database with real data.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.Exec("PRAGMA foreign_keys = ON")
	_, err = db.Exec(`CREATE TABLE snapshots (
		id INTEGER PRIMARY KEY,
		name TEXT UNIQUE NOT NULL,
		profile TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		tab_count INTEGER NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create old snapshots: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE snapshot_groups (
		id INTEGER PRIMARY KEY,
		snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
		firefox_id TEXT NOT NULL,
		name TEXT NOT NULL,
		color TEXT
	)`)
	if err != nil {
		t.Fatalf("create old groups: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE snapshot_tabs (
		id INTEGER PRIMARY KEY,
		snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
		group_id INTEGER REFERENCES snapshot_groups(id),
		url TEXT NOT NULL,
		title TEXT NOT NULL,
		pinned BOOLEAN DEFAULT FALSE
	)`)
	if err != nil {
		t.Fatalf("create old tabs: %v", err)
	}

	// Insert old data: 2 snapshots, one with a group and tabs.
	db.Exec(`INSERT INTO snapshots (id, name, profile, tab_count) VALUES (1, 'first', 'default', 2)`)
	db.Exec(`INSERT INTO snapshots (id, name, profile, tab_count) VALUES (2, 'second', 'default', 1)`)
	db.Exec(`INSERT INTO snapshot_groups (id, snapshot_id, firefox_id, name, color) VALUES (1, 1, 'g1', 'Work', 'blue')`)
	db.Exec(`INSERT INTO snapshot_tabs (snapshot_id, group_id, url, title, pinned) VALUES (1, 1, 'https://example.com', 'Example', 1)`)
	db.Exec(`INSERT INTO snapshot_tabs (snapshot_id, url, title) VALUES (1, 'https://go.dev', 'Go')`)
	db.Exec(`INSERT INTO snapshot_tabs (snapshot_id, url, title) VALUES (2, 'https://mozilla.org', 'Mozilla')`)
	db.Close()

	// Reopen with OpenDB — should detect old DB, mark migration 1, apply migration 2.
	db2, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB after migration: %v", err)
	}
	defer db2.Close()

	// All migrations should be recorded.
	var count int
	db2.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count)
	if count != len(migrations) {
		t.Errorf("expected %d migrations recorded, got %d", len(migrations), count)
	}

	// Old snapshots should be preserved with assigned rev numbers.
	list, err := ListSnapshots(db2)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 migrated snapshots, got %d", len(list))
	}

	// Verify rev assignment (ordered by id, so first=rev1, second=rev2).
	snap1, err := GetSnapshot(db2, "default", 1)
	if err != nil {
		t.Fatalf("GetSnapshot rev 1: %v", err)
	}
	if snap1.Name != "first" {
		t.Errorf("expected name 'first', got %q", snap1.Name)
	}
	if snap1.TabCount != 2 {
		t.Errorf("expected 2 tabs, got %d", snap1.TabCount)
	}
	if len(snap1.Groups) != 1 {
		t.Errorf("expected 1 group, got %d", len(snap1.Groups))
	}
	if len(snap1.Tabs) != 2 {
		t.Errorf("expected 2 tabs loaded, got %d", len(snap1.Tabs))
	}

	snap2, err := GetSnapshot(db2, "default", 2)
	if err != nil {
		t.Fatalf("GetSnapshot rev 2: %v", err)
	}
	if snap2.Name != "second" {
		t.Errorf("expected name 'second', got %q", snap2.Name)
	}

	// New snapshots should continue from rev 3.
	rev, err := CreateSnapshot(db2, "default", nil, []SnapshotTab{
		{URL: "https://new.com", Title: "New"},
	}, "")
	if err != nil {
		t.Fatalf("CreateSnapshot after migration: %v", err)
	}
	if rev != 3 {
		t.Errorf("expected rev 3 (continuing after migrated data), got %d", rev)
	}
}

func TestOpenDB_FreshDB_AllMigrations(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fresh.db")

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	// All migrations should be recorded.
	var count int
	db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count)
	if count != len(migrations) {
		t.Errorf("expected %d migrations recorded, got %d", len(migrations), count)
	}

	// New schema should work.
	rev, err := CreateSnapshot(db, "default", nil, []SnapshotTab{
		{URL: "https://example.com", Title: "Example"},
	}, "")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if rev != 1 {
		t.Errorf("expected rev 1, got %d", rev)
	}
}

func TestOpenDB_IdempotentMigrations(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "idempotent.db")

	// Open twice — second time should be a no-op.
	db1, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("first OpenDB: %v", err)
	}
	CreateSnapshot(db1, "default", nil, []SnapshotTab{
		{URL: "https://example.com", Title: "Example"},
	}, "")
	db1.Close()

	db2, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("second OpenDB: %v", err)
	}
	defer db2.Close()

	// Data should survive.
	snap, err := GetLatestSnapshot(db2, "default")
	if err != nil {
		t.Fatalf("GetLatestSnapshot: %v", err)
	}
	if snap == nil || snap.Rev != 1 {
		t.Error("expected existing snapshot to survive reopening")
	}
}

func TestDefaultDBPath(t *testing.T) {
	p, err := DefaultDBPath()
	if err != nil {
		t.Fatalf("DefaultDBPath: %v", err)
	}
	if filepath.Base(p) != "tabsordnung.db" {
		t.Errorf("expected filename tabsordnung.db, got %s", filepath.Base(p))
	}
	if !filepath.IsAbs(p) {
		t.Errorf("expected absolute path, got %s", p)
	}
}

func TestCreateAndListSnapshots(t *testing.T) {
	db := testDB(t)

	groups := []SnapshotGroup{
		{FirefoxID: "g1", Name: "Work", Color: "blue"},
	}
	tabs := []SnapshotTab{
		{URL: "https://example.com", Title: "Example", GroupIndex: intPtr(0)},
		{URL: "https://go.dev", Title: "Go", GroupIndex: intPtr(0)},
	}

	// Create first snapshot — should get rev 1.
	rev, err := CreateSnapshot(db, "default", groups, tabs, "")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if rev != 1 {
		t.Errorf("expected rev 1, got %d", rev)
	}

	// Create second snapshot — should get rev 2.
	rev2, err := CreateSnapshot(db, "default", nil, []SnapshotTab{
		{URL: "https://a.com", Title: "A"},
	}, "with label")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if rev2 != 2 {
		t.Errorf("expected rev 2, got %d", rev2)
	}

	// Different profile starts at rev 1.
	rev3, err := CreateSnapshot(db, "work", nil, []SnapshotTab{
		{URL: "https://b.com", Title: "B"},
	}, "")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if rev3 != 1 {
		t.Errorf("expected rev 1 for different profile, got %d", rev3)
	}

	// List should return newest first.
	list, err := ListSnapshots(db)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 snapshots, got %d", len(list))
	}

	// Verify label is stored.
	found := false
	for _, s := range list {
		if s.Rev == 2 && s.Profile == "default" && s.Name == "with label" {
			found = true
		}
	}
	if !found {
		t.Error("expected snapshot with label 'with label'")
	}

	// Verify empty label is empty string (not stored as "").
	for _, s := range list {
		if s.Rev == 1 && s.Profile == "default" && s.Name != "" {
			t.Errorf("expected empty label for rev 1, got %q", s.Name)
		}
	}
}

func TestGetSnapshot(t *testing.T) {
	db := testDB(t)

	groups := []SnapshotGroup{
		{FirefoxID: "g1", Name: "Dev", Color: "green"},
	}
	tabs := []SnapshotTab{
		{URL: "https://example.com", Title: "Example", GroupIndex: intPtr(0), Pinned: true},
		{URL: "https://ungrouped.com", Title: "Ungrouped", GroupIndex: nil},
	}

	rev, err := CreateSnapshot(db, "default", groups, tabs, "my label")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	snap, err := GetSnapshot(db, "default", rev)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}

	if snap.Rev != 1 {
		t.Errorf("expected rev 1, got %d", snap.Rev)
	}
	if snap.Name != "my label" {
		t.Errorf("expected name 'my label', got %q", snap.Name)
	}
	if snap.Profile != "default" {
		t.Errorf("expected profile 'default', got %q", snap.Profile)
	}
	if snap.TabCount != 2 {
		t.Errorf("expected 2 tabs, got %d", snap.TabCount)
	}
	if len(snap.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(snap.Groups))
	}
	if len(snap.Tabs) != 2 {
		t.Fatalf("expected 2 tabs, got %d", len(snap.Tabs))
	}

	// Verify grouped tab.
	var groupedTab, ungroupedTab *SnapshotTab
	for i := range snap.Tabs {
		if snap.Tabs[i].URL == "https://example.com" {
			groupedTab = &snap.Tabs[i]
		}
		if snap.Tabs[i].URL == "https://ungrouped.com" {
			ungroupedTab = &snap.Tabs[i]
		}
	}
	if groupedTab == nil {
		t.Fatal("grouped tab not found")
	}
	if !groupedTab.Pinned {
		t.Error("expected grouped tab to be pinned")
	}
	if groupedTab.GroupName != "Dev" {
		t.Errorf("expected GroupName 'Dev', got %q", groupedTab.GroupName)
	}
	if ungroupedTab == nil {
		t.Fatal("ungrouped tab not found")
	}
	if ungroupedTab.GroupName != "" {
		t.Errorf("expected empty GroupName, got %q", ungroupedTab.GroupName)
	}

	// Non-existent rev should error.
	_, err = GetSnapshot(db, "default", 99)
	if err == nil {
		t.Fatal("expected error for non-existent rev")
	}
}

func TestGetLatestSnapshot(t *testing.T) {
	db := testDB(t)

	// No snapshots — should return nil.
	snap, err := GetLatestSnapshot(db, "default")
	if err != nil {
		t.Fatalf("GetLatestSnapshot: %v", err)
	}
	if snap != nil {
		t.Fatal("expected nil for empty DB")
	}

	// Create two snapshots.
	CreateSnapshot(db, "default", nil, []SnapshotTab{{URL: "https://a.com", Title: "A"}}, "")
	CreateSnapshot(db, "default", nil, []SnapshotTab{{URL: "https://b.com", Title: "B"}}, "")

	snap, err = GetLatestSnapshot(db, "default")
	if err != nil {
		t.Fatalf("GetLatestSnapshot: %v", err)
	}
	if snap.Rev != 2 {
		t.Errorf("expected latest rev 2, got %d", snap.Rev)
	}

	// Different profile should not see default's snapshots.
	snap, err = GetLatestSnapshot(db, "work")
	if err != nil {
		t.Fatalf("GetLatestSnapshot: %v", err)
	}
	if snap != nil {
		t.Fatal("expected nil for profile with no snapshots")
	}
}

func TestDeleteSnapshot(t *testing.T) {
	db := testDB(t)

	groups := []SnapshotGroup{
		{FirefoxID: "g1", Name: "Grp", Color: "blue"},
	}
	tabs := []SnapshotTab{
		{URL: "https://a.com", Title: "A", GroupIndex: intPtr(0)},
	}

	rev, err := CreateSnapshot(db, "default", groups, tabs, "")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	err = DeleteSnapshot(db, "default", rev)
	if err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}

	list, _ := ListSnapshots(db)
	if len(list) != 0 {
		t.Fatalf("expected 0 snapshots after delete, got %d", len(list))
	}

	// Deleting non-existent should error.
	err = DeleteSnapshot(db, "default", rev)
	if err == nil {
		t.Fatal("expected error deleting non-existent snapshot")
	}

	// Verify cascade.
	var groupCount, tabCount int
	db.QueryRow("SELECT COUNT(*) FROM snapshot_groups").Scan(&groupCount)
	db.QueryRow("SELECT COUNT(*) FROM snapshot_tabs").Scan(&tabCount)
	if groupCount != 0 {
		t.Errorf("expected 0 orphan groups, got %d", groupCount)
	}
	if tabCount != 0 {
		t.Errorf("expected 0 orphan tabs, got %d", tabCount)
	}
}

func TestListUnclassifiedSignals(t *testing.T) {
	db := testDB(t)

	// Insert 3 signals: one classified, one unclassified active, one unclassified completed
	db.Exec(`INSERT INTO signals (source, title, preview, source_ts, captured_at, urgency, urgency_source)
		VALUES ('gmail', 'Alice', 'hello', 'ts1', CURRENT_TIMESTAMP, 'urgent', 'llm')`)
	db.Exec(`INSERT INTO signals (source, title, preview, source_ts, captured_at)
		VALUES ('slack', 'Bob', 'unread', 'ts2', CURRENT_TIMESTAMP)`)
	db.Exec(`INSERT INTO signals (source, title, preview, source_ts, captured_at, completed_at)
		VALUES ('gmail', 'Carol', 'bye', 'ts3', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)

	sigs, err := ListUnclassifiedSignals(db)
	if err != nil {
		t.Fatalf("ListUnclassifiedSignals: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("expected 1 unclassified active signal, got %d", len(sigs))
	}
	if sigs[0].Title != "Bob" {
		t.Errorf("expected Bob, got %q", sigs[0].Title)
	}
}

func TestUpdateUrgency(t *testing.T) {
	db := testDB(t)

	db.Exec(`INSERT INTO signals (source, title, preview, source_ts, captured_at)
		VALUES ('gmail', 'Alice', 'hello', 'ts1', CURRENT_TIMESTAMP)`)

	var id int64
	db.QueryRow(`SELECT id FROM signals WHERE title = 'Alice'`).Scan(&id)

	err := UpdateUrgency(db, id, "urgent", "llm")
	if err != nil {
		t.Fatalf("UpdateUrgency: %v", err)
	}

	var urgency, urgencySource sql.NullString
	db.QueryRow(`SELECT urgency, urgency_source FROM signals WHERE id = ?`, id).
		Scan(&urgency, &urgencySource)
	if !urgency.Valid || urgency.String != "urgent" {
		t.Errorf("expected urgency 'urgent', got %v", urgency)
	}
	if !urgencySource.Valid || urgencySource.String != "llm" {
		t.Errorf("expected urgency_source 'llm', got %v", urgencySource)
	}
}

func TestReconcileSignals_HeuristicUrgency(t *testing.T) {
	db := testDB(t)
	now := time.Now()

	items := []SignalRecord{
		{Title: "alice", Preview: "DM · unread", Kind: "dm"},
		{Title: "#general", Preview: "3 mentioned", Kind: "mention"},
		{Title: "#random", Preview: "unread", Kind: "channel"},
		{Title: "Bob", Preview: "Project update", Kind: ""}, // gmail, no kind
	}
	err := ReconcileSignals(db, "slack", items[:3], now)
	if err != nil {
		t.Fatalf("ReconcileSignals slack: %v", err)
	}
	err = ReconcileSignals(db, "gmail", items[3:], now)
	if err != nil {
		t.Fatalf("ReconcileSignals gmail: %v", err)
	}

	sigs, _ := ListSignals(db, "", false)

	urgencies := make(map[string]*string)
	for _, s := range sigs {
		urgencies[s.Title] = s.Urgency
	}

	// DM -> urgent
	if urgencies["alice"] == nil || *urgencies["alice"] != "urgent" {
		t.Errorf("expected alice=urgent, got %v", urgencies["alice"])
	}
	// mention -> review
	if urgencies["#general"] == nil || *urgencies["#general"] != "review" {
		t.Errorf("expected #general=review, got %v", urgencies["#general"])
	}
	// channel -> fyi
	if urgencies["#random"] == nil || *urgencies["#random"] != "fyi" {
		t.Errorf("expected #random=fyi, got %v", urgencies["#random"])
	}
	// gmail with no kind -> nil (unclassified)
	if urgencies["Bob"] != nil {
		t.Errorf("expected Bob=nil (unclassified), got %v", *urgencies["Bob"])
	}
}

func TestGitHubTablesExist(t *testing.T) {
	db := testDB(t)

	// github_entities table should exist after migration
	_, err := db.Exec(`INSERT INTO github_entities (owner, repo, number, kind, first_seen_source)
		VALUES ('mozilla', 'gecko-dev', 123, 'pull', 'tab')`)
	if err != nil {
		t.Fatalf("insert into github_entities: %v", err)
	}

	// Same owner/repo/number should be rejected
	_, err = db.Exec(`INSERT INTO github_entities (owner, repo, number, kind, first_seen_source)
		VALUES ('mozilla', 'gecko-dev', 123, 'pull', 'tab')`)
	if err == nil {
		t.Fatal("expected unique constraint violation")
	}

	// github_entity_events table should exist
	_, err = db.Exec(`INSERT INTO github_entity_events (entity_id, event_type) VALUES (1, 'tab_seen')`)
	if err != nil {
		t.Fatalf("insert into github_entity_events: %v", err)
	}
}

func TestBugzillaTablesExist(t *testing.T) {
	db := testDB(t)

	// bugzilla_entities table should exist after migration
	_, err := db.Exec(`INSERT INTO bugzilla_entities (host, bug_id, first_seen_source)
		VALUES ('bugzilla.mozilla.org', 1900001, 'tab')`)
	if err != nil {
		t.Fatalf("insert into bugzilla_entities: %v", err)
	}

	// Same host+bug_id should be rejected
	_, err = db.Exec(`INSERT INTO bugzilla_entities (host, bug_id, first_seen_source)
		VALUES ('bugzilla.mozilla.org', 1900001, 'signal')`)
	if err == nil {
		t.Fatal("expected unique constraint violation")
	}

	// bugzilla_entity_events table should exist
	_, err = db.Exec(`INSERT INTO bugzilla_entity_events (entity_id, event_type) VALUES (1, 'tab_seen')`)
	if err != nil {
		t.Fatalf("insert into bugzilla_entity_events: %v", err)
	}
}

func TestMigration6_UrgencyColumns(t *testing.T) {
	db := testDB(t)

	_, err := db.Exec(`INSERT INTO signals (source, title, preview, source_ts, captured_at)
		VALUES ('gmail', 'Alice', 'hello', '2:30 PM', CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	var kind, urgency, urgencySource sql.NullString
	err = db.QueryRow(`SELECT kind, urgency, urgency_source FROM signals WHERE title = 'Alice'`).
		Scan(&kind, &urgency, &urgencySource)
	if err != nil {
		t.Fatalf("select new columns: %v", err)
	}
	if kind.String != "" {
		t.Errorf("expected empty kind, got %q", kind.String)
	}
	if urgency.Valid {
		t.Errorf("expected NULL urgency, got %q", urgency.String)
	}
	if urgencySource.Valid {
		t.Errorf("expected NULL urgency_source, got %q", urgencySource.String)
	}
}

func TestBugzillaEntityNewColumns(t *testing.T) {
	db := testDB(t)
	var title, status, resolution, assignee string
	var lastRefreshed sql.NullTime
	_, err := db.Exec(`INSERT INTO bugzilla_entities
		(host, bug_id, first_seen_source, title, status, resolution, assignee)
		VALUES ('bugzilla.mozilla.org', 9000001, 'tab', 'Test Title', 'RESOLVED', 'FIXED', 'dev@example.com')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	err = db.QueryRow(`SELECT title, status, resolution, assignee, last_refreshed_at
		FROM bugzilla_entities WHERE bug_id = 9000001`).
		Scan(&title, &status, &resolution, &assignee, &lastRefreshed)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if title != "Test Title" || status != "RESOLVED" || resolution != "FIXED" || assignee != "dev@example.com" {
		t.Errorf("got title=%q status=%q resolution=%q assignee=%q", title, status, resolution, assignee)
	}
	if lastRefreshed.Valid {
		t.Error("last_refreshed_at should be NULL initially")
	}
}
