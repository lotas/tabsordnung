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
	Rev       int
	Name      string // optional label
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

// migration is a numbered schema change. Migrations are applied in order
// and tracked in the schema_migrations table so each runs exactly once.
type migration struct {
	Version     int
	Description string
	SQL         string
}

var migrations = []migration{
	{
		Version:     1,
		Description: "initial schema",
		SQL: `
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
);`,
	},
	{
		Version:     2,
		Description: "snapshot revisions: replace named snapshots with per-profile rev numbers",
		SQL: `
-- Rename old tables
ALTER TABLE snapshot_tabs RENAME TO snapshot_tabs_old;
ALTER TABLE snapshot_groups RENAME TO snapshot_groups_old;
ALTER TABLE snapshots RENAME TO snapshots_old;

-- Create new tables
CREATE TABLE snapshots (
    id          INTEGER PRIMARY KEY,
    rev         INTEGER NOT NULL,
    name        TEXT,
    profile     TEXT NOT NULL,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    tab_count   INTEGER NOT NULL,
    UNIQUE(profile, rev)
);
CREATE TABLE snapshot_groups (
    id          INTEGER PRIMARY KEY,
    snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    firefox_id  TEXT NOT NULL,
    name        TEXT NOT NULL,
    color       TEXT
);
CREATE TABLE snapshot_tabs (
    id          INTEGER PRIMARY KEY,
    snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    group_id    INTEGER REFERENCES snapshot_groups(id),
    url         TEXT NOT NULL,
    title       TEXT NOT NULL,
    pinned      BOOLEAN DEFAULT FALSE
);

-- Migrate data: assign rev numbers using ROW_NUMBER partitioned by profile
INSERT INTO snapshots (id, rev, name, profile, created_at, tab_count)
SELECT id,
       ROW_NUMBER() OVER (PARTITION BY profile ORDER BY id),
       name,
       profile,
       created_at,
       tab_count
FROM snapshots_old;

-- Copy groups and tabs (IDs and foreign keys are preserved)
INSERT INTO snapshot_groups (id, snapshot_id, firefox_id, name, color)
SELECT id, snapshot_id, firefox_id, name, color FROM snapshot_groups_old;

INSERT INTO snapshot_tabs (id, snapshot_id, group_id, url, title, pinned)
SELECT id, snapshot_id, group_id, url, title, pinned FROM snapshot_tabs_old;

-- Drop old tables
DROP TABLE snapshot_tabs_old;
DROP TABLE snapshot_groups_old;
DROP TABLE snapshots_old;`,
	},
	{
		Version:     3,
		Description: "create signals table",
		SQL: `
CREATE TABLE signals (
    id              INTEGER PRIMARY KEY,
    source          TEXT NOT NULL,
    title           TEXT NOT NULL,
    preview         TEXT DEFAULT '',
    source_ts       TEXT NOT NULL DEFAULT '',
    captured_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at    DATETIME,
    auto_completed  BOOLEAN DEFAULT 0,
    pinned          BOOLEAN DEFAULT 0,
    UNIQUE(source, title, source_ts)
);`,
	},
	{
		Version:     4,
		Description: "add snippet column to signals",
		SQL:         `ALTER TABLE signals ADD COLUMN snippet TEXT DEFAULT '';`,
	},
	{
		Version:     5,
		Description: "widen signals unique constraint to include preview",
		SQL: `
CREATE TABLE signals_new (
    id              INTEGER PRIMARY KEY,
    source          TEXT NOT NULL,
    title           TEXT NOT NULL,
    preview         TEXT DEFAULT '',
    snippet         TEXT DEFAULT '',
    source_ts       TEXT NOT NULL DEFAULT '',
    captured_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at    DATETIME,
    auto_completed  BOOLEAN DEFAULT 0,
    pinned          BOOLEAN DEFAULT 0,
    UNIQUE(source, title, preview, source_ts)
);
INSERT INTO signals_new SELECT id, source, title, preview, snippet, source_ts, captured_at, completed_at, auto_completed, pinned FROM signals;
DROP TABLE signals;
ALTER TABLE signals_new RENAME TO signals;`,
	},
	{
		Version:     6,
		Description: "add kind, urgency, urgency_source columns to signals",
		SQL: `ALTER TABLE signals ADD COLUMN kind TEXT DEFAULT '';
ALTER TABLE signals ADD COLUMN urgency TEXT;
ALTER TABLE signals ADD COLUMN urgency_source TEXT;`,
	},
	{
		Version:     7,
		Description: "create github_entities and github_entity_events tables",
		SQL: `
CREATE TABLE github_entities (
    id                INTEGER PRIMARY KEY,
    owner             TEXT NOT NULL,
    repo              TEXT NOT NULL,
    number            INTEGER NOT NULL,
    kind              TEXT NOT NULL,
    title             TEXT DEFAULT '',
    state             TEXT DEFAULT '',
    author            TEXT DEFAULT '',
    assignees         TEXT DEFAULT '',
    review_status     TEXT,
    checks_status     TEXT,
    first_seen_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
    first_seen_source TEXT NOT NULL DEFAULT '',
    last_refreshed_at DATETIME,
    gh_updated_at     DATETIME,
    UNIQUE(owner, repo, number)
);
CREATE TABLE github_entity_events (
    id          INTEGER PRIMARY KEY,
    entity_id   INTEGER NOT NULL REFERENCES github_entities(id) ON DELETE CASCADE,
    event_type  TEXT NOT NULL,
    signal_id   INTEGER REFERENCES signals(id),
    snapshot_id INTEGER REFERENCES snapshots(id),
    detail      TEXT DEFAULT '',
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);`,
	},
}

