# Snapshots & GitHub Triage — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add `tabsordnung snapshot` (create/list/restore/diff/delete) and `tabsordnung triage` (GitHub tab classification) as CLI subcommands.

**Architecture:** SQLite for snapshot storage (`~/.local/share/tabsordnung/tabsordnung.db`), pure Go driver (`modernc.org/sqlite`). Snapshot restore and triage apply use live mode WebSocket. Triage extends existing GitHub GraphQL query with assignee/reviewer/updatedAt fields.

**Tech Stack:** Go, SQLite (`modernc.org/sqlite`), existing WebSocket server (`nhooyr.io/websocket`)

**Constraints:** No git operations. All `go` commands use `GOMAXPROCS=1 -p 1`.

---

### Task 1: Add SQLite Dependency

**Files:**
- Modify: `go.mod`

**Step 1: Add the dependency**

Run: `cd /Users/ykurmyza/dev/Mozilla/tabsordnung && GOMAXPROCS=1 go get -x modernc.org/sqlite`

**Step 2: Verify**

Run: `grep modernc go.mod`
Expected: `modernc.org/sqlite` appears in require block

---

### Task 2: Storage Package — DB Init & Schema

**Files:**
- Create: `internal/storage/storage.go`
- Create: `internal/storage/storage_test.go`

**Step 1: Write the failing test**

```go
// internal/storage/storage_test.go
package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	// Verify file was created
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("db file not created: %v", err)
	}

	// Verify tables exist by inserting a snapshot
	_, err = db.Exec(`INSERT INTO snapshots (name, profile, tab_count) VALUES ('test', 'default', 0)`)
	if err != nil {
		t.Fatalf("insert into snapshots: %v", err)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `GOMAXPROCS=1 go test -p 1 -run TestOpenDB ./internal/storage/ -v`
Expected: FAIL — package doesn't exist

**Step 3: Write the implementation**

```go
// internal/storage/storage.go
package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS snapshots (
    id          INTEGER PRIMARY KEY,
    name        TEXT UNIQUE NOT NULL,
    profile     TEXT NOT NULL,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    tab_count   INTEGER NOT NULL
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

// DefaultDBPath returns ~/.local/share/tabsordnung/tabsordnung.db.
func DefaultDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "tabsordnung", "tabsordnung.db"), nil
}

// OpenDB opens (or creates) the SQLite database at the given path.
func OpenDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Enable foreign keys and WAL mode
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	return db, nil
}
```

**Step 4: Run test to verify it passes**

Run: `GOMAXPROCS=1 go test -p 1 -run TestOpenDB ./internal/storage/ -v`
Expected: PASS

---

### Task 3: Storage Package — Snapshot CRUD

**Files:**
- Modify: `internal/storage/storage.go`
- Modify: `internal/storage/storage_test.go`

**Step 1: Write failing tests for Create and List**

Append to `internal/storage/storage_test.go`:

```go
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestCreateAndListSnapshots(t *testing.T) {
	db := testDB(t)

	groups := []SnapshotGroup{
		{FirefoxID: "1", Name: "Work", Color: "blue"},
	}
	tabs := []SnapshotTab{
		{URL: "https://github.com/org/repo", Title: "Repo", GroupIndex: intPtr(0), Pinned: false},
		{URL: "https://google.com", Title: "Google", GroupIndex: nil, Pinned: true},
	}

	err := CreateSnapshot(db, "test-snap", "default-release", groups, tabs)
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	// Duplicate name should error
	err = CreateSnapshot(db, "test-snap", "default-release", nil, nil)
	if err == nil {
		t.Fatal("expected error for duplicate name")
	}

	snaps, err := ListSnapshots(db)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	if snaps[0].Name != "test-snap" {
		t.Errorf("name: got %q, want %q", snaps[0].Name, "test-snap")
	}
	if snaps[0].TabCount != 2 {
		t.Errorf("tab_count: got %d, want 2", snaps[0].TabCount)
	}
}

func TestGetSnapshot(t *testing.T) {
	db := testDB(t)

	groups := []SnapshotGroup{
		{FirefoxID: "1", Name: "Work", Color: "blue"},
	}
	tabs := []SnapshotTab{
		{URL: "https://example.com", Title: "Example", GroupIndex: intPtr(0), Pinned: true},
		{URL: "https://other.com", Title: "Other", GroupIndex: nil, Pinned: false},
	}
	if err := CreateSnapshot(db, "snap1", "profile1", groups, tabs); err != nil {
		t.Fatal(err)
	}

	snap, err := GetSnapshot(db, "snap1")
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if snap.Name != "snap1" {
		t.Errorf("name: got %q", snap.Name)
	}
	if len(snap.Groups) != 1 {
		t.Fatalf("groups: got %d, want 1", len(snap.Groups))
	}
	if snap.Groups[0].Name != "Work" {
		t.Errorf("group name: got %q", snap.Groups[0].Name)
	}
	if len(snap.Tabs) != 2 {
		t.Fatalf("tabs: got %d, want 2", len(snap.Tabs))
	}
	if !snap.Tabs[0].Pinned {
		t.Error("first tab should be pinned")
	}
}

