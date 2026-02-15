# Snapshot Revisions Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace named snapshots with auto-incrementing per-profile revision numbers, optional labels, and diff-before-save logic for cron usage.

**Architecture:** Breaking schema change to `snapshots` table (add `rev`, make `name` nullable). Storage layer functions change signatures to use `(profile, rev)` instead of `name`. Snapshot package gains diff-before-save in `Create` and split `Diff` into `DiffAgainstCurrent`/`DiffRevisions`. CLI rewired for new subcommand structure.

**Tech Stack:** Go, SQLite (modernc.org/sqlite), standard library testing

---

### Task 1: Update storage schema and `CreateSnapshot`

**Files:**
- Modify: `internal/storage/storage.go`

**Step 1: Update schema and `SnapshotSummary` struct**

Change the schema constant and struct:

```go
// SnapshotSummary holds the metadata for a snapshot.
type SnapshotSummary struct {
	ID        int64
	Rev       int
	Name      string // optional label
	Profile   string
	CreatedAt time.Time
	TabCount  int
}

const schema = `
CREATE TABLE IF NOT EXISTS snapshots (
    id          INTEGER PRIMARY KEY,
    rev         INTEGER NOT NULL,
    name        TEXT,
    profile     TEXT NOT NULL,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    tab_count   INTEGER NOT NULL,
    UNIQUE(profile, rev)
);

CREATE TABLE IF NOT EXISTS snapshot_groups (
    id          INTEGER PRIMARY KEY,
    snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    firefox_id  TEXT NOT NULL,
    name        TEXT NOT NULL,
    color       TEXT
);

CREATE TABLE IF NOT EXISTS snapshot_tabs (
    id          INTEGER PRIMARY KEY,
    snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    group_id    INTEGER REFERENCES snapshot_groups(id),
    url         TEXT NOT NULL,
    title       TEXT NOT NULL,
    pinned      BOOLEAN DEFAULT FALSE
);
`
```

**Step 2: Rewrite `CreateSnapshot`**

New signature: `CreateSnapshot(db *sql.DB, profile string, groups []SnapshotGroup, tabs []SnapshotTab, label string) (int, error)`

```go
func CreateSnapshot(db *sql.DB, profile string, groups []SnapshotGroup, tabs []SnapshotTab, label string) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Assign next rev for this profile.
	var rev int
	err = tx.QueryRow("SELECT COALESCE(MAX(rev), 0) + 1 FROM snapshots WHERE profile = ?", profile).Scan(&rev)
	if err != nil {
		return 0, fmt.Errorf("compute next rev: %w", err)
	}

	// Convert empty label to nil for SQL.
	var nameVal interface{}
	if label != "" {
		nameVal = label
	}

	tabCount := len(tabs)
	res, err := tx.Exec(
		"INSERT INTO snapshots (rev, name, profile, tab_count) VALUES (?, ?, ?, ?)",
		rev, nameVal, profile, tabCount,
	)
	if err != nil {
		return 0, fmt.Errorf("insert snapshot: %w", err)
	}
	snapID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get snapshot id: %w", err)
	}

	// Insert groups (unchanged logic).
	groupIDs := make([]int64, len(groups))
	for i, g := range groups {
		res, err := tx.Exec(
			"INSERT INTO snapshot_groups (snapshot_id, firefox_id, name, color) VALUES (?, ?, ?, ?)",
			snapID, g.FirefoxID, g.Name, g.Color,
		)
		if err != nil {
			return 0, fmt.Errorf("insert group %q: %w", g.Name, err)
		}
		gID, err := res.LastInsertId()
		if err != nil {
			return 0, fmt.Errorf("get group id: %w", err)
		}
		groupIDs[i] = gID
	}

	// Insert tabs (unchanged logic).
	for _, tab := range tabs {
		var groupID *int64
		if tab.GroupIndex != nil {
			idx := *tab.GroupIndex
			if idx < 0 || idx >= len(groupIDs) {
				return 0, fmt.Errorf("tab %q has invalid group index %d", tab.URL, idx)
			}
			gid := groupIDs[idx]
			groupID = &gid
		}
		_, err := tx.Exec(
			"INSERT INTO snapshot_tabs (snapshot_id, group_id, url, title, pinned) VALUES (?, ?, ?, ?, ?)",
			snapID, groupID, tab.URL, tab.Title, tab.Pinned,
		)
		if err != nil {
			return 0, fmt.Errorf("insert tab %q: %w", tab.URL, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}
	return rev, nil
}
```

**Step 3: Run tests to confirm they fail (old tests reference old signature)**

