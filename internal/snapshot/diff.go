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

// DiffResult holds the result of comparing a snapshot against the current session.
type DiffResult struct {
	SnapshotName string
	Added        []DiffEntry // in current but not in snapshot
	Removed      []DiffEntry // in snapshot but not in current
}

// Diff compares a stored snapshot against the current session data.
// Added entries are tabs present in current but not in the snapshot.
// Removed entries are tabs present in the snapshot but not in current.
// Comparison is by URL.
func Diff(db *sql.DB, snapshotName string, current *types.SessionData) (*DiffResult, error) {
	snap, err := storage.GetSnapshot(db, snapshotName)
	if err != nil {
		return nil, err
	}

	// Build URL set for snapshot tabs.
	snapshotURLs := make(map[string]DiffEntry, len(snap.Tabs))
	for _, tab := range snap.Tabs {
		snapshotURLs[tab.URL] = DiffEntry{
			URL:   tab.URL,
			Title: tab.Title,
			Group: tab.GroupName,
		}
	}

	// Build URL set for current tabs.
	// Also build a group name lookup from session groups.
	groupNames := make(map[string]string) // GroupID -> Name
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

	result := &DiffResult{
		SnapshotName: snapshotName,
	}

	// Added: in current but not in snapshot.
	for url, entry := range currentURLs {
		if _, ok := snapshotURLs[url]; !ok {
			result.Added = append(result.Added, entry)
		}
	}

	// Removed: in snapshot but not in current.
	for url, entry := range snapshotURLs {
		if _, ok := currentURLs[url]; !ok {
			result.Removed = append(result.Removed, entry)
		}
	}

	return result, nil
}

// FormatDiff returns a human-readable string representation of a DiffResult.
func FormatDiff(d *DiffResult) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Diff against snapshot %q\n", d.SnapshotName)
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
