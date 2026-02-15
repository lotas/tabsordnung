package snapshot

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/lotas/tabsordnung/internal/storage"
	"github.com/lotas/tabsordnung/internal/types"
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