Run: `GOMAXPROCS=1 go test -p 1 ./internal/storage/ -v`
Expected: Compilation errors (old `CreateSnapshot` signature)

---

### Task 2: Update `GetSnapshot`, `GetLatestSnapshot`, `DeleteSnapshot`, `ListSnapshots`

**Files:**
- Modify: `internal/storage/storage.go`

**Step 1: Rewrite `ListSnapshots` to include rev**

```go
func ListSnapshots(db *sql.DB) ([]SnapshotSummary, error) {
	rows, err := db.Query(
		"SELECT id, rev, name, profile, created_at, tab_count FROM snapshots ORDER BY created_at DESC, id DESC",
	)
	if err != nil {
		return nil, fmt.Errorf("query snapshots: %w", err)
	}
	defer rows.Close()

	var result []SnapshotSummary
	for rows.Next() {
		var s SnapshotSummary
		var name sql.NullString
		if err := rows.Scan(&s.ID, &s.Rev, &name, &s.Profile, &s.CreatedAt, &s.TabCount); err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}
		if name.Valid {
			s.Name = name.String
		}
		result = append(result, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate snapshots: %w", err)
	}
	return result, nil
}
```

**Step 2: Rewrite `GetSnapshot` to use profile+rev**

New signature: `GetSnapshot(db *sql.DB, profile string, rev int) (*SnapshotFull, error)`

```go
func GetSnapshot(db *sql.DB, profile string, rev int) (*SnapshotFull, error) {
	snap := &SnapshotFull{}

	var name sql.NullString
	err := db.QueryRow(
		"SELECT id, rev, name, profile, created_at, tab_count FROM snapshots WHERE profile = ? AND rev = ?",
		profile, rev,
	).Scan(&snap.ID, &snap.Rev, &name, &snap.Profile, &snap.CreatedAt, &snap.TabCount)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("snapshot rev %d not found for profile %q", rev, profile)
		}
		return nil, fmt.Errorf("query snapshot: %w", err)
	}
	if name.Valid {
		snap.Name = name.String
	}

	// Load groups (unchanged).
	groupRows, err := db.Query(
		"SELECT id, firefox_id, name, color FROM snapshot_groups WHERE snapshot_id = ?",
		snap.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("query groups: %w", err)
	}
	defer groupRows.Close()

	groupNameByID := make(map[int64]string)
	for groupRows.Next() {
		var g SnapshotGroup
		if err := groupRows.Scan(&g.ID, &g.FirefoxID, &g.Name, &g.Color); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		snap.Groups = append(snap.Groups, g)
		groupNameByID[g.ID] = g.Name
	}
	if err := groupRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate groups: %w", err)
	}

	// Load tabs (unchanged).
	tabRows, err := db.Query(
		"SELECT url, title, group_id, pinned FROM snapshot_tabs WHERE snapshot_id = ?",
		snap.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("query tabs: %w", err)
	}
	defer tabRows.Close()

	for tabRows.Next() {
		var tab SnapshotTab
		var groupID *int64
		if err := tabRows.Scan(&tab.URL, &tab.Title, &groupID, &tab.Pinned); err != nil {
			return nil, fmt.Errorf("scan tab: %w", err)
		}
		if groupID != nil {
			if gName, ok := groupNameByID[*groupID]; ok {
				tab.GroupName = gName
			}
		}
		snap.Tabs = append(snap.Tabs, tab)
	}
	if err := tabRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tabs: %w", err)
	}

	return snap, nil
}
```

**Step 3: Add `GetLatestSnapshot`**

```go
func GetLatestSnapshot(db *sql.DB, profile string) (*SnapshotFull, error) {
	var rev int
	err := db.QueryRow(
		"SELECT rev FROM snapshots WHERE profile = ? ORDER BY rev DESC LIMIT 1",
		profile,
	).Scan(&rev)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // no snapshots yet
		}
		return nil, fmt.Errorf("query latest rev: %w", err)
	}
	return GetSnapshot(db, profile, rev)
}
```

**Step 4: Rewrite `DeleteSnapshot` to use profile+rev**

```go
func DeleteSnapshot(db *sql.DB, profile string, rev int) error {
	res, err := db.Exec("DELETE FROM snapshots WHERE profile = ? AND rev = ?", profile, rev)
	if err != nil {
		return fmt.Errorf("delete snapshot: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("check rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("snapshot rev %d not found for profile %q", rev, profile)
	}
	return nil
}
```

---

### Task 3: Rewrite storage tests

**Files:**
- Modify: `internal/storage/storage_test.go`

**Step 1: Rewrite all tests for new signatures**

