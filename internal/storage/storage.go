package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// SnapshotSummary holds the metadata for a snapshot.
type SnapshotSummary struct {
	ID        int64
	Name      string
	Profile   string
	CreatedAt time.Time
	TabCount  int
}

// SnapshotGroup represents a Firefox tab group within a snapshot.
type SnapshotGroup struct {
	ID        int64 // set after insert
	FirefoxID string
	Name      string
	Color     string
}

// SnapshotTab represents a single tab within a snapshot.
type SnapshotTab struct {
	URL        string
	Title      string
	GroupIndex *int   // index into groups slice; nil = ungrouped
	Pinned     bool
	GroupName  string // populated by GetSnapshot
}

// SnapshotFull is a snapshot with its groups and tabs.
type SnapshotFull struct {
	SnapshotSummary
	Groups []SnapshotGroup
	Tabs   []SnapshotTab
}

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

// OpenDB opens (or creates) a SQLite database at the given path.
// It creates parent directories if needed, enables foreign keys and WAL mode,
// and ensures the schema exists.
func OpenDB(path string) (*sql.DB, error) {
	// Create parent directory if needed.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create directory %s: %w", dir, err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Enable foreign keys.
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	// Enable WAL mode for better concurrency.
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}

	// Create schema.
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	return db, nil
}

// DefaultDBPath returns the default database file path:
// ~/.local/share/tabsordnung/tabsordnung.db
func DefaultDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "tabsordnung", "tabsordnung.db"), nil
}

// CreateSnapshot inserts a new snapshot with its groups and tabs in a single
// transaction. Returns an error if a snapshot with the given name already exists.
func CreateSnapshot(db *sql.DB, name, profile string, groups []SnapshotGroup, tabs []SnapshotTab) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Insert snapshot row.
	tabCount := len(tabs)
	res, err := tx.Exec(
		"INSERT INTO snapshots (name, profile, tab_count) VALUES (?, ?, ?)",
		name, profile, tabCount,
	)
	if err != nil {
		return fmt.Errorf("insert snapshot: %w", err)
	}
	snapID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("get snapshot id: %w", err)
	}

	// Insert groups and record their DB IDs (indexed by slice position).
	groupIDs := make([]int64, len(groups))
	for i, g := range groups {
		res, err := tx.Exec(
			"INSERT INTO snapshot_groups (snapshot_id, firefox_id, name, color) VALUES (?, ?, ?, ?)",
			snapID, g.FirefoxID, g.Name, g.Color,
		)
		if err != nil {
			return fmt.Errorf("insert group %q: %w", g.Name, err)
		}
		gID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("get group id: %w", err)
		}
		groupIDs[i] = gID
	}

	// Insert tabs.
	for _, tab := range tabs {
		var groupID *int64
		if tab.GroupIndex != nil {
			idx := *tab.GroupIndex
			if idx < 0 || idx >= len(groupIDs) {
				return fmt.Errorf("tab %q has invalid group index %d", tab.URL, idx)
			}
			gid := groupIDs[idx]
			groupID = &gid
		}
		_, err := tx.Exec(
			"INSERT INTO snapshot_tabs (snapshot_id, group_id, url, title, pinned) VALUES (?, ?, ?, ?, ?)",
			snapID, groupID, tab.URL, tab.Title, tab.Pinned,
		)
		if err != nil {
			return fmt.Errorf("insert tab %q: %w", tab.URL, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// ListSnapshots returns all snapshots ordered by creation time descending.
func ListSnapshots(db *sql.DB) ([]SnapshotSummary, error) {
	rows, err := db.Query(
		"SELECT id, name, profile, created_at, tab_count FROM snapshots ORDER BY created_at DESC, id DESC",
	)
	if err != nil {
		return nil, fmt.Errorf("query snapshots: %w", err)
	}
	defer rows.Close()

	var result []SnapshotSummary
	for rows.Next() {
		var s SnapshotSummary
		if err := rows.Scan(&s.ID, &s.Name, &s.Profile, &s.CreatedAt, &s.TabCount); err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}
		result = append(result, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate snapshots: %w", err)
	}
	return result, nil
}

// GetSnapshot loads a full snapshot by name, including its groups and tabs.
// Each tab's GroupName field is populated from the associated group.
func GetSnapshot(db *sql.DB, name string) (*SnapshotFull, error) {
	snap := &SnapshotFull{}

	// Load snapshot metadata.
	err := db.QueryRow(
		"SELECT id, name, profile, created_at, tab_count FROM snapshots WHERE name = ?",
		name,
	).Scan(&snap.ID, &snap.Name, &snap.Profile, &snap.CreatedAt, &snap.TabCount)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("snapshot %q not found", name)
		}
		return nil, fmt.Errorf("query snapshot: %w", err)
	}

	// Load groups.
	groupRows, err := db.Query(
		"SELECT id, firefox_id, name, color FROM snapshot_groups WHERE snapshot_id = ?",
		snap.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("query groups: %w", err)
	}
	defer groupRows.Close()

	// Map group DB ID -> group name for tab lookups.
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

	// Load tabs.
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

// DeleteSnapshot removes a snapshot by name. Groups and tabs are cascade-deleted.
// Returns an error if the snapshot does not exist.
func DeleteSnapshot(db *sql.DB, name string) error {
	res, err := db.Exec("DELETE FROM snapshots WHERE name = ?", name)
	if err != nil {
		return fmt.Errorf("delete snapshot: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("check rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("snapshot %q not found", name)
	}
	return nil
}
