package snapshot

import (
	"testing"
	"time"

	"github.com/lotas/tabsordnung/internal/storage"
	"github.com/lotas/tabsordnung/internal/types"
)

func TestDiffAgainstCurrent(t *testing.T) {
	db := testDB(t)

	// Create a snapshot with 2 tabs.
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