```go
package storage

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

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

func intPtr(i int) *int {
	return &i
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
```

**Step 2: Run tests**

Run: `GOMAXPROCS=1 go test -p 1 ./internal/storage/ -v`
Expected: All PASS

---

### Task 4: Update snapshot package — `Create` with diff-before-save

**Files:**
- Modify: `internal/snapshot/snapshot.go`

**Step 1: Rewrite `Create` function**

New signature: `Create(db *sql.DB, session *types.SessionData, label string) (rev int, created bool, diff *DiffResult, err error)`

```go
func Create(db *sql.DB, session *types.SessionData, label string) (rev int, created bool, diff *DiffResult, err error) {
	profile := session.Profile.Name

	// Check latest snapshot for changes.
	latest, err := storage.GetLatestSnapshot(db, profile)
	if err != nil {
		return 0, false, nil, fmt.Errorf("get latest snapshot: %w", err)
	}

	if latest != nil {
		// Compare URL sets.
		latestURLs := make(map[string]bool, len(latest.Tabs))
		for _, tab := range latest.Tabs {
			latestURLs[tab.URL] = true
		}
		currentURLs := make(map[string]bool, len(session.AllTabs))
		for _, tab := range session.AllTabs {
			currentURLs[tab.URL] = true
		}

		// Check if identical.
		identical := len(latestURLs) == len(currentURLs)
		if identical {
			for url := range currentURLs {
				if !latestURLs[url] {
					identical = false
					break
				}
			}
		}

		if identical {
			return latest.Rev, false, nil, nil
		}
	}

	// Convert session to storage types.
	var groups []storage.SnapshotGroup
	groupIndex := make(map[string]int)

	for _, g := range session.Groups {
		if g.ID == "" {
			continue
		}
		idx := len(groups)
		groupIndex[g.ID] = idx
		groups = append(groups, storage.SnapshotGroup{
			FirefoxID: g.ID,
			Name:      g.Name,
			Color:     g.Color,
		})
	}

	tabs := make([]storage.SnapshotTab, 0, len(session.AllTabs))
	for _, t := range session.AllTabs {
		tab := storage.SnapshotTab{
			URL:    t.URL,
			Title:  t.Title,
			Pinned: t.Pinned,
		}
		if t.GroupID != "" {
			if idx, ok := groupIndex[t.GroupID]; ok {
				tab.GroupIndex = &idx
			}
		}
		tabs = append(tabs, tab)
	}

	newRev, err := storage.CreateSnapshot(db, profile, groups, tabs, label)
	if err != nil {
		return 0, false, nil, err
	}

	// Compute diff for output (only if there was a previous snapshot).
	if latest != nil {
		diff = diffSnapshots(latest, session)
	}

	return newRev, true, diff, nil
}

// diffSnapshots compares a stored snapshot against current session data.
func diffSnapshots(snap *storage.SnapshotFull, current *types.SessionData) *DiffResult {
	snapshotURLs := make(map[string]DiffEntry, len(snap.Tabs))
	for _, tab := range snap.Tabs {
		snapshotURLs[tab.URL] = DiffEntry{
			URL:   tab.URL,
			Title: tab.Title,
			Group: tab.GroupName,
		}
	}

	groupNames := make(map[string]string)
	for _, g := range current.Groups {
		if g.ID != "" {
			groupNames[g.ID] = g.Name
		}
	}

	currentURLs := make(map[string]DiffEntry, len(current.AllTabs))
	for _, tab := range current.AllTabs {
		groupName := ""
		if tab.GroupID != "" {
			groupName = groupNames[tab.GroupID]
		}
		currentURLs[tab.URL] = DiffEntry{
			URL:   tab.URL,
			Title: tab.Title,
			Group: groupName,
		}
	}

	result := &DiffResult{}
	for url, entry := range currentURLs {
		if _, ok := snapshotURLs[url]; !ok {
			result.Added = append(result.Added, entry)
		}
	}
	for url, entry := range snapshotURLs {
		if _, ok := currentURLs[url]; !ok {
			result.Removed = append(result.Removed, entry)
		}
	}

	return result
}
```

**Step 2: Remove the `Restore` function's name-based lookup — update to use profile+rev**

In `Restore`, change the lookup from `storage.GetSnapshot(db, name)` to `storage.GetSnapshot(db, profile, rev)`:

```go
func Restore(db *sql.DB, profile string, rev int, port int) error {
	snap, err := storage.GetSnapshot(db, profile, rev)
	if err != nil {
		return err
	}
	// ... rest unchanged ...
}
```

---

### Task 5: Update diff.go — `DiffAgainstCurrent` and `DiffRevisions`

