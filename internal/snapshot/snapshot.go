package snapshot

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/lotas/tabsordnung/internal/applog"
	"github.com/lotas/tabsordnung/internal/server"
	"github.com/lotas/tabsordnung/internal/storage"
	"github.com/lotas/tabsordnung/internal/types"
)

// Create converts a SessionData into storage types and persists a snapshot.
// It first checks the latest snapshot for the profile and skips saving if
// the URL sets are identical. Returns the rev number, whether a new snapshot
// was created, the diff against the previous snapshot (nil if first), and error.
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
			applog.Info("snapshot.skipped", "profile", profile, "rev", latest.Rev)
			return latest.Rev, false, nil, nil
		}
	}

	// Convert groups, skipping the virtual "Ungrouped" group (empty ID).
	var groups []storage.SnapshotGroup
	groupIndex := make(map[string]int) // GroupID -> index in groups slice

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

	// Convert tabs.
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

	// Extract GitHub entities from new snapshot tabs.
	var snapshotID int64
	if err := db.QueryRow("SELECT id FROM snapshots WHERE profile = ? AND rev = ?", profile, newRev).Scan(&snapshotID); err == nil && snapshotID > 0 {
		if n, ghErr := storage.ExtractGitHubFromSnapshot(db, snapshotID); ghErr != nil {
			applog.Error("snapshot.github.extract", ghErr)
		} else if n > 0 {
			applog.Info("snapshot.github.extract", "entities", n, "rev", newRev)
		}
		if n, bzErr := storage.ExtractBugzillaFromSnapshot(db, snapshotID); bzErr != nil {
			applog.Error("snapshot.bugzilla.extract", bzErr)
		} else if n > 0 {
			applog.Info("snapshot.bugzilla.extract", "entities", n, "rev", newRev)
		}
	}

	applog.Info("snapshot.created", "rev", newRev, "tabs", len(tabs), "profile", profile)

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

// Restore reopens tabs from a snapshot via the live mode WebSocket bridge.
func Restore(db *sql.DB, profile string, rev int, port int) error {
	applog.Info("snapshot.restore.start", "rev", rev, "profile", profile)
	snap, err := storage.GetSnapshot(db, profile, rev)
	if err != nil {
		return err
	}

	srv := server.New(port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.ListenAndServe(ctx)

	fmt.Fprintf(os.Stderr, "Waiting for Firefox extension on port %d...\n", port)

	// Wait for initial "snapshot" message from extension (confirms connection).
	select {
	case msg := <-srv.Messages():
		if msg.Type != "snapshot" {
			return fmt.Errorf("expected initial \"snapshot\" message, got %q", msg.Type)
		}
	case <-time.After(10 * time.Second):
		return fmt.Errorf("timed out waiting for extension to connect")
	}

	// Create groups first, storing the returned GroupIDs.
	groupIDs := make(map[int]int) // group slice index -> browser GroupID
	for i, g := range snap.Groups {
		msgID := fmt.Sprintf("create-group-%d", i)
		if err := srv.Send(server.OutgoingMsg{
			ID:     msgID,
			Action: "create-group",
			Name:   g.Name,
			Color:  g.Color,
		}); err != nil {
			return fmt.Errorf("send create-group for %q: %w", g.Name, err)
		}

		// Wait for response with 5s timeout.
		select {
		case resp := <-srv.Messages():
			if resp.OK != nil && !*resp.OK {
				return fmt.Errorf("create-group %q failed: %s", g.Name, resp.Error)
			}
			groupIDs[i] = resp.GroupID
		case <-time.After(5 * time.Second):
			return fmt.Errorf("timed out waiting for create-group response for %q", g.Name)
		}
	}

	// Build tabs to open.
	tabs := make([]server.TabToOpen, 0, len(snap.Tabs))
	for _, t := range snap.Tabs {
		tabs = append(tabs, server.TabToOpen{
			URL:    t.URL,
			Pinned: t.Pinned,
		})
	}

	// Send a single "open" message with all tabs.
	if err := srv.Send(server.OutgoingMsg{
		ID:     "open-tabs",
		Action: "open",
		Tabs:   tabs,
	}); err != nil {
		return fmt.Errorf("send open tabs: %w", err)
	}

	// Wait for confirmation with 30s timeout.
	select {
	case resp := <-srv.Messages():
		if resp.OK != nil && !*resp.OK {
			return fmt.Errorf("open tabs failed: %s", resp.Error)
		}
	case <-time.After(30 * time.Second):
		return fmt.Errorf("timed out waiting for open tabs confirmation")
	}

	applog.Info("snapshot.restore.done", "rev", rev, "tabs", len(snap.Tabs))
	fmt.Fprintf(os.Stderr, "Restored %d tabs from snapshot #%d\n", len(snap.Tabs), rev)
	return nil
}
