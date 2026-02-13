package snapshot

import (
	"testing"
	"time"

	"github.com/lotas/tabsordnung/internal/storage"
	"github.com/lotas/tabsordnung/internal/types"
)

func TestDiff(t *testing.T) {
	db := testDB(t)

	// Create a snapshot with 3 tabs: kept, removed, moved.
	groups := []storage.SnapshotGroup{
		{FirefoxID: "g1", Name: "Work", Color: "blue"},
	}
	tabs := []storage.SnapshotTab{
		{URL: "https://kept.com", Title: "Kept", GroupIndex: intPtr(0)},
		{URL: "https://removed.com", Title: "Removed", GroupIndex: intPtr(0)},
		{URL: "https://moved.com", Title: "Moved", GroupIndex: intPtr(0)},
	}

	err := storage.CreateSnapshot(db, "baseline", "default", groups, tabs)
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	// Build current session: kept stays, removed is gone, moved changes group, added is new.
	workGroup := &types.TabGroup{
		ID:    "g1",
		Name:  "Work",
		Color: "blue",
	}
	personalGroup := &types.TabGroup{
		ID:    "g2",
		Name:  "Personal",
		Color: "red",
	}

	tabKept := &types.Tab{
		URL:     "https://kept.com",
		Title:   "Kept",
		GroupID: "g1",
	}
	tabMoved := &types.Tab{
		URL:     "https://moved.com",
		Title:   "Moved",
		GroupID: "g2", // moved from g1 to g2
	}
	tabAdded := &types.Tab{
		URL:     "https://added.com",
		Title:   "Added",
		GroupID: "g2",
	}

	workGroup.Tabs = []*types.Tab{tabKept}
	personalGroup.Tabs = []*types.Tab{tabMoved, tabAdded}

	current := &types.SessionData{
		Groups:   []*types.TabGroup{workGroup, personalGroup},
		AllTabs:  []*types.Tab{tabKept, tabMoved, tabAdded},
		Profile:  types.Profile{Name: "default"},
		ParsedAt: time.Now(),
	}

	result, err := Diff(db, "baseline", current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	if result.SnapshotName != "baseline" {
		t.Errorf("expected SnapshotName 'baseline', got %q", result.SnapshotName)
	}

	// Added: https://added.com (in current but not in snapshot).
	if len(result.Added) != 1 {
		t.Fatalf("expected 1 added, got %d", len(result.Added))
	}
	if result.Added[0].URL != "https://added.com" {
		t.Errorf("expected added URL 'https://added.com', got %q", result.Added[0].URL)
	}
	if result.Added[0].Group != "Personal" {
		t.Errorf("expected added group 'Personal', got %q", result.Added[0].Group)
	}

	// Removed: https://removed.com (in snapshot but not in current).
	if len(result.Removed) != 1 {
		t.Fatalf("expected 1 removed, got %d", len(result.Removed))
	}
	if result.Removed[0].URL != "https://removed.com" {
		t.Errorf("expected removed URL 'https://removed.com', got %q", result.Removed[0].URL)
	}
	if result.Removed[0].Group != "Work" {
		t.Errorf("expected removed group 'Work', got %q", result.Removed[0].Group)
	}

	// Moved tab (https://moved.com) should appear in neither Added nor Removed
	// since the URL still exists in both.
	for _, e := range result.Added {
		if e.URL == "https://moved.com" {
			t.Error("moved tab should not appear in Added")
		}
	}
	for _, e := range result.Removed {
		if e.URL == "https://moved.com" {
			t.Error("moved tab should not appear in Removed")
		}
	}

	// Verify FormatDiff produces non-empty output.
	formatted := FormatDiff(result)
	if formatted == "" {
		t.Error("FormatDiff returned empty string")
	}
	if len(formatted) < 20 {
		t.Errorf("FormatDiff output suspiciously short: %q", formatted)
	}
}

func TestDiffNoChanges(t *testing.T) {
	db := testDB(t)

	// Create a snapshot with 1 tab.
	tabs := []storage.SnapshotTab{
		{URL: "https://example.com", Title: "Example"},
	}
	err := storage.CreateSnapshot(db, "same", "default", nil, tabs)
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	// Current session has the same tab.
	current := &types.SessionData{
		AllTabs:  []*types.Tab{{URL: "https://example.com", Title: "Example"}},
		Profile:  types.Profile{Name: "default"},
		ParsedAt: time.Now(),
	}

	result, err := Diff(db, "same", current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	if len(result.Added) != 0 {
		t.Errorf("expected 0 added, got %d", len(result.Added))
	}
	if len(result.Removed) != 0 {
		t.Errorf("expected 0 removed, got %d", len(result.Removed))
	}

	formatted := FormatDiff(result)
	if !contains(formatted, "No changes") {
		t.Errorf("expected 'No changes' in output, got: %q", formatted)
	}
}

func TestDiffSnapshotNotFound(t *testing.T) {
	db := testDB(t)

	current := &types.SessionData{
		AllTabs:  []*types.Tab{{URL: "https://example.com", Title: "Example"}},
		Profile:  types.Profile{Name: "default"},
		ParsedAt: time.Now(),
	}

	_, err := Diff(db, "nonexistent", current)
	if err == nil {
		t.Fatal("expected error for nonexistent snapshot, got nil")
	}
}

// intPtr returns a pointer to the given int.
func intPtr(i int) *int {
	return &i
}

// contains checks if s contains substr.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