**Files:**
- Modify: `internal/snapshot/diff.go`

**Step 1: Replace `Diff` with `DiffAgainstCurrent` and `DiffRevisions`**

Remove old `Diff` function. Remove `SnapshotName` from `DiffResult`, replace with `RevFrom` and `RevTo` ints.

```go
package snapshot

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/lotas/tabsordnung/internal/storage"
	"github.com/lotas/tabsordnung/internal/types"
)

type DiffEntry struct {
	URL   string
	Title string
	Group string
}

type DiffResult struct {
	RevFrom int // 0 means "current session"
	RevTo   int // 0 means "current session"
	Added   []DiffEntry
	Removed []DiffEntry
}

// DiffAgainstCurrent compares a stored snapshot against current session data.
// If rev is 0, uses the latest snapshot.
func DiffAgainstCurrent(db *sql.DB, profile string, rev int, current *types.SessionData) (*DiffResult, error) {
	var snap *storage.SnapshotFull
	var err error

	if rev == 0 {
		snap, err = storage.GetLatestSnapshot(db, profile)
		if err != nil {
			return nil, err
		}
		if snap == nil {
			return nil, fmt.Errorf("no snapshots found for profile %q", profile)
		}
	} else {
		snap, err = storage.GetSnapshot(db, profile, rev)
		if err != nil {
			return nil, err
		}
	}

	result := diffSnapshots(snap, current)
	result.RevFrom = snap.Rev
	result.RevTo = 0
	return result, nil
}

// DiffRevisions compares two stored snapshots.
func DiffRevisions(db *sql.DB, profile string, rev1, rev2 int) (*DiffResult, error) {
	snap1, err := storage.GetSnapshot(db, profile, rev1)
	if err != nil {
		return nil, fmt.Errorf("load rev %d: %w", rev1, err)
	}
	snap2, err := storage.GetSnapshot(db, profile, rev2)
	if err != nil {
		return nil, fmt.Errorf("load rev %d: %w", rev2, err)
	}

	// Build URL maps.
	urls1 := make(map[string]DiffEntry, len(snap1.Tabs))
	for _, tab := range snap1.Tabs {
		urls1[tab.URL] = DiffEntry{URL: tab.URL, Title: tab.Title, Group: tab.GroupName}
	}
	urls2 := make(map[string]DiffEntry, len(snap2.Tabs))
	for _, tab := range snap2.Tabs {
		urls2[tab.URL] = DiffEntry{URL: tab.URL, Title: tab.Title, Group: tab.GroupName}
	}

	result := &DiffResult{RevFrom: rev1, RevTo: rev2}

	// Added: in rev2 but not rev1.
	for url, entry := range urls2 {
		if _, ok := urls1[url]; !ok {
			result.Added = append(result.Added, entry)
		}
	}
	// Removed: in rev1 but not rev2.
	for url, entry := range urls1 {
		if _, ok := urls2[url]; !ok {
			result.Removed = append(result.Removed, entry)
		}
	}

	return result, nil
}

func FormatDiff(d *DiffResult) string {
	var sb strings.Builder

	if d.RevTo == 0 {
		fmt.Fprintf(&sb, "Diff: snapshot #%d vs current\n", d.RevFrom)
	} else {
		fmt.Fprintf(&sb, "Diff: snapshot #%d vs #%d\n", d.RevFrom, d.RevTo)
	}
	fmt.Fprintf(&sb, "Added: %d  Removed: %d\n", len(d.Added), len(d.Removed))

	if len(d.Added) > 0 {
		sb.WriteString("\n+ Added:\n")
		for _, e := range d.Added {
			if e.Group != "" {
				fmt.Fprintf(&sb, "  + %s [%s]\n", e.URL, e.Group)
			} else {
				fmt.Fprintf(&sb, "  + %s\n", e.URL)
			}
		}
	}

	if len(d.Removed) > 0 {
		sb.WriteString("\n- Removed:\n")
		for _, e := range d.Removed {
			if e.Group != "" {
				fmt.Fprintf(&sb, "  - %s [%s]\n", e.URL, e.Group)
			} else {
				fmt.Fprintf(&sb, "  - %s\n", e.URL)
			}
		}
	}

	if len(d.Added) == 0 && len(d.Removed) == 0 {
		sb.WriteString("\nNo changes.\n")
	}

	return sb.String()
}
```

---

### Task 6: Rewrite snapshot tests

**Files:**
- Modify: `internal/snapshot/snapshot_test.go`
- Modify: `internal/snapshot/diff_test.go`

**Step 1: Rewrite `snapshot_test.go`**

