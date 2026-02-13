package snapshot

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/server"
	"github.com/nickel-chromium/tabsordnung/internal/storage"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// Create converts a SessionData into storage types and persists a snapshot.
// Groups with an empty ID (the virtual "Ungrouped" group) are skipped.
func Create(db *sql.DB, name string, session *types.SessionData) error {
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

	if err := storage.CreateSnapshot(db, name, session.Profile.Name, groups, tabs); err != nil {
		return err
	}

	fmt.Printf("Snapshot %q created: %d tabs in %d groups\n", name, len(tabs), len(groups))
	return nil
}

// Restore reopens tabs from a snapshot via the live mode WebSocket bridge.
// It starts a WebSocket server, waits for the Firefox extension to connect,
// creates groups, and opens all tabs. Note: for V1, tabs are opened ungrouped
// after creation — the "open" action does not assign tabs to groups. Group
// assignment after opening is a future enhancement.
func Restore(db *sql.DB, name string, port int) error {
	snap, err := storage.GetSnapshot(db, name)
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

	fmt.Fprintf(os.Stderr, "Restored %d tabs from snapshot %q\n", len(snap.Tabs), name)
	return nil
}
