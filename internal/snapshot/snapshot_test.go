package snapshot

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/storage"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// testDB creates a temporary SQLite database for testing.
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

func TestCreateFromSession(t *testing.T) {
	db := testDB(t)

	// Build a SessionData with one real group, one Ungrouped (empty ID), and tabs.
	realGroup := &types.TabGroup{
		ID:    "g1",
		Name:  "Work",
		Color: "blue",
	}
	ungroupedGroup := &types.TabGroup{
		ID:   "", // virtual Ungrouped group
		Name: "Ungrouped",
	}

	tab1 := &types.Tab{
		URL:     "https://example.com",
		Title:   "Example",
		GroupID: "g1",
		Pinned:  true,
	}
	tab2 := &types.Tab{
		URL:     "https://go.dev",
		Title:   "Go",
		GroupID: "g1",
		Pinned:  false,
	}
	tab3 := &types.Tab{
		URL:    "https://ungrouped.com",
		Title:  "Ungrouped Tab",
		Pinned: false,
	}

	realGroup.Tabs = []*types.Tab{tab1, tab2}
	ungroupedGroup.Tabs = []*types.Tab{tab3}

	session := &types.SessionData{
		Groups:  []*types.TabGroup{realGroup, ungroupedGroup},
		AllTabs: []*types.Tab{tab1, tab2, tab3},
		Profile: types.Profile{Name: "default"},
		ParsedAt: time.Now(),
	}

	err := Create(db, "test-snap", session)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify snapshot was stored correctly.
	snap, err := storage.GetSnapshot(db, "test-snap")
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}

	// Should have 3 tabs total.
	if snap.TabCount != 3 {
		t.Errorf("expected 3 tabs, got %d", snap.TabCount)
	}

	// Should have only 1 group (Ungrouped should NOT be stored).
	if len(snap.Groups) != 1 {
		t.Fatalf("expected 1 group (Ungrouped excluded), got %d", len(snap.Groups))
	}

	if snap.Groups[0].Name != "Work" {
		t.Errorf("expected group name 'Work', got %q", snap.Groups[0].Name)
	}
	if snap.Groups[0].FirefoxID != "g1" {
		t.Errorf("expected group firefox_id 'g1', got %q", snap.Groups[0].FirefoxID)
	}

	// Verify tabs.
	if len(snap.Tabs) != 3 {
		t.Fatalf("expected 3 tabs, got %d", len(snap.Tabs))
	}

	// Find the pinned tab and verify it.
	var pinnedFound bool
	var ungroupedFound bool
	for _, tab := range snap.Tabs {
		if tab.URL == "https://example.com" {
			pinnedFound = true
			if !tab.Pinned {
				t.Error("expected example.com tab to be pinned")
			}
			if tab.GroupName != "Work" {
				t.Errorf("expected GroupName 'Work', got %q", tab.GroupName)
			}
		}
		if tab.URL == "https://ungrouped.com" {
			ungroupedFound = true
			if tab.GroupName != "" {
				t.Errorf("expected empty GroupName for ungrouped tab, got %q", tab.GroupName)
			}
		}
	}
	if !pinnedFound {
		t.Error("pinned tab (example.com) not found in snapshot")
	}
	if !ungroupedFound {
		t.Error("ungrouped tab not found in snapshot")
	}

	// Verify profile name.
	if snap.Profile != "default" {
		t.Errorf("expected profile 'default', got %q", snap.Profile)
	}
}