```go
package snapshot

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/lotas/tabsordnung/internal/storage"
	"github.com/lotas/tabsordnung/internal/types"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := storage.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB(%q): %v", dbPath, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestCreateFirstSnapshot(t *testing.T) {
	db := testDB(t)

	session := &types.SessionData{
		Groups: []*types.TabGroup{
			{ID: "g1", Name: "Work", Color: "blue", Tabs: []*types.Tab{
				{URL: "https://example.com", Title: "Example", GroupID: "g1", Pinned: true},
			}},
			{ID: "", Name: "Ungrouped", Tabs: []*types.Tab{
				{URL: "https://ungrouped.com", Title: "Ungrouped"},
			}},
		},
		AllTabs: []*types.Tab{
			{URL: "https://example.com", Title: "Example", GroupID: "g1", Pinned: true},
			{URL: "https://ungrouped.com", Title: "Ungrouped"},
		},
		Profile:  types.Profile{Name: "default"},
		ParsedAt: time.Now(),
	}

	rev, created, diff, err := Create(db, session, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !created {
		t.Fatal("expected created=true for first snapshot")
	}
	if rev != 1 {
		t.Errorf("expected rev 1, got %d", rev)
	}
	if diff != nil {
		t.Error("expected nil diff for first snapshot")
	}

	// Verify stored correctly.
	snap, err := storage.GetSnapshot(db, "default", 1)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if snap.TabCount != 2 {
		t.Errorf("expected 2 tabs, got %d", snap.TabCount)
	}
	if len(snap.Groups) != 1 {
		t.Errorf("expected 1 group (ungrouped excluded), got %d", len(snap.Groups))
	}
}

func TestCreateSkipsWhenNoChanges(t *testing.T) {
	db := testDB(t)

	session := &types.SessionData{
		AllTabs: []*types.Tab{
			{URL: "https://example.com", Title: "Example"},
		},
		Profile:  types.Profile{Name: "default"},
		ParsedAt: time.Now(),
	}

	// First snapshot.
	rev1, created1, _, err := Create(db, session, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !created1 || rev1 != 1 {
		t.Fatalf("expected rev=1, created=true; got rev=%d, created=%v", rev1, created1)
	}

	// Same tabs — should skip.
	rev2, created2, _, err := Create(db, session, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created2 {
		t.Error("expected created=false when no changes")
	}
	if rev2 != 1 {
		t.Errorf("expected rev 1 (latest), got %d", rev2)
	}
}

func TestCreateDetectsChanges(t *testing.T) {
	db := testDB(t)

	session1 := &types.SessionData{
		AllTabs: []*types.Tab{
			{URL: "https://a.com", Title: "A"},
			{URL: "https://b.com", Title: "B"},
		},
		Profile:  types.Profile{Name: "default"},
		ParsedAt: time.Now(),
	}

	Create(db, session1, "")

	// Add a tab, remove a tab.
	session2 := &types.SessionData{
		AllTabs: []*types.Tab{
			{URL: "https://a.com", Title: "A"},
			{URL: "https://c.com", Title: "C"},
		},
		Profile:  types.Profile{Name: "default"},
		ParsedAt: time.Now(),
	}

	rev, created, diff, err := Create(db, session2, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !created {
		t.Fatal("expected created=true when tabs changed")
	}
	if rev != 2 {
		t.Errorf("expected rev 2, got %d", rev)
	}
	if diff == nil {
		t.Fatal("expected non-nil diff")
	}
	if len(diff.Added) != 1 {
		t.Errorf("expected 1 added, got %d", len(diff.Added))
	}
	if len(diff.Removed) != 1 {
		t.Errorf("expected 1 removed, got %d", len(diff.Removed))
	}
}

func TestCreateWithLabel(t *testing.T) {
	db := testDB(t)

	session := &types.SessionData{
		AllTabs:  []*types.Tab{{URL: "https://a.com", Title: "A"}},
		Profile:  types.Profile{Name: "default"},
		ParsedAt: time.Now(),
	}

	rev, _, _, err := Create(db, session, "before cleanup")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	snap, err := storage.GetSnapshot(db, "default", rev)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if snap.Name != "before cleanup" {
		t.Errorf("expected label 'before cleanup', got %q", snap.Name)
	}
}
```

**Step 2: Rewrite `diff_test.go`**

