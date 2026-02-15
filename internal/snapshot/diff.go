package snapshot

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/lotas/tabsordnung/internal/storage"
	"github.com/lotas/tabsordnung/internal/types"
)

// DiffEntry represents a single tab in a diff result.
type DiffEntry struct {
	URL   string
	Title string
	Group string // group name, or empty if ungrouped
}

// DiffResult holds the result of comparing two tab sets.
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

// FormatDiff returns a human-readable string representation of a DiffResult.
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