// OpenDB opens (or creates) a SQLite database at the given path.
// It creates parent directories if needed, enables foreign keys and WAL mode,
// and runs any pending migrations.
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

	// Run migrations.
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return db, nil
}

// runMigrations ensures the schema_migrations table exists, detects which
// migrations have already been applied, and runs any pending ones.
// For pre-migration databases (tables exist but no schema_migrations table),
// it detects the current state and marks already-applied migrations.
func runMigrations(db *sql.DB) error {
	// Create the migrations tracking table.
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version     INTEGER PRIMARY KEY,
		description TEXT NOT NULL,
		applied_at  DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	// Detect pre-migration databases: if the snapshots table exists but
	// schema_migrations is empty, the DB was created before migrations
	// were introduced. Mark migration 1 as applied since the tables exist.
	var appliedCount int
	db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&appliedCount)
	if appliedCount == 0 {
		var snapshotsExists int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('snapshots')").Scan(&snapshotsExists)
		if snapshotsExists > 0 {
			// Old DB — migration 1 was effectively already applied.
			db.Exec("INSERT INTO schema_migrations (version, description) VALUES (1, 'initial schema')")
		}
	}

	// Apply pending migrations in order.
	for _, m := range migrations {
		var exists int
		err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", m.Version).Scan(&exists)
		if err != nil {
			return fmt.Errorf("check migration %d: %w", m.Version, err)
		}
		if exists > 0 {
			continue
		}

		if _, err := db.Exec(m.SQL); err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", m.Version, m.Description, err)
		}
		if _, err := db.Exec(
			"INSERT INTO schema_migrations (version, description) VALUES (?, ?)",
			m.Version, m.Description,
		); err != nil {
			return fmt.Errorf("record migration %d: %w", m.Version, err)
		}
	}

	// Run one-time backfill for GitHub entities after first migration.
	var ghCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM github_entities").Scan(&ghCount); err == nil && ghCount == 0 {
		// Table exists but is empty — backfill from existing data
		BackfillGitHubEntities(db)
	}

	return nil
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
// transaction. The rev number is auto-assigned per profile. Label is optional
// (empty string = no label). Returns the assigned rev number.
func CreateSnapshot(db *sql.DB, profile string, groups []SnapshotGroup, tabs []SnapshotTab, label string) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Assign next rev for this profile.
	var rev int
	err = tx.QueryRow("SELECT COALESCE(MAX(rev), 0) + 1 FROM snapshots WHERE profile = ?", profile).Scan(&rev)
	if err != nil {
		return 0, fmt.Errorf("compute next rev: %w", err)
	}

	// Convert empty label to nil for SQL.
	var nameVal interface{}
	if label != "" {
		nameVal = label
	}

	tabCount := len(tabs)
	res, err := tx.Exec(
		"INSERT INTO snapshots (rev, name, profile, tab_count) VALUES (?, ?, ?, ?)",
		rev, nameVal, profile, tabCount,
	)
	if err != nil {
		return 0, fmt.Errorf("insert snapshot: %w", err)
	}
	snapID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get snapshot id: %w", err)
	}

	// Insert groups and record their DB IDs (indexed by slice position).
	groupIDs := make([]int64, len(groups))
	for i, g := range groups {
		res, err := tx.Exec(
			"INSERT INTO snapshot_groups (snapshot_id, firefox_id, name, color) VALUES (?, ?, ?, ?)",
			snapID, g.FirefoxID, g.Name, g.Color,
		)
		if err != nil {
			return 0, fmt.Errorf("insert group %q: %w", g.Name, err)
		}
		gID, err := res.LastInsertId()
		if err != nil {
			return 0, fmt.Errorf("get group id: %w", err)
		}
		groupIDs[i] = gID
	}

	// Insert tabs.
	for _, tab := range tabs {
		var groupID *int64
		if tab.GroupIndex != nil {
			idx := *tab.GroupIndex
			if idx < 0 || idx >= len(groupIDs) {
				return 0, fmt.Errorf("tab %q has invalid group index %d", tab.URL, idx)
			}
			gid := groupIDs[idx]
			groupID = &gid
		}
		_, err := tx.Exec(
			"INSERT INTO snapshot_tabs (snapshot_id, group_id, url, title, pinned) VALUES (?, ?, ?, ?, ?)",
			snapID, groupID, tab.URL, tab.Title, tab.Pinned,
		)
		if err != nil {
			return 0, fmt.Errorf("insert tab %q: %w", tab.URL, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}
	return rev, nil
}

// ListSnapshots returns all snapshots ordered by creation time descending.
func ListSnapshots(db *sql.DB) ([]SnapshotSummary, error) {
	rows, err := db.Query(
		"SELECT id, rev, name, profile, created_at, tab_count FROM snapshots ORDER BY created_at DESC, id DESC",
	)
	if err != nil {
		return nil, fmt.Errorf("query snapshots: %w", err)
	}
	defer rows.Close()

	var result []SnapshotSummary
	for rows.Next() {
		var s SnapshotSummary
		var name sql.NullString
		if err := rows.Scan(&s.ID, &s.Rev, &name, &s.Profile, &s.CreatedAt, &s.TabCount); err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}
		if name.Valid {
			s.Name = name.String
		}
		result = append(result, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate snapshots: %w", err)
	}
	return result, nil
}

// ListSnapshotsByProfile returns snapshots for a specific profile, ordered by
// creation time descending.
func ListSnapshotsByProfile(db *sql.DB, profile string) ([]SnapshotSummary, error) {
	rows, err := db.Query(
		"SELECT id, rev, name, profile, created_at, tab_count FROM snapshots WHERE profile = ? ORDER BY created_at DESC, id DESC",
		profile,
	)
	if err != nil {
		return nil, fmt.Errorf("query snapshots: %w", err)
	}
	defer rows.Close()

	var result []SnapshotSummary
	for rows.Next() {
		var s SnapshotSummary
		var name sql.NullString
		if err := rows.Scan(&s.ID, &s.Rev, &name, &s.Profile, &s.CreatedAt, &s.TabCount); err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}
		if name.Valid {
			s.Name = name.String
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// GetSnapshot loads a full snapshot by profile and rev number.
// Each tab's GroupName field is populated from the associated group.
func GetSnapshot(db *sql.DB, profile string, rev int) (*SnapshotFull, error) {
	snap := &SnapshotFull{}

	var name sql.NullString
	err := db.QueryRow(
		"SELECT id, rev, name, profile, created_at, tab_count FROM snapshots WHERE profile = ? AND rev = ?",
		profile, rev,
	).Scan(&snap.ID, &snap.Rev, &name, &snap.Profile, &snap.CreatedAt, &snap.TabCount)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("snapshot rev %d not found for profile %q", rev, profile)
		}
		return nil, fmt.Errorf("query snapshot: %w", err)
	}
	if name.Valid {
		snap.Name = name.String
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

// GetLatestSnapshot returns the most recent snapshot for a profile.
// Returns nil, nil if no snapshots exist for the profile.
func GetLatestSnapshot(db *sql.DB, profile string) (*SnapshotFull, error) {
	var rev int
	err := db.QueryRow(
		"SELECT rev FROM snapshots WHERE profile = ? ORDER BY rev DESC LIMIT 1",
		profile,
	).Scan(&rev)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query latest rev: %w", err)
	}
	return GetSnapshot(db, profile, rev)
}

// DeleteSnapshot removes a snapshot by profile and rev. Groups and tabs are cascade-deleted.
// Returns an error if the snapshot does not exist.
func DeleteSnapshot(db *sql.DB, profile string, rev int) error {
	res, err := db.Exec("DELETE FROM snapshots WHERE profile = ? AND rev = ?", profile, rev)
	if err != nil {
		return fmt.Errorf("delete snapshot: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("check rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("snapshot rev %d not found for profile %q", rev, profile)
	}
	return nil
}