```go
package snapshot

import (
	"testing"
	"time"

	"github.com/lotas/tabsordnung/internal/storage"
	"github.com/lotas/tabsordnung/internal/types"
)

func TestDiffAgainstCurrent(t *testing.T) {
	db := testDB(t)

	// Create a snapshot with 3 tabs.
	groups := []storage.SnapshotGroup{{FirefoxID: "g1", Name: "Work", Color: "blue"}}
	tabs := []storage.SnapshotTab{
		{URL: "https://kept.com", Title: "Kept", GroupIndex: intPtr(0)},
		{URL: "https://removed.com", Title: "Removed", GroupIndex: intPtr(0)},
	}
	storage.CreateSnapshot(db, "default", groups, tabs, "")

	current := &types.SessionData{
		Groups: []*types.TabGroup{{ID: "g1", Name: "Work", Color: "blue"}},
		AllTabs: []*types.Tab{
			{URL: "https://kept.com", Title: "Kept", GroupID: "g1"},
			{URL: "https://added.com", Title: "Added", GroupID: "g1"},
		},
		Profile:  types.Profile{Name: "default"},
		ParsedAt: time.Now(),
	}

	// rev=0 means latest.
	result, err := DiffAgainstCurrent(db, "default", 0, current)
	if err != nil {
		t.Fatalf("DiffAgainstCurrent: %v", err)
	}

	if result.RevFrom != 1 {
		t.Errorf("expected RevFrom=1, got %d", result.RevFrom)
	}
	if len(result.Added) != 1 || result.Added[0].URL != "https://added.com" {
		t.Errorf("expected 1 added (added.com), got %v", result.Added)
	}
	if len(result.Removed) != 1 || result.Removed[0].URL != "https://removed.com" {
		t.Errorf("expected 1 removed (removed.com), got %v", result.Removed)
	}
}

func TestDiffAgainstCurrentByRev(t *testing.T) {
	db := testDB(t)

	storage.CreateSnapshot(db, "default", nil, []storage.SnapshotTab{
		{URL: "https://a.com", Title: "A"},
	}, "")
	storage.CreateSnapshot(db, "default", nil, []storage.SnapshotTab{
		{URL: "https://b.com", Title: "B"},
	}, "")

	current := &types.SessionData{
		AllTabs:  []*types.Tab{{URL: "https://c.com", Title: "C"}},
		Profile:  types.Profile{Name: "default"},
		ParsedAt: time.Now(),
	}

	// Diff against rev 1 specifically.
	result, err := DiffAgainstCurrent(db, "default", 1, current)
	if err != nil {
		t.Fatalf("DiffAgainstCurrent: %v", err)
	}
	if result.RevFrom != 1 {
		t.Errorf("expected RevFrom=1, got %d", result.RevFrom)
	}
	if len(result.Added) != 1 || result.Added[0].URL != "https://c.com" {
		t.Errorf("unexpected added: %v", result.Added)
	}
	if len(result.Removed) != 1 || result.Removed[0].URL != "https://a.com" {
		t.Errorf("unexpected removed: %v", result.Removed)
	}
}

func TestDiffRevisions(t *testing.T) {
	db := testDB(t)

	storage.CreateSnapshot(db, "default", nil, []storage.SnapshotTab{
		{URL: "https://a.com", Title: "A"},
		{URL: "https://b.com", Title: "B"},
	}, "")
	storage.CreateSnapshot(db, "default", nil, []storage.SnapshotTab{
		{URL: "https://b.com", Title: "B"},
		{URL: "https://c.com", Title: "C"},
	}, "")

	result, err := DiffRevisions(db, "default", 1, 2)
	if err != nil {
		t.Fatalf("DiffRevisions: %v", err)
	}
	if result.RevFrom != 1 || result.RevTo != 2 {
		t.Errorf("expected RevFrom=1, RevTo=2, got %d, %d", result.RevFrom, result.RevTo)
	}
	if len(result.Added) != 1 || result.Added[0].URL != "https://c.com" {
		t.Errorf("expected added c.com, got %v", result.Added)
	}
	if len(result.Removed) != 1 || result.Removed[0].URL != "https://a.com" {
		t.Errorf("expected removed a.com, got %v", result.Removed)
	}
}

func TestDiffNoChanges(t *testing.T) {
	db := testDB(t)

	storage.CreateSnapshot(db, "default", nil, []storage.SnapshotTab{
		{URL: "https://example.com", Title: "Example"},
	}, "")

	current := &types.SessionData{
		AllTabs:  []*types.Tab{{URL: "https://example.com", Title: "Example"}},
		Profile:  types.Profile{Name: "default"},
		ParsedAt: time.Now(),
	}

	result, err := DiffAgainstCurrent(db, "default", 0, current)
	if err != nil {
		t.Fatalf("DiffAgainstCurrent: %v", err)
	}
	if len(result.Added) != 0 || len(result.Removed) != 0 {
		t.Errorf("expected no changes, got added=%d removed=%d", len(result.Added), len(result.Removed))
	}

	formatted := FormatDiff(result)
	if !contains(formatted, "No changes") {
		t.Errorf("expected 'No changes' in output, got: %q", formatted)
	}
}

func TestDiffNoSnapshots(t *testing.T) {
	db := testDB(t)

	current := &types.SessionData{
		AllTabs:  []*types.Tab{{URL: "https://example.com", Title: "Example"}},
		Profile:  types.Profile{Name: "default"},
		ParsedAt: time.Now(),
	}

	_, err := DiffAgainstCurrent(db, "default", 0, current)
	if err == nil {
		t.Fatal("expected error when no snapshots exist")
	}
}

func intPtr(i int) *int {
	return &i
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
```