func TestDeleteSnapshot(t *testing.T) {
	db := testDB(t)

	if err := CreateSnapshot(db, "to-delete", "p", nil, []SnapshotTab{
		{URL: "https://x.com", Title: "X"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := DeleteSnapshot(db, "to-delete"); err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}

	snaps, err := ListSnapshots(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 0 {
		t.Errorf("expected 0 snapshots, got %d", len(snaps))
	}
}

func intPtr(i int) *int { return &i }
```

**Step 2: Run tests to verify they fail**

Run: `GOMAXPROCS=1 go test -p 1 -run 'TestCreate|TestGet|TestDelete' ./internal/storage/ -v`
Expected: FAIL — types/functions not defined

**Step 3: Write the implementation**

Append to `internal/storage/storage.go`:

```go
import "time" // add to imports

// SnapshotSummary is returned by ListSnapshots.
type SnapshotSummary struct {
	ID        int64
	Name      string
	Profile   string
	CreatedAt time.Time
	TabCount  int
}

// SnapshotGroup is a group within a snapshot.
type SnapshotGroup struct {
	ID        int64  // set after insert
	FirefoxID string
	Name      string
	Color     string
}

// SnapshotTab is a tab within a snapshot.
type SnapshotTab struct {
	URL        string
	Title      string
	GroupIndex *int // index into the groups slice; nil = ungrouped
	Pinned     bool
	GroupName  string // populated by GetSnapshot
}

// SnapshotFull is a complete snapshot with groups and tabs.
type SnapshotFull struct {
	SnapshotSummary
	Groups []SnapshotGroup
	Tabs   []SnapshotTab
}

// CreateSnapshot inserts a new snapshot with its groups and tabs.
func CreateSnapshot(db *sql.DB, name, profile string, groups []SnapshotGroup, tabs []SnapshotTab) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO snapshots (name, profile, tab_count) VALUES (?, ?, ?)`,
		name, profile, len(tabs),
	)
	if err != nil {
		return fmt.Errorf("snapshot %q already exists", name)
	}
	snapID, _ := res.LastInsertId()

	// Insert groups and track their DB IDs
	groupIDs := make([]int64, len(groups))
	for i, g := range groups {
		res, err := tx.Exec(
			`INSERT INTO snapshot_groups (snapshot_id, firefox_id, name, color) VALUES (?, ?, ?, ?)`,
			snapID, g.FirefoxID, g.Name, g.Color,
		)
		if err != nil {
			return fmt.Errorf("insert group %q: %w", g.Name, err)
		}
		groupIDs[i], _ = res.LastInsertId()
	}

	// Insert tabs
	for _, tab := range tabs {
		var groupID *int64
		if tab.GroupIndex != nil && *tab.GroupIndex < len(groupIDs) {
			groupID = &groupIDs[*tab.GroupIndex]
		}
		_, err := tx.Exec(
			`INSERT INTO snapshot_tabs (snapshot_id, group_id, url, title, pinned) VALUES (?, ?, ?, ?, ?)`,
			snapID, groupID, tab.URL, tab.Title, tab.Pinned,
		)
		if err != nil {
			return fmt.Errorf("insert tab %q: %w", tab.URL, err)
		}
	}

	return tx.Commit()
}

// ListSnapshots returns all snapshots ordered by creation time (newest first).
func ListSnapshots(db *sql.DB) ([]SnapshotSummary, error) {
	rows, err := db.Query(`
		SELECT id, name, profile, created_at, tab_count
		FROM snapshots ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var snaps []SnapshotSummary
	for rows.Next() {
		var s SnapshotSummary
		if err := rows.Scan(&s.ID, &s.Name, &s.Profile, &s.CreatedAt, &s.TabCount); err != nil {
			return nil, err
		}
		snaps = append(snaps, s)
	}
	return snaps, rows.Err()
}

// GetSnapshot returns a full snapshot by name.
func GetSnapshot(db *sql.DB, name string) (*SnapshotFull, error) {
	snap := &SnapshotFull{}
	err := db.QueryRow(`
		SELECT id, name, profile, created_at, tab_count
		FROM snapshots WHERE name = ?
	`, name).Scan(&snap.ID, &snap.Name, &snap.Profile, &snap.CreatedAt, &snap.TabCount)
	if err != nil {
		return nil, fmt.Errorf("snapshot %q not found", name)
	}

	// Load groups
	rows, err := db.Query(`
		SELECT id, firefox_id, name, color
		FROM snapshot_groups WHERE snapshot_id = ?
	`, snap.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groupNames := make(map[int64]string) // group DB ID -> name
	for rows.Next() {
		var g SnapshotGroup
		if err := rows.Scan(&g.ID, &g.FirefoxID, &g.Name, &g.Color); err != nil {
			return nil, err
		}
		groupNames[g.ID] = g.Name
		snap.Groups = append(snap.Groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Load tabs
	tabRows, err := db.Query(`
		SELECT url, title, group_id, pinned
		FROM snapshot_tabs WHERE snapshot_id = ?
	`, snap.ID)
	if err != nil {
		return nil, err
	}
	defer tabRows.Close()

	for tabRows.Next() {
		var tab SnapshotTab
		var groupID *int64
		if err := tabRows.Scan(&tab.URL, &tab.Title, &groupID, &tab.Pinned); err != nil {
			return nil, err
		}
		if groupID != nil {
			tab.GroupName = groupNames[*groupID]
		}
		snap.Tabs = append(snap.Tabs, tab)
	}
	return snap, tabRows.Err()
}

// DeleteSnapshot removes a snapshot by name (cascades to groups and tabs).
func DeleteSnapshot(db *sql.DB, name string) error {
	res, err := db.Exec(`DELETE FROM snapshots WHERE name = ?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("snapshot %q not found", name)
	}
	return nil
}
```

**Step 4: Run tests to verify they pass**

Run: `GOMAXPROCS=1 go test -p 1 -run 'TestCreate|TestGet|TestDelete' ./internal/storage/ -v`
Expected: PASS

---

### Task 4: Snapshot Create — Orchestration from SessionData

**Files:**
- Create: `internal/snapshot/snapshot.go`
- Create: `internal/snapshot/snapshot_test.go`

**Step 1: Write the failing test**

```go
// internal/snapshot/snapshot_test.go
package snapshot

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/storage"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

func testDB(t *testing.T) *storage.DB {
	t.Helper()
	// We reuse storage.OpenDB but wrap it for convenience
	db, err := storage.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestCreateFromSession(t *testing.T) {
	db := testDB(t)

	tab1 := &types.Tab{URL: "https://github.com/org/repo", Title: "Repo", GroupID: "1", Pinned: false}
	tab2 := &types.Tab{URL: "https://google.com", Title: "Google", GroupID: "", Pinned: true}

	session := &types.SessionData{
		Groups: []*types.TabGroup{
			{ID: "1", Name: "Work", Color: "blue", Tabs: []*types.Tab{tab1}},
			{ID: "", Name: "Ungrouped", Tabs: []*types.Tab{tab2}},
		},
		AllTabs:  []*types.Tab{tab1, tab2},
		Profile:  types.Profile{Name: "default"},
		ParsedAt: time.Now(),
	}

	err := Create(db, "test-snap", session)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	snap, err := storage.GetSnapshot(db, "test-snap")
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if snap.TabCount != 2 {
		t.Errorf("tab count: got %d, want 2", snap.TabCount)
	}
	if len(snap.Groups) != 1 {
		t.Errorf("groups: got %d, want 1 (Ungrouped is skipped)", len(snap.Groups))
	}
}
```

Note: This test reveals a design choice — the `snapshot.Create` function converts `SessionData` into the storage types. The "Ungrouped" virtual group (empty ID) is not stored as a group; ungrouped tabs have `group_id = NULL`.

**Step 2: Run test to verify it fails**

Run: `GOMAXPROCS=1 go test -p 1 -run TestCreateFromSession ./internal/snapshot/ -v`
Expected: FAIL — package doesn't exist

**Step 3: Write the implementation**

```go
// internal/snapshot/snapshot.go
package snapshot

import (
	"database/sql"
	"fmt"

	"github.com/nickel-chromium/tabsordnung/internal/storage"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// Create saves the current session as a named snapshot.
func Create(db *sql.DB, name string, session *types.SessionData) error {
	// Convert groups (skip the virtual "Ungrouped" group)
	var groups []storage.SnapshotGroup
	groupIndex := make(map[string]int) // GroupID -> index in groups slice
	for _, g := range session.Groups {
		if g.ID == "" {
			continue // skip Ungrouped virtual group
		}
		groupIndex[g.ID] = len(groups)
		groups = append(groups, storage.SnapshotGroup{
			FirefoxID: g.ID,
			Name:      g.Name,
			Color:     g.Color,
		})
	}

	// Convert tabs
	var tabs []storage.SnapshotTab
	for _, tab := range session.AllTabs {
		st := storage.SnapshotTab{
			URL:    tab.URL,
			Title:  tab.Title,
			Pinned: tab.Pinned,
		}
		if idx, ok := groupIndex[tab.GroupID]; ok {
			st.GroupIndex = &idx
		}
		tabs = append(tabs, st)
	}

	if err := storage.CreateSnapshot(db, name, session.Profile.Name, groups, tabs); err != nil {
		return err
	}

	fmt.Printf("Snapshot %q created: %d tabs in %d groups\n", name, len(tabs), len(groups))
	return nil
}
```

Note: `types.Tab` does not currently have a `Pinned` field. We need to add it.

**Step 3b: Add Pinned field to types.Tab**

Add to `internal/types/types.go` line 14, after `BrowserID`:

```go
Pinned      bool
```

**Step 4: Run test to verify it passes**

Run: `GOMAXPROCS=1 go test -p 1 -run TestCreateFromSession ./internal/snapshot/ -v`
Expected: PASS

---

### Task 5: Snapshot Diff

**Files:**
- Create: `internal/snapshot/diff.go`
- Create: `internal/snapshot/diff_test.go`

**Step 1: Write the failing test**

```go
// internal/snapshot/diff_test.go
package snapshot

import (
	"testing"
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/storage"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

func TestDiff(t *testing.T) {
	db := testDB(t)

	// Create a snapshot with 3 tabs
	session := &types.SessionData{
		AllTabs: []*types.Tab{
			{URL: "https://kept.com", Title: "Kept"},
			{URL: "https://removed.com", Title: "Removed"},
			{URL: "https://moved.com", Title: "Moved", GroupID: "1"},
		},
		Groups: []*types.TabGroup{
			{ID: "1", Name: "Work", Tabs: []*types.Tab{{URL: "https://moved.com"}}},
		},
		Profile: types.Profile{Name: "default"},
	}
	if err := Create(db, "base", session); err != nil {
		t.Fatal(err)
	}

	// Current state: kept.com still there, removed.com gone, added.com new, moved.com in different group
	current := &types.SessionData{
		AllTabs: []*types.Tab{
			{URL: "https://kept.com", Title: "Kept"},
			{URL: "https://added.com", Title: "Added"},
			{URL: "https://moved.com", Title: "Moved", GroupID: "2"},
		},
		Groups: []*types.TabGroup{
			{ID: "2", Name: "Personal", Tabs: []*types.Tab{{URL: "https://moved.com"}}},
		},
		ParsedAt: time.Now(),
	}

	result, err := Diff(db, "base", current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	if len(result.Added) != 1 || result.Added[0].URL != "https://added.com" {
		t.Errorf("added: got %v", result.Added)
	}
	if len(result.Removed) != 1 || result.Removed[0].URL != "https://removed.com" {
		t.Errorf("removed: got %v", result.Removed)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `GOMAXPROCS=1 go test -p 1 -run TestDiff ./internal/snapshot/ -v`
Expected: FAIL — Diff not defined

**Step 3: Write the implementation**

```go
// internal/snapshot/diff.go
package snapshot

import (
	"database/sql"
	"fmt"

	"github.com/nickel-chromium/tabsordnung/internal/storage"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// DiffEntry represents a tab in a diff result.
type DiffEntry struct {
	URL   string
	Title string
	Group string // group name, or empty
}

// DiffResult holds the comparison between a snapshot and current state.
type DiffResult struct {
	SnapshotName string
	Added        []DiffEntry // in current but not in snapshot
	Removed      []DiffEntry // in snapshot but not in current
}

// Diff compares a named snapshot against the current session.
func Diff(db *sql.DB, snapshotName string, current *types.SessionData) (*DiffResult, error) {
	snap, err := storage.GetSnapshot(db, snapshotName)
	if err != nil {
		return nil, err
	}

	// Build URL sets
	snapURLs := make(map[string]storage.SnapshotTab)
	for _, tab := range snap.Tabs {
		snapURLs[tab.URL] = tab
	}

	currentURLs := make(map[string]*types.Tab)
	for _, tab := range current.AllTabs {
		currentURLs[tab.URL] = tab
	}

	// Build group name lookup for current session
	groupNames := make(map[string]string)
	for _, g := range current.Groups {
		groupNames[g.ID] = g.Name
	}

	result := &DiffResult{SnapshotName: snapshotName}

	// Added: in current but not in snapshot
	for url, tab := range currentURLs {
		if _, inSnap := snapURLs[url]; !inSnap {
			result.Added = append(result.Added, DiffEntry{
				URL:   url,
				Title: tab.Title,
				Group: groupNames[tab.GroupID],
			})
		}
	}

	// Removed: in snapshot but not in current
	for url, tab := range snapURLs {
		if _, inCurrent := currentURLs[url]; !inCurrent {
			result.Removed = append(result.Removed, DiffEntry{
				URL:   url,
				Title: tab.Title,
				Group: tab.GroupName,
			})
		}
	}

	return result, nil
}

// FormatDiff returns a human-readable diff string.
func FormatDiff(d *DiffResult) string {
	var s string
	s += fmt.Sprintf("Compared against snapshot %q:\n", d.SnapshotName)
	s += fmt.Sprintf("  + %d tabs added\n", len(d.Added))
	s += fmt.Sprintf("  - %d tabs removed\n\n", len(d.Removed))

	if len(d.Added) > 0 {
		s += "Added:\n"
		for _, e := range d.Added {
			group := e.Group
			if group == "" {
				group = "Ungrouped"
			}
			s += fmt.Sprintf("  %s  (%s)\n", e.URL, group)
		}
		s += "\n"
	}

	if len(d.Removed) > 0 {
		s += "Removed:\n"
		for _, e := range d.Removed {
			group := e.Group
			if group == "" {
				group = "Ungrouped"
			}
			s += fmt.Sprintf("  %s  (%s)\n", e.URL, group)
		}
	}

	return s
}
```

**Step 4: Run test to verify it passes**

Run: `GOMAXPROCS=1 go test -p 1 -run TestDiff ./internal/snapshot/ -v`
Expected: PASS

---

### Task 6: Subcommand Routing + Snapshot CLI Handlers

**Files:**
- Modify: `main.go`

This task refactors `main.go` to support subcommands. The approach: check `os.Args` for a subcommand before `flag.Parse()`.

**Step 1: Write the implementation**

Replace `main.go` with the following structure. Key change: before `flag.Parse()`, check if `os.Args[1]` is `"snapshot"` or `"triage"`, and if so, route to the subcommand handler instead of the default TUI/export flow.

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nickel-chromium/tabsordnung/internal/firefox"
	"github.com/nickel-chromium/tabsordnung/internal/server"
	"github.com/nickel-chromium/tabsordnung/internal/snapshot"
	"github.com/nickel-chromium/tabsordnung/internal/storage"
	"github.com/nickel-chromium/tabsordnung/internal/export"
	"github.com/nickel-chromium/tabsordnung/internal/tui"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

func main() {
	// Check for subcommands before flag.Parse()
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "snapshot":
			runSnapshot(os.Args[2:])
			return
		case "triage":
			runTriage(os.Args[2:])
			return
		}
	}

	// Original flag-based flow for TUI/export modes
	profileName := flag.String("profile", "", "Firefox profile name (skip picker)")
	staleDays := flag.Int("stale-days", 7, "Days before a tab is considered stale")
	liveMode := flag.Bool("live", false, "Start in live mode (connect to extension)")
	port := flag.Int("port", 19191, "WebSocket port for live mode")
	exportMode := flag.Bool("export", false, "Export tabs and exit")
	exportFormat := flag.String("format", "markdown", "Export format: markdown or json")
	outFile := flag.String("out", "", "Output file path (default: stdout)")
	listProfiles := flag.Bool("list-profiles", false, "List Firefox profiles and exit")
	flag.Parse()

	profiles, err := firefox.DiscoverProfiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error discovering Firefox profiles: %v\n", err)
		os.Exit(1)
	}
	if len(profiles) == 0 {
		fmt.Fprintln(os.Stderr, "No Firefox profiles found.")
		os.Exit(1)
	}

	if *listProfiles {
		for _, p := range profiles {
			suffix := ""
			if p.IsDefault {
				suffix = " [default]"
			}
			fmt.Printf("%s (%s)%s\n", p.Name, p.Path, suffix)
		}
		return
	}

	if *profileName != "" {
		var filtered []types.Profile
		for _, p := range profiles {
			if p.Name == *profileName {
				filtered = append(filtered, p)
				break
			}
		}
		if len(filtered) == 0 {
			fmt.Fprintf(os.Stderr, "Profile %q not found. Available profiles:\n", *profileName)
			for _, p := range profiles {
				fmt.Fprintf(os.Stderr, "  - %s\n", p.Name)
			}
			os.Exit(1)
		}
		profiles = filtered
	}

	if *exportMode {
		var data *types.SessionData
		if *liveMode {
			data, err = exportLive(*port)
		} else {
			profile := profiles[0]
			data, err = firefox.ReadSessionFile(profile.Path)
			if err == nil {
				data.Profile = profile
			}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		var output string
		switch *exportFormat {
		case "json":
			output, err = export.JSON(data)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error generating JSON: %v\n", err)
				os.Exit(1)
			}
		case "markdown", "md":
			output = export.Markdown(data)
		default:
			fmt.Fprintf(os.Stderr, "Unknown format %q. Use 'markdown' or 'json'.\n", *exportFormat)
			os.Exit(1)
		}
		if *outFile != "" {
			if err := os.WriteFile(*outFile, []byte(output), 0644); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing file: %v\n", err)
				os.Exit(1)
			}
		} else {
			fmt.Print(output)
		}
		return
	}

	srv := server.New(*port)
	model := tui.NewModel(profiles, *staleDays, *liveMode, srv)
	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func exportLive(port int) (*types.SessionData, error) {
	srv := server.New(port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.ListenAndServe(ctx)
	fmt.Fprintf(os.Stderr, "Waiting for Firefox extension on port %d...\n", port)
	timeout := time.After(10 * time.Second)
	for {
		select {
		case msg := <-srv.Messages():
			if msg.Type == "snapshot" {
				return server.ParseSnapshot(msg)
			}
		case <-timeout:
			return nil, fmt.Errorf("timed out waiting for extension (10s)")
		}
	}
}

func runSnapshot(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: tabsordnung snapshot <create|list|restore|diff|delete> [args]")
		os.Exit(1)
	}

	dbPath, err := storage.DefaultDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	sub := args[0]
	rest := args[1:]

	switch sub {
	case "create":
		fs := flag.NewFlagSet("snapshot create", flag.ExitOnError)
		profileName := fs.String("profile", "", "Firefox profile name")
		fs.Parse(rest)
		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "Usage: tabsordnung snapshot create <name> [--profile name]")
			os.Exit(1)
		}
		name := fs.Arg(0)

		session, err := resolveSession(*profileName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		db, err := storage.OpenDB(dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()

		if err := snapshot.Create(db, name, session); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "list":
		db, err := storage.OpenDB(dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()

		snaps, err := storage.ListSnapshots(db)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if len(snaps) == 0 {
			fmt.Println("No snapshots.")
			return
		}
		fmt.Printf("%-40s %5s %8s  %s\n", "NAME", "TABS", "PROFILE", "CREATED")
		for _, s := range snaps {
			fmt.Printf("%-40s %5d %8s  %s\n", s.Name, s.TabCount, s.Profile, s.CreatedAt.Format("2006-01-02 15:04"))
		}

	case "diff":
		fs := flag.NewFlagSet("snapshot diff", flag.ExitOnError)
		profileName := fs.String("profile", "", "Firefox profile name")
		fs.Parse(rest)
		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "Usage: tabsordnung snapshot diff <name> [--profile name]")
			os.Exit(1)
		}
		name := fs.Arg(0)

		session, err := resolveSession(*profileName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		db, err := storage.OpenDB(dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()

		result, err := snapshot.Diff(db, name, session)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(snapshot.FormatDiff(result))

	case "delete":
		fs := flag.NewFlagSet("snapshot delete", flag.ExitOnError)
		yes := fs.Bool("yes", false, "Skip confirmation")
		fs.Parse(rest)
		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "Usage: tabsordnung snapshot delete <name> [--yes]")
			os.Exit(1)
		}
		name := fs.Arg(0)

		if !*yes {
			fmt.Printf("Delete snapshot %q? [y/N] ", name)
			var answer string
			fmt.Scanln(&answer)
			if answer != "y" && answer != "Y" {
				fmt.Println("Cancelled.")
				return
			}
		}

		db, err := storage.OpenDB(dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()

		if err := storage.DeleteSnapshot(db, name); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Snapshot %q deleted.\n", name)

	case "restore":
		fs := flag.NewFlagSet("snapshot restore", flag.ExitOnError)
		port := fs.Int("port", 19191, "WebSocket port for live mode")
		fs.Parse(rest)
		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "Usage: tabsordnung snapshot restore <name> [--port N]")
			os.Exit(1)
		}
		name := fs.Arg(0)

		db, err := storage.OpenDB(dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()

		if err := snapshot.Restore(db, name, *port); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown snapshot command: %s\n", sub)
		fmt.Fprintln(os.Stderr, "Usage: tabsordnung snapshot <create|list|restore|diff|delete> [args]")
		os.Exit(1)
	}
}

// resolveSession reads a Firefox session, using the given profile name or the default.
func resolveSession(profileName string) (*types.SessionData, error) {
	profiles, err := firefox.DiscoverProfiles()
	if err != nil {
		return nil, fmt.Errorf("discover profiles: %w", err)
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("no Firefox profiles found")
	}

	var profile types.Profile
	if profileName != "" {
		for _, p := range profiles {
			if p.Name == profileName {
				profile = p
				break
			}
		}
		if profile.Path == "" {
			return nil, fmt.Errorf("profile %q not found", profileName)
		}
	} else {
		// Use first profile (or default)
		for _, p := range profiles {
			if p.IsDefault {
				profile = p
				break
			}
		}
		if profile.Path == "" {
			profile = profiles[0]
		}
	}

	session, err := firefox.ReadSessionFile(profile.Path)
	if err != nil {
		return nil, err
	}
	session.Profile = profile
	return session, nil
}

func runTriage(args []string) {
	// Implemented in Task 10
	fmt.Fprintln(os.Stderr, "triage: not yet implemented")
	os.Exit(1)
}
```

**Step 2: Verify it compiles**

Run: `GOMAXPROCS=1 go build -p 1 -o /dev/null .`
Expected: compiles (snapshot.Restore not yet defined — will cause error). Add a placeholder.

**Step 2b: Add Restore placeholder**

```go
// Append to internal/snapshot/snapshot.go

// Restore reopens tabs from a snapshot via the live mode WebSocket bridge.
func Restore(db *sql.DB, name string, port int) error {
	return fmt.Errorf("restore not yet implemented")
}
```

**Step 3: Verify build**

Run: `GOMAXPROCS=1 go build -p 1 -o /dev/null .`
Expected: compiles successfully

**Step 4: Run all existing tests to ensure no regressions**

Run: `GOMAXPROCS=1 go test -p 1 ./... -v`
Expected: all existing tests PASS

---

### Task 7: WebSocket Protocol Extensions

**Files:**
- Modify: `internal/server/server.go` (add fields to OutgoingMsg)
- Modify: `extension/background.js` (handle new actions)

**Step 1: Extend OutgoingMsg**

In `internal/server/server.go`, replace the `OutgoingMsg` struct:

```go
// OutgoingMsg is a command from the TUI to the extension.
type OutgoingMsg struct {
	ID      string      `json:"id"`
	Action  string      `json:"action"`
	TabID   int         `json:"tabId,omitempty"`
	TabIDs  []int       `json:"tabIds,omitempty"`
	GroupID int         `json:"groupId,omitempty"`
	Tabs    []TabToOpen `json:"tabs,omitempty"`
	Name    string      `json:"name,omitempty"`
	Color   string      `json:"color,omitempty"`
}

// TabToOpen specifies a tab to create in the browser.
type TabToOpen struct {
	URL    string `json:"url"`
	Pinned bool   `json:"pinned,omitempty"`
}
```

**Step 2: Add IncomingMsg fields for group creation response**

In `internal/server/server.go`, add to `IncomingMsg`:

```go
GroupID int `json:"groupId,omitempty"` // response from create-group
```

Wait — `GroupID` already exists on `OutgoingMsg`. For `IncomingMsg` responses, the existing `OK` and `Error` fields are sufficient. For `create-group`, we need the new group's ID in the response. Add a `Data` field:

Actually, keep it simple. The extension response for `create-group` will return the new group ID in a `groupId` field on the response message. The `IncomingMsg` already doesn't have `GroupID` — let's add it:

Add to `IncomingMsg` struct (after the Error field):

```go
GroupID int `json:"groupId,omitempty"` // returned by create-group
```

**Step 3: Extend background.js**

Add two new cases to the `handleCommand` switch in `extension/background.js`:

```javascript
      case "open":
        for (const tab of (msg.tabs || [])) {
          await browser.tabs.create({
            url: tab.url,
            pinned: tab.pinned || false,
          });
        }
        break;
      case "create-group":
        if (browser.tabs.group) {
          // Chrome-style tab groups API
          const groupId = await chrome.tabs.group({ tabIds: [] });
          await chrome.tabGroups.update(groupId, {
            title: msg.name || "",
            color: msg.color || "blue",
          });
          send({ id: msg.id, ok: true, groupId });
          return;
        }
        // Firefox doesn't have native tab groups API yet — skip silently
        send({ id: msg.id, ok: true, groupId: -1 });
        return;
```

**Step 4: Verify build**

Run: `GOMAXPROCS=1 go build -p 1 -o /dev/null .`
Expected: compiles

**Step 5: Run server tests**

Run: `GOMAXPROCS=1 go test -p 1 ./internal/server/ -v`
Expected: PASS (existing tests unaffected)

---

### Task 8: Snapshot Restore via Live Mode

**Files:**
- Modify: `internal/snapshot/snapshot.go` (replace Restore placeholder)

**Step 1: Write the implementation**

Replace the `Restore` placeholder in `internal/snapshot/snapshot.go`:

```go
import (
	"context"
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/server"
	"github.com/nickel-chromium/tabsordnung/internal/storage"
)

// Restore reopens tabs from a snapshot via the live mode WebSocket bridge.
func Restore(db *sql.DB, name string, port int) error {
	snap, err := storage.GetSnapshot(db, name)
	if err != nil {
		return err
	}

	// Start server and wait for extension connection
	srv := server.New(port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.ListenAndServe(ctx)
	fmt.Fprintf(os.Stderr, "Waiting for Firefox extension on port %d...\n", port)

	// Wait for initial snapshot (confirms extension is connected)
	timeout := time.After(10 * time.Second)
	select {
	case msg := <-srv.Messages():
		if msg.Type != "snapshot" {
			return fmt.Errorf("expected snapshot message, got %q", msg.Type)
		}
	case <-timeout:
		return fmt.Errorf("timed out waiting for extension (10s)")
	}

	// Create groups first
	groupMap := make(map[string]int) // snapshot group name -> browser group ID
	for _, g := range snap.Groups {
		err := srv.Send(server.OutgoingMsg{
			ID:     fmt.Sprintf("create-group-%s", g.Name),
			Action: "create-group",
			Name:   g.Name,
			Color:  g.Color,
		})
		if err != nil {
			return fmt.Errorf("create group %q: %w", g.Name, err)
		}

		// Wait for response with group ID
		respTimeout := time.After(5 * time.Second)
		select {
		case resp := <-srv.Messages():
			if resp.OK != nil && *resp.OK {
				groupMap[g.Name] = resp.GroupID
			}
		case <-respTimeout:
			fmt.Fprintf(os.Stderr, "Warning: timed out creating group %q\n", g.Name)
		}
	}

	// Open tabs in batches
	var tabs []server.TabToOpen
	for _, tab := range snap.Tabs {
		tabs = append(tabs, server.TabToOpen{
			URL:    tab.URL,
			Pinned: tab.Pinned,
		})
	}

	err = srv.Send(server.OutgoingMsg{
		ID:     "restore-tabs",
		Action: "open",
		Tabs:   tabs,
	})
	if err != nil {
		return fmt.Errorf("open tabs: %w", err)
	}

	// Wait for confirmation
	respTimeout := time.After(30 * time.Second)
	select {
	case resp := <-srv.Messages():
		if resp.OK != nil && !*resp.OK {
			return fmt.Errorf("restore failed: %s", resp.Error)
		}
	case <-respTimeout:
		return fmt.Errorf("timed out waiting for restore confirmation")
	}

	// Move tabs into groups if we got group IDs
	// Note: tabs opened via "open" action don't automatically get assigned to groups.
	// The extension would need to track the newly created tab IDs and group them.
	// For V1, tabs are opened ungrouped — group assignment is a future enhancement.

	fmt.Printf("Restored %d tabs from snapshot %q\n", len(snap.Tabs), name)
	return nil
}
```

**Step 2: Verify build**

Run: `GOMAXPROCS=1 go build -p 1 -o /dev/null .`
Expected: compiles

Note: Restore is hard to unit test without a real WebSocket connection. Integration testing would be done manually with Firefox.

---

### Task 9: Enhanced GitHub Analyzer for Triage

**Files:**
- Modify: `internal/analyzer/github.go`
- Modify: `internal/analyzer/github_test.go`

**Step 1: Add new types for triage data**

Add to `internal/types/types.go`:

```go
// GitHubTriageInfo holds extended GitHub metadata for triage classification.
type GitHubTriageInfo struct {
	ReviewRequested bool      // current user is a requested reviewer
	Assigned        bool      // current user is an assignee
	UpdatedAt       time.Time // last update time on GitHub
}
```

And add field to `Tab`:

```go
GitHubTriage *GitHubTriageInfo // populated by triage analyzer; nil if not a GitHub URL
```

**Step 2: Write failing test for enhanced query**

Append to `internal/analyzer/github_test.go`:

```go
func TestBuildTriageGraphQLQuery(t *testing.T) {
	refs := []*githubRef{
		{Owner: "org", Repo: "repo", Kind: "issue", Number: 42},
		{Owner: "org", Repo: "repo", Kind: "pr", Number: 99},
	}

	query, _ := buildTriageGraphQLQuery(refs)

	// Should include triage-specific fields
	if !containsAll(query, "assignees", "updatedAt") {
		t.Errorf("query missing triage fields: %s", query)
	}
	// PR should include reviewRequests
	if !containsAll(query, "reviewRequests") {
		t.Errorf("query missing reviewRequests: %s", query)
	}
}
```

**Step 3: Run test to verify it fails**

Run: `GOMAXPROCS=1 go test -p 1 -run TestBuildTriageGraphQLQuery ./internal/analyzer/ -v`
Expected: FAIL — function not defined

**Step 4: Implement the enhanced query builder and analyzer**

Add to `internal/analyzer/github.go`:

```go
// buildTriageGraphQLQuery constructs a query with extended fields for triage.
func buildTriageGraphQLQuery(refs []*githubRef) (string, map[string]*githubRef) {
	aliasMap := make(map[string]*githubRef)

	type repoGroup struct {
		owner string
		repo  string
		refs  []*githubRef
	}
	repoGroups := make(map[string]*repoGroup)
	var repoOrder []string
	for _, ref := range refs {
		key := repoKey(ref.Owner, ref.Repo)
		if _, ok := repoGroups[key]; !ok {
			repoGroups[key] = &repoGroup{owner: ref.Owner, repo: ref.Repo}
			repoOrder = append(repoOrder, key)
		}
		repoGroups[key].refs = append(repoGroups[key].refs, ref)
	}

	var b strings.Builder
	b.WriteString("query {")

	for ri, key := range repoOrder {
		rg := repoGroups[key]
		repoAlias := fmt.Sprintf("r%d", ri)
		b.WriteString(fmt.Sprintf(" %s: repository(owner: %q, name: %q) {", repoAlias, rg.owner, rg.repo))

		for ii, ref := range rg.refs {
			var itemAlias string
			if ref.Kind == "issue" {
				itemAlias = fmt.Sprintf("i%d", ii)
				b.WriteString(fmt.Sprintf(` %s: issue(number: %d) { state updatedAt assignees(first: 10) { nodes { login } } }`, itemAlias, ref.Number))
			} else {
				itemAlias = fmt.Sprintf("p%d", ii)
				b.WriteString(fmt.Sprintf(` %s: pullRequest(number: %d) { state updatedAt assignees(first: 10) { nodes { login } } reviewRequests(first: 100) { nodes { requestedReviewer { ... on User { login } } } } }`, itemAlias, ref.Number))
			}
			aliasMap[repoAlias+"."+itemAlias] = ref
		}

		b.WriteString(" }")
	}

	b.WriteString(" }")
	return b.String(), aliasMap
}

// triageItemResponse is the response shape for triage queries.
type triageItemResponse struct {
	State     string `json:"state"`
	UpdatedAt string `json:"updatedAt"`
	Assignees struct {
		Nodes []struct {
			Login string `json:"login"`
		} `json:"nodes"`
	} `json:"assignees"`
	ReviewRequests *struct {
		Nodes []struct {
			RequestedReviewer struct {
				Login string `json:"login"`
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
}

// ResolveGitHubUser returns the authenticated GitHub username.
func ResolveGitHubUser(token string) (string, error) {
	body, _ := json.Marshal(map[string]string{"query": "{ viewer { login } }"})
	req, err := http.NewRequest("POST", "https://api.github.com/graphql", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Data struct {
			Viewer struct {
				Login string `json:"login"`
			} `json:"viewer"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.Data.Viewer.Login == "" {
		return "", fmt.Errorf("could not resolve GitHub username")
	}
	return result.Data.Viewer.Login, nil
}

// AnalyzeGitHubTriage runs the extended GitHub analysis for triage classification.
// It populates both GitHubStatus and GitHubTriage on matching tabs.
func AnalyzeGitHubTriage(tabs []*types.Tab, username string) {
	var refs []*githubRef
	for _, tab := range tabs {
		ref := parseGitHubURL(tab.URL)
		if ref == nil {
			continue
		}
		ref.Tab = tab
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return
	}

	token := resolveGitHubToken()
	if token == "" {
		return
	}

	query, aliasMap := buildTriageGraphQLQuery(refs)

	body, _ := json.Marshal(map[string]string{"query": query})
	req, err := http.NewRequest("POST", "https://api.github.com/graphql", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var gqlResp graphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&gqlResp); err != nil {
		return
	}

	for repoAlias, repoRaw := range gqlResp.Data {
		var items map[string]json.RawMessage
		if err := json.Unmarshal(repoRaw, &items); err != nil {
			continue
		}
		for itemAlias, itemRaw := range items {
			fullAlias := repoAlias + "." + itemAlias
			ref, ok := aliasMap[fullAlias]
			if !ok {
				continue
			}
			var item triageItemResponse
			if err := json.Unmarshal(itemRaw, &item); err != nil {
				continue
			}

			ref.Tab.GitHubStatus = strings.ToLower(item.State)

			triage := &types.GitHubTriageInfo{}
			if t, err := time.Parse(time.RFC3339, item.UpdatedAt); err == nil {
				triage.UpdatedAt = t
			}

			// Check if user is an assignee
			for _, a := range item.Assignees.Nodes {
				if strings.EqualFold(a.Login, username) {
					triage.Assigned = true
					break
				}
			}

			// Check if user is a requested reviewer (PRs only)
			if item.ReviewRequests != nil {
				for _, rr := range item.ReviewRequests.Nodes {
					if strings.EqualFold(rr.RequestedReviewer.Login, username) {
						triage.ReviewRequested = true
						break
					}
				}
			}

			ref.Tab.GitHubTriage = triage
		}
	}
}

// ExportedResolveGitHubToken wraps the unexported function for use by triage.
func ResolveGitHubToken() string {
	return resolveGitHubToken()
}
```

**Step 5: Run test to verify it passes**

Run: `GOMAXPROCS=1 go test -p 1 -run TestBuildTriageGraphQLQuery ./internal/analyzer/ -v`
Expected: PASS

**Step 6: Run all analyzer tests**

Run: `GOMAXPROCS=1 go test -p 1 ./internal/analyzer/ -v`
Expected: all PASS

---

### Task 10: Triage Classification Package

**Files:**
- Create: `internal/triage/triage.go`
- Create: `internal/triage/triage_test.go`

**Step 1: Write the failing test**

```go
// internal/triage/triage_test.go
package triage

import (
	"testing"
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/types"
)

func TestClassify(t *testing.T) {
	now := time.Now()

	tabs := []*types.Tab{
		// Needs attention: review requested
		{
			URL: "https://github.com/org/repo/pull/1", Title: "PR1",
			GitHubStatus: "open",
			GitHubTriage: &types.GitHubTriageInfo{ReviewRequested: true},
		},
		// Needs attention: assigned + new activity
		{
			URL: "https://github.com/org/repo/issues/2", Title: "Issue2",
			GitHubStatus: "open",
			LastAccessed: now.Add(-48 * time.Hour),
			GitHubTriage: &types.GitHubTriageInfo{Assigned: true, UpdatedAt: now.Add(-1 * time.Hour)},
		},
		// Needs attention: new activity since last access (not assigned, not reviewer)
		{
			URL: "https://github.com/org/repo/pull/3", Title: "PR3",
			GitHubStatus: "open",
			LastAccessed: now.Add(-24 * time.Hour),
			GitHubTriage: &types.GitHubTriageInfo{UpdatedAt: now.Add(-1 * time.Hour)},
		},
		// Open PR (no attention needed)
		{
			URL: "https://github.com/org/repo/pull/4", Title: "PR4",
			GitHubStatus: "open",
			LastAccessed: now,
			GitHubTriage: &types.GitHubTriageInfo{UpdatedAt: now.Add(-1 * time.Hour)},
		},
		// Open issue
		{
			URL: "https://github.com/org/repo/issues/5", Title: "Issue5",
			GitHubStatus: "open",
			LastAccessed: now,
			GitHubTriage: &types.GitHubTriageInfo{UpdatedAt: now.Add(-1 * time.Hour)},
		},
		// Closed issue
		{
			URL: "https://github.com/org/repo/issues/6", Title: "Issue6",
			GitHubStatus: "closed",
			GitHubTriage: &types.GitHubTriageInfo{},
		},
		// Merged PR
		{
			URL: "https://github.com/org/repo/pull/7", Title: "PR7",
			GitHubStatus: "merged",
			GitHubTriage: &types.GitHubTriageInfo{},
		},
		// Non-GitHub tab (should be skipped)
		{
			URL: "https://google.com", Title: "Google",
		},
	}

	result := Classify(tabs)

	if len(result.NeedsAttention) != 3 {
		t.Errorf("NeedsAttention: got %d, want 3", len(result.NeedsAttention))
	}
	if len(result.OpenPRs) != 1 {
		t.Errorf("OpenPRs: got %d, want 1", len(result.OpenPRs))
	}
	if len(result.OpenIssues) != 1 {
		t.Errorf("OpenIssues: got %d, want 1", len(result.OpenIssues))
	}
	if len(result.ClosedMerged) != 2 {
		t.Errorf("ClosedMerged: got %d, want 2", len(result.ClosedMerged))
	}
	if result.Skipped != 1 {
		t.Errorf("Skipped: got %d, want 1", result.Skipped)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `GOMAXPROCS=1 go test -p 1 -run TestClassify ./internal/triage/ -v`
Expected: FAIL — package doesn't exist

**Step 3: Write the implementation**

```go
// internal/triage/triage.go
package triage

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/nickel-chromium/tabsordnung/internal/types"
)

var githubURLPattern = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/(issues|pull)/(\d+)`)

// Category is a triage classification bucket.
type Category string

const (
	CatNeedsAttention Category = "Needs Attention"
	CatOpenPRs        Category = "Open PRs"
	CatOpenIssues     Category = "Open Issues"
	CatClosedMerged   Category = "Closed / Merged"
)

// Move represents a proposed tab move.
type Move struct {
	Tab      *types.Tab
	Category Category
	Reason   string // human-readable reason for classification
}

// Result holds the triage classification output.
type Result struct {
	NeedsAttention []*Move
	OpenPRs        []*Move
	OpenIssues     []*Move
	ClosedMerged   []*Move
	Skipped        int // non-GitHub tabs
}

// Classify assigns each GitHub tab to a triage category.
func Classify(tabs []*types.Tab) *Result {
	result := &Result{}

	for _, tab := range tabs {
		if tab.GitHubTriage == nil {
			result.Skipped++
			continue
		}

		kind := parseKind(tab.URL)

		// Priority 1: Needs Attention
		if needsAttention(tab) {
			reason := attentionReason(tab)
			result.NeedsAttention = append(result.NeedsAttention, &Move{
				Tab: tab, Category: CatNeedsAttention, Reason: reason,
			})
			continue
		}

		// Priority 2: Closed/Merged
		if tab.GitHubStatus == "closed" || tab.GitHubStatus == "merged" {
			result.ClosedMerged = append(result.ClosedMerged, &Move{
				Tab: tab, Category: CatClosedMerged, Reason: tab.GitHubStatus,
			})
			continue
		}

		// Priority 3: Open PR vs Open Issue
		if kind == "pr" {
			result.OpenPRs = append(result.OpenPRs, &Move{
				Tab: tab, Category: CatOpenPRs,
			})
		} else {
			result.OpenIssues = append(result.OpenIssues, &Move{
				Tab: tab, Category: CatOpenIssues,
			})
		}
	}

	return result
}

func needsAttention(tab *types.Tab) bool {
	if tab.GitHubStatus != "open" {
		return false
	}
	t := tab.GitHubTriage
	if t.ReviewRequested {
		return true
	}
	if t.Assigned {
		return true
	}
	// New activity since last visit
	if !t.UpdatedAt.IsZero() && t.UpdatedAt.After(tab.LastAccessed) {
		return true
	}
	return false
}

func attentionReason(tab *types.Tab) string {
	var reasons []string
	t := tab.GitHubTriage
	if t.ReviewRequested {
		reasons = append(reasons, "review requested")
	}
	if t.Assigned {
		reasons = append(reasons, "assigned")
	}
	if !t.UpdatedAt.IsZero() && t.UpdatedAt.After(tab.LastAccessed) {
		reasons = append(reasons, "new activity")
	}
	return strings.Join(reasons, ", ")
}

func parseKind(rawURL string) string {
	matches := githubURLPattern.FindStringSubmatch(rawURL)
	if matches == nil {
		return ""
	}
	if matches[3] == "pull" {
		return "pr"
	}
	return "issue"
}

// FormatDryRun returns a human-readable summary of proposed moves.
func FormatDryRun(r *Result) string {
	total := len(r.NeedsAttention) + len(r.OpenPRs) + len(r.OpenIssues) + len(r.ClosedMerged)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Proposed moves (%d GitHub tabs):\n\n", total))

	formatGroup := func(name string, moves []*Move) {
		if len(moves) == 0 {
			return
		}
		b.WriteString(fmt.Sprintf("  → %s (%d)\n", name, len(moves)))
		for _, m := range moves {
			reason := ""
			if m.Reason != "" {
				reason = fmt.Sprintf(" (%s)", m.Reason)
			}
			// Truncate URL for display
			url := m.Tab.URL
			if len(url) > 60 {
				url = url[:57] + "..."
			}
			title := m.Tab.Title
			if len(title) > 40 {
				title = title[:37] + "..."
			}
			b.WriteString(fmt.Sprintf("    %-60s %q%s\n", url, title, reason))
		}
		b.WriteString("\n")
	}

	formatGroup(string(CatNeedsAttention), r.NeedsAttention)
	formatGroup(string(CatOpenPRs), r.OpenPRs)
	formatGroup(string(CatOpenIssues), r.OpenIssues)
	formatGroup(string(CatClosedMerged), r.ClosedMerged)

	b.WriteString(fmt.Sprintf("  %d non-GitHub tabs unchanged.\n", r.Skipped))
	return b.String()
}
```

**Step 4: Run test to verify it passes**

Run: `GOMAXPROCS=1 go test -p 1 -run TestClassify ./internal/triage/ -v`
Expected: PASS

---

### Task 11: Triage CLI Handler

**Files:**
- Modify: `main.go` (replace `runTriage` placeholder)

**Step 1: Implement runTriage**

Replace the `runTriage` function in `main.go`:

```go
func runTriage(args []string) {
	fs := flag.NewFlagSet("triage", flag.ExitOnError)
	profileName := fs.String("profile", "", "Firefox profile name")
	apply := fs.Bool("apply", false, "Apply moves via live mode (skip confirmation)")
	port := fs.Int("port", 19191, "WebSocket port for live mode")
	fs.Parse(args)

	session, err := resolveSession(*profileName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Resolve GitHub token and username
	token := analyzer.ResolveGitHubToken()
	if token == "" {
		fmt.Fprintln(os.Stderr, "Error: no GitHub token available. Run 'gh auth login' or set GITHUB_TOKEN.")
		os.Exit(1)
	}
	username, err := analyzer.ResolveGitHubUser(token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving GitHub user: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Fetching GitHub status for %d tabs (as @%s)...\n", len(session.AllTabs), username)
	analyzer.AnalyzeGitHubTriage(session.AllTabs, username)

	result := triage.Classify(session.AllTabs)
	fmt.Print(triage.FormatDryRun(result))

	total := len(result.NeedsAttention) + len(result.OpenPRs) + len(result.OpenIssues) + len(result.ClosedMerged)
	if total == 0 {
		fmt.Println("No GitHub tabs to triage.")
		return
	}

	// Check if user wants to apply
	if !*apply {
		fmt.Print("Apply? [y/N] ")
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			fmt.Println("No changes applied.")
			return
		}
	}

	// Apply via live mode
	if err := triage.Apply(result, *port); err != nil {
		fmt.Fprintf(os.Stderr, "Error applying triage: %v\n", err)
		os.Exit(1)
	}
}
```

Add the `Apply` function to `internal/triage/triage.go`:

```go
import (
	"context"
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/server"
)

// Apply executes the triage moves via the live mode WebSocket bridge.
func Apply(r *Result, port int) error {
	srv := server.New(port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.ListenAndServe(ctx)
	fmt.Printf("Waiting for Firefox extension on port %d...\n", port)

	// Wait for connection
	timeout := time.After(10 * time.Second)
	select {
	case msg := <-srv.Messages():
		if msg.Type != "snapshot" {
			return fmt.Errorf("expected snapshot, got %q", msg.Type)
		}
	case <-timeout:
		return fmt.Errorf("timed out waiting for extension")
	}

	// Create the four triage groups
	categories := []struct {
		name  string
		color string
		moves []*Move
	}{
		{string(CatNeedsAttention), "red", r.NeedsAttention},
		{string(CatOpenPRs), "blue", r.OpenPRs},
		{string(CatOpenIssues), "cyan", r.OpenIssues},
		{string(CatClosedMerged), "grey", r.ClosedMerged},
	}

	for _, cat := range categories {
		if len(cat.moves) == 0 {
			continue
		}

		// Create group
		err := srv.Send(server.OutgoingMsg{
			ID:     fmt.Sprintf("create-%s", cat.name),
			Action: "create-group",
			Name:   cat.name,
			Color:  cat.color,
		})
		if err != nil {
			return fmt.Errorf("create group %q: %w", cat.name, err)
		}

		// Wait for group ID response
		var groupID int
		respTimeout := time.After(5 * time.Second)
		select {
		case resp := <-srv.Messages():
			if resp.OK != nil && *resp.OK {
				groupID = resp.GroupID
			}
		case <-respTimeout:
			fmt.Fprintf(os.Stderr, "Warning: timed out creating group %q\n", cat.name)
			continue
		}

		if groupID <= 0 {
			continue
		}

		// Move tabs into the group
		var tabIDs []int
		for _, m := range cat.moves {
			if m.Tab.BrowserID > 0 {
				tabIDs = append(tabIDs, m.Tab.BrowserID)
			}
		}
		if len(tabIDs) == 0 {
			continue
		}

		err = srv.Send(server.OutgoingMsg{
			ID:      fmt.Sprintf("move-%s", cat.name),
			Action:  "move",
			TabIDs:  tabIDs,
			GroupID: groupID,
		})
		if err != nil {
			return fmt.Errorf("move tabs to %q: %w", cat.name, err)
		}

		// Wait for confirmation
		moveTimeout := time.After(5 * time.Second)
		select {
		case <-srv.Messages():
		case <-moveTimeout:
		}

		fmt.Printf("  Moved %d tabs → %s\n", len(tabIDs), cat.name)
	}

	fmt.Println("Triage complete.")
	return nil
}
```

Add import of `"github.com/nickel-chromium/tabsordnung/internal/analyzer"` and `"github.com/nickel-chromium/tabsordnung/internal/triage"` to `main.go` imports.

**Step 2: Verify build**

Run: `GOMAXPROCS=1 go build -p 1 -o /dev/null .`
Expected: compiles

---

### Task 12: Update README

**Files:**
- Modify: `README.md`

Replace `README.md` content with:

```markdown
# Tabsordnung

Terminal UI for analyzing Firefox tabs. Reads session data directly from Firefox profile files and surfaces stale tabs, duplicates, dead links, and GitHub issue/PR status.

## How it works

Firefox stores open tabs in `recovery.jsonlz4` (active session) or `previous.jsonlz4` (last closed session) inside each profile's `sessionstore-backups/` directory. Tabsordnung decompresses these mozlz4 files, parses the session JSON, and runs analysis:

- **Stale tabs** -- not accessed within a configurable number of days
- **Duplicate tabs** -- multiple tabs with the same URL
- **Dead links** -- URLs that return HTTP errors (checked async in the background)
- **GitHub status** -- checks if GitHub issue/PR tabs are still open or closed/merged

Tabs are displayed in a collapsible tree grouped by Firefox tab groups.

## Install

```
go install github.com/nickel-chromium/tabsordnung@latest
```

## Usage

### TUI mode (default)

```
tabsordnung [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--profile` | | Firefox profile name (skips profile picker) |
| `--stale-days` | 7 | Days before a tab is considered stale |
| `--live` | false | Start in live mode (connect to extension) |
| `--port` | 19191 | WebSocket port for live mode |
| `--export` | false | Export tabs and exit (no TUI) |
| `--format` | markdown | Export format: `markdown` or `json` |
| `--out` | stdout | Output file path |
| `--list-profiles` | false | List Firefox profiles and exit |

### Snapshots

Save and restore tab sessions. Snapshots are stored in `~/.local/share/tabsordnung/tabsordnung.db`.

```
tabsordnung snapshot create <name> [--profile name]
tabsordnung snapshot list
tabsordnung snapshot restore <name> [--port N]
tabsordnung snapshot diff <name> [--profile name]
tabsordnung snapshot delete <name> [--yes]
```

`restore` requires the Firefox extension running in live mode.

### GitHub Triage

Classify GitHub tabs into groups (Needs Attention, Open PRs, Open Issues, Closed/Merged) based on issue/PR status, review requests, and assignment.

```
tabsordnung triage [--profile name] [--apply] [--port N]
```

Dry-run by default — shows proposed moves and asks for confirmation. Use `--apply` to skip confirmation (for automation). Requires `gh auth login` or `GITHUB_TOKEN` environment variable.

## Keys

| Key | Action |
|-----|--------|
| `j`/`k` | Navigate up/down |
| `h` | Collapse group or jump to parent group |
| `l` | Expand group or enter first tab |
| `Enter` | Toggle expand/collapse |
| `f` | Open filter picker |
| `r` | Reload session data |
| `p` | Switch Firefox profile |
| `q` | Quit |

## Live mode

Install the companion Firefox extension from the `extension/` directory. The extension communicates with tabsordnung over a local WebSocket connection (default port 19191). Live mode enables:

- Real-time tab synchronization
- Close, focus, and move tabs from the TUI
- Snapshot restore and triage apply

## Supported platforms

Linux and macOS. Requires Firefox profile data on disk.
```

**Step 1: Write the updated README**

Apply the content above to `README.md`.

**Step 2: Verify build one final time**

Run: `GOMAXPROCS=1 go build -p 1 -o /dev/null .`
Expected: compiles

Run: `GOMAXPROCS=1 go test -p 1 ./... -v`
Expected: all PASS

---

## Implementation Order Summary

| Task | Description | Dependencies |
|------|-------------|-------------|
| 1 | Add SQLite dependency | none |
| 2 | Storage: DB init & schema | 1 |
| 3 | Storage: Snapshot CRUD | 2 |
| 4 | Snapshot create from SessionData | 3 |
| 5 | Snapshot diff | 4 |
| 6 | Subcommand routing + CLI handlers | 4, 5 |
| 7 | WebSocket protocol extensions | none |
| 8 | Snapshot restore via live mode | 6, 7 |
| 9 | Enhanced GitHub analyzer for triage | none |
| 10 | Triage classification | 9 |
| 11 | Triage CLI handler | 6, 10, 7 |
| 12 | Update README | all |

**Parallelizable:** Tasks 7 and 9 can run in parallel with tasks 2-5.
