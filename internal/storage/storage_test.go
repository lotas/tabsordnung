package storage

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
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

func TestOpenDB(t *testing.T) {
	dir := t.TempDir()
	// Nested path to verify parent directory creation.
	dbPath := filepath.Join(dir, "sub", "dir", "tabsordnung.db")

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB failed: %v", err)
	}
	defer db.Close()

	// Verify the file was created on disk.
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("database file not found: %v", err)
	}

	// Verify tables exist by inserting into each one.
	res, err := db.Exec(`INSERT INTO snapshots (name, profile, tab_count) VALUES ('test', 'default', 5)`)
	if err != nil {
		t.Fatalf("insert into snapshots: %v", err)
	}
	snapID, _ := res.LastInsertId()

	_, err = db.Exec(`INSERT INTO snapshot_groups (snapshot_id, firefox_id, name, color) VALUES (?, 'g1', 'Group 1', 'blue')`, snapID)
	if err != nil {
		t.Fatalf("insert into snapshot_groups: %v", err)
	}

	_, err = db.Exec(`INSERT INTO snapshot_tabs (snapshot_id, url, title) VALUES (?, 'https://example.com', 'Example')`, snapID)
	if err != nil {
		t.Fatalf("insert into snapshot_tabs: %v", err)
	}

	// Verify foreign keys are enforced: insert a tab with a bad snapshot_id.
	_, err = db.Exec(`INSERT INTO snapshot_tabs (snapshot_id, url, title) VALUES (9999, 'https://bad.com', 'Bad')`)
	if err == nil {
		t.Fatal("expected foreign key violation, got nil")
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
		{FirefoxID: "g2", Name: "Personal", Color: "red"},
	}
	tabs := []SnapshotTab{
		{URL: "https://example.com", Title: "Example", GroupIndex: intPtr(0), Pinned: true},
		{URL: "https://go.dev", Title: "Go", GroupIndex: intPtr(0), Pinned: false},
		{URL: "https://mozilla.org", Title: "Mozilla", GroupIndex: intPtr(1), Pinned: false},
		{URL: "https://ungrouped.com", Title: "Ungrouped", GroupIndex: nil, Pinned: false},
	}

	// Create first snapshot.
	err := CreateSnapshot(db, "snap-1", "default", groups, tabs)
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	// Duplicate name should error.
	err = CreateSnapshot(db, "snap-1", "default", nil, nil)
	if err == nil {
		t.Fatal("expected error on duplicate name, got nil")
	}

	// Create second snapshot.
	err = CreateSnapshot(db, "snap-2", "work", nil, []SnapshotTab{
		{URL: "https://a.com", Title: "A"},
	})
	if err != nil {
		t.Fatalf("CreateSnapshot snap-2: %v", err)
	}

	// List should return newest first.
	list, err := ListSnapshots(db)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(list))
	}

	// Most recent should be first (snap-2).
	if list[0].Name != "snap-2" {
		t.Errorf("expected snap-2 first, got %s", list[0].Name)
	}
	if list[0].TabCount != 1 {
		t.Errorf("expected tab_count 1, got %d", list[0].TabCount)
	}
	if list[0].Profile != "work" {
		t.Errorf("expected profile 'work', got %s", list[0].Profile)
	}

	if list[1].Name != "snap-1" {
		t.Errorf("expected snap-1 second, got %s", list[1].Name)
	}
	if list[1].TabCount != 4 {
		t.Errorf("expected tab_count 4, got %d", list[1].TabCount)
	}
}

func TestGetSnapshot(t *testing.T) {
	db := testDB(t)

	groups := []SnapshotGroup{
		{FirefoxID: "g1", Name: "Dev", Color: "green"},
	}
	tabs := []SnapshotTab{
		{URL: "https://example.com", Title: "Example", GroupIndex: intPtr(0), Pinned: true},
		{URL: "https://ungrouped.com", Title: "Ungrouped", GroupIndex: nil, Pinned: false},
	}

	err := CreateSnapshot(db, "my-snap", "default", groups, tabs)
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	snap, err := GetSnapshot(db, "my-snap")
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}

	if snap.Name != "my-snap" {
		t.Errorf("expected name 'my-snap', got %s", snap.Name)
	}
	if snap.Profile != "default" {
		t.Errorf("expected profile 'default', got %s", snap.Profile)
	}
	if snap.TabCount != 2 {
		t.Errorf("expected tab_count 2, got %d", snap.TabCount)
	}
	if snap.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}

	// Verify groups.
	if len(snap.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(snap.Groups))
	}
	g := snap.Groups[0]
	if g.FirefoxID != "g1" || g.Name != "Dev" || g.Color != "green" {
		t.Errorf("unexpected group: %+v", g)
	}

	// Verify tabs.
	if len(snap.Tabs) != 2 {
		t.Fatalf("expected 2 tabs, got %d", len(snap.Tabs))
	}

	// Find the grouped tab.
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
	if ungroupedTab.Pinned {
		t.Error("expected ungrouped tab to not be pinned")
	}
	if ungroupedTab.GroupName != "" {
		t.Errorf("expected empty GroupName, got %q", ungroupedTab.GroupName)
	}

	// Non-existent snapshot should error.
	_, err = GetSnapshot(db, "no-such-snap")
	if err == nil {
		t.Fatal("expected error for non-existent snapshot, got nil")
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

	err := CreateSnapshot(db, "to-delete", "default", groups, tabs)
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	// Verify it exists.
	list, _ := ListSnapshots(db)
	if len(list) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(list))
	}

	// Delete it.
	err = DeleteSnapshot(db, "to-delete")
	if err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}

	// Verify it is gone.
	list, _ = ListSnapshots(db)
	if len(list) != 0 {
		t.Fatalf("expected 0 snapshots after delete, got %d", len(list))
	}

	// Deleting non-existent should error.
	err = DeleteSnapshot(db, "to-delete")
	if err == nil {
		t.Fatal("expected error deleting non-existent snapshot, got nil")
	}

	// Verify cascade: no orphan rows left in groups or tabs tables.
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