**Step 3: Run all snapshot tests**

Run: `GOMAXPROCS=1 go test -p 1 ./internal/snapshot/ -v`
Expected: All PASS

---

### Task 7: Rewire CLI in main.go

**Files:**
- Modify: `main.go`

**Step 1: Rewrite `runSnapshot`**

The key change: bare `snapshot` (no subcommand or args starting with `--`) triggers auto-create. Subcommands `list`, `diff`, `delete`, `restore` use rev numbers.

```go
func runSnapshot(args []string) {
	// If no args or first arg is a flag, it's the auto-create flow.
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		runSnapshotCreate(args)
		return
	}

	subcmd := args[0]
	subArgs := args[1:]

	switch subcmd {
	case "list":
		runSnapshotList()
	case "diff":
		runSnapshotDiff(subArgs)
	case "delete":
		runSnapshotDelete(subArgs)
	case "restore":
		runSnapshotRestore(subArgs)
	// Keep "create" as an alias for discoverability.
	case "create":
		runSnapshotCreate(subArgs)
	default:
		fmt.Fprintf(os.Stderr, "Unknown snapshot command %q. Use list, diff, delete, or restore.\n", subcmd)
		os.Exit(1)
	}
}

func runSnapshotCreate(args []string) {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	profileName := fs.String("profile", "", "Firefox profile name")
	label := fs.String("label", "", "Optional label for the snapshot")
	fs.Parse(args)

	session, err := resolveSession(resolveProfileName(*profileName))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	db, err := openDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	rev, created, diff, err := snapshot.Create(db, session, *label)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating snapshot: %v\n", err)
		os.Exit(1)
	}

	if !created {
		fmt.Printf("No changes since snapshot #%d\n", rev)
		return
	}

	groups := 0
	for _, g := range session.Groups {
		if g.ID != "" {
			groups++
		}
	}
	fmt.Printf("Snapshot #%d created: %d tabs in %d groups\n", rev, len(session.AllTabs), groups)

	if diff != nil && (len(diff.Added) > 0 || len(diff.Removed) > 0) {
		fmt.Println()
		// Print just the added/removed sections (skip the header since we already printed one).
		if len(diff.Added) > 0 {
			fmt.Printf("+ Added (%d):\n", len(diff.Added))
			for _, e := range diff.Added {
				if e.Group != "" {
					fmt.Printf("  + %s [%s]\n", e.URL, e.Group)
				} else {
					fmt.Printf("  + %s\n", e.URL)
				}
			}
		}
		if len(diff.Removed) > 0 {
			if len(diff.Added) > 0 {
				fmt.Println()
			}
			fmt.Printf("- Removed (%d):\n", len(diff.Removed))
			for _, e := range diff.Removed {
				if e.Group != "" {
					fmt.Printf("  - %s [%s]\n", e.URL, e.Group)
				} else {
					fmt.Printf("  - %s\n", e.URL)
				}
			}
		}
	}
}

func runSnapshotList() {
	db, err := openDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	snaps, err := storage.ListSnapshots(db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing snapshots: %v\n", err)
		os.Exit(1)
	}

	if len(snaps) == 0 {
		fmt.Println("No snapshots found.")
		return
	}

	fmt.Printf("%-5s %5s  %-12s %-20s  %s\n", "REV", "TABS", "PROFILE", "LABEL", "CREATED")
	for _, s := range snaps {
		fmt.Printf("%5d %5d  %-12s %-20s  %s\n",
			s.Rev,
			s.TabCount,
			s.Profile,
			s.Name,
			s.CreatedAt.Format("2006-01-02 15:04"),
		)
	}
}

func runSnapshotDiff(args []string) {
	fs := flag.NewFlagSet("snapshot diff", flag.ExitOnError)
	profileName := fs.String("profile", "", "Firefox profile name")
	fs.Parse(reorderArgs(args))

	profile := resolveProfileName(*profileName)

	db, err := openDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	switch fs.NArg() {
	case 0:
		// Diff latest vs current.
		session, err := resolveSession(profile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		result, err := snapshot.DiffAgainstCurrent(db, session.Profile.Name, 0, session)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(snapshot.FormatDiff(result))

	case 1:
		// Diff specific rev vs current.
		rev, err := strconv.Atoi(fs.Arg(0))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid revision number: %s\n", fs.Arg(0))
			os.Exit(1)
		}
		session, err := resolveSession(profile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		result, err := snapshot.DiffAgainstCurrent(db, session.Profile.Name, rev, session)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(snapshot.FormatDiff(result))

	case 2:
		// Diff two revisions.
		rev1, err := strconv.Atoi(fs.Arg(0))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid revision number: %s\n", fs.Arg(0))
			os.Exit(1)
		}
		rev2, err := strconv.Atoi(fs.Arg(1))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid revision number: %s\n", fs.Arg(1))
			os.Exit(1)
		}
		// For rev-vs-rev diff, we need a profile name. Resolve from flag/env, or discover default.
		resolvedProfile := profile
		if resolvedProfile == "" {
			session, err := resolveSession("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			resolvedProfile = session.Profile.Name
		}
		result, err := snapshot.DiffRevisions(db, resolvedProfile, rev1, rev2)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(snapshot.FormatDiff(result))

	default:
		fmt.Fprintln(os.Stderr, "Usage: tabsordnung snapshot diff [rev] [rev2] [--profile name]")
		os.Exit(1)
	}
}

func runSnapshotDelete(args []string) {
	fs := flag.NewFlagSet("snapshot delete", flag.ExitOnError)
	profileName := fs.String("profile", "", "Firefox profile name")
	yes := fs.Bool("yes", false, "Skip confirmation prompt")
	fs.Parse(reorderArgs(args))

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: tabsordnung snapshot delete <rev> [--profile name] [--yes]")
		os.Exit(1)
	}

	rev, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid revision number: %s\n", fs.Arg(0))
		os.Exit(1)
	}

	// Resolve profile.
	profile := resolveProfileName(*profileName)
	if profile == "" {
		session, err := resolveSession("")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		profile = session.Profile.Name
	}

	if !*yes {
		fmt.Printf("Delete snapshot #%d? [y/N] ", rev)
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("Aborted.")
			return
		}
	}

	db, err := openDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := storage.DeleteSnapshot(db, profile, rev); err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting snapshot: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Snapshot #%d deleted.\n", rev)
}

func runSnapshotRestore(args []string) {
	fs := flag.NewFlagSet("snapshot restore", flag.ExitOnError)
	profileName := fs.String("profile", "", "Firefox profile name")
	port := fs.Int("port", 19191, "WebSocket port for live mode")
	fs.Parse(reorderArgs(args))

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: tabsordnung snapshot restore <rev> [--profile name] [--port N]")
		os.Exit(1)
	}

	rev, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid revision number: %s\n", fs.Arg(0))
		os.Exit(1)
	}

	// Resolve profile.
	profile := resolveProfileName(*profileName)
	if profile == "" {
		session, err := resolveSession("")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		profile = session.Profile.Name
	}

	db, err := openDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := snapshot.Restore(db, profile, rev, *port); err != nil {
		fmt.Fprintf(os.Stderr, "Error restoring snapshot: %v\n", err)
		os.Exit(1)
	}
}
```

**Step 2: Add `strconv` to imports in `main.go`**

Add `"strconv"` to the import block.

**Step 3: Update `printHelp`**

Replace snapshot section in help text:

```
  tabsordnung snapshot [--profile X] [--label "text"]  Auto-snapshot (only if changed)
  tabsordnung snapshot list                             List saved snapshots
  tabsordnung snapshot diff [rev] [rev2] [--profile X]  Compare snapshots or current tabs
  tabsordnung snapshot delete <rev> [--profile X] [--yes]  Delete a snapshot
  tabsordnung snapshot restore <rev> [--profile X] [--port N]  Restore tabs via live mode
```

---

### Task 8: Build and verify

**Step 1: Run all tests**

Run: `GOMAXPROCS=1 go test -p 1 ./... -v`
Expected: All PASS

**Step 2: Build**

Run: `GOMAXPROCS=1 go build -p 1 -o tabsordnung .`
Expected: Clean build

**Step 3: Verify help output**

Run: `./tabsordnung help`
Expected: Updated snapshot commands shown
