package storage

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// GitHubEntity represents a tracked GitHub PR or issue.
type GitHubEntity struct {
	ID              int64
	Owner           string
	Repo            string
	Number          int
	Kind            string // "pull" or "issue"
	Title           string
	State           string // "open", "closed", "merged", ""
	Author          string
	Assignees       string // comma-separated
	ReviewStatus    *string
	ChecksStatus    *string
	FirstSeenAt     time.Time
	FirstSeenSource string
	LastRefreshedAt *time.Time
	GHUpdatedAt     *time.Time
}

// GitHubEntityEvent is a timeline entry for an entity.
type GitHubEntityEvent struct {
	ID         int64
	EntityID   int64
	EventType  string // "tab_seen", "signal_seen", "status_changed"
	SignalID   *int64
	SnapshotID *int64
	Detail     string
	CreatedAt  time.Time
}

// GitHubFilter controls which entities are returned by ListGitHubEntities.
type GitHubFilter struct {
	State string // "open", "closed", "merged", or "" for all
	Kind  string // "pull", "issue", or "" for all
	Repo  string // "owner/repo" or "" for all
}

// GitHubStatusUpdate carries fields from a gh CLI refresh.
type GitHubStatusUpdate struct {
	Title        string
	State        string
	Author       string
	Assignees    string
	ReviewStatus *string
	ChecksStatus *string
	GHUpdatedAt  *time.Time
}

// UpsertGitHubEntity looks up a GitHub entity by owner/repo/number. If it does
// not exist, it inserts a new row. Returns (id, isNew, error).
func UpsertGitHubEntity(db *sql.DB, owner, repo string, number int, kind, source string) (int64, bool, error) {
	var id int64
	err := db.QueryRow(
		`SELECT id FROM github_entities WHERE owner = ? AND repo = ? AND number = ?`,
		owner, repo, number,
	).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if err != sql.ErrNoRows {
		return 0, false, fmt.Errorf("select github entity: %w", err)
	}

	res, err := db.Exec(
		`INSERT INTO github_entities (owner, repo, number, kind, first_seen_source)
		 VALUES (?, ?, ?, ?, ?)`,
		owner, repo, number, kind, source,
	)
	if err != nil {
		return 0, false, fmt.Errorf("insert github entity: %w", err)
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, false, fmt.Errorf("get last insert id: %w", err)
	}
	return id, true, nil
}

// RecordGitHubEvent inserts a timeline event for a GitHub entity.
func RecordGitHubEvent(db *sql.DB, entityID int64, eventType string, signalID *int64, snapshotID *int64, detail string) error {
	_, err := db.Exec(
		`INSERT INTO github_entity_events (entity_id, event_type, signal_id, snapshot_id, detail)
		 VALUES (?, ?, ?, ?, ?)`,
		entityID, eventType, signalID, snapshotID, detail,
	)
	if err != nil {
		return fmt.Errorf("insert github entity event: %w", err)
	}
	return nil
}

// GetGitHubEntity retrieves a single entity by owner/repo/number.
// Returns nil, nil if the entity does not exist.
func GetGitHubEntity(db *sql.DB, owner, repo string, number int) (*GitHubEntity, error) {
	var e GitHubEntity
	var reviewStatus, checksStatus sql.NullString
	var lastRefreshedAt, ghUpdatedAt sql.NullTime

	err := db.QueryRow(
		`SELECT id, owner, repo, number, kind, title, state, author, assignees,
		        review_status, checks_status, first_seen_at, first_seen_source,
		        last_refreshed_at, gh_updated_at
		 FROM github_entities WHERE owner = ? AND repo = ? AND number = ?`,
		owner, repo, number,
	).Scan(&e.ID, &e.Owner, &e.Repo, &e.Number, &e.Kind, &e.Title, &e.State,
		&e.Author, &e.Assignees, &reviewStatus, &checksStatus,
		&e.FirstSeenAt, &e.FirstSeenSource, &lastRefreshedAt, &ghUpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("select github entity: %w", err)
	}

	if reviewStatus.Valid {
		e.ReviewStatus = &reviewStatus.String
	}
	if checksStatus.Valid {
		e.ChecksStatus = &checksStatus.String
	}
	if lastRefreshedAt.Valid {
		e.LastRefreshedAt = &lastRefreshedAt.Time
	}
	if ghUpdatedAt.Valid {
		e.GHUpdatedAt = &ghUpdatedAt.Time
	}

	return &e, nil
}

// ListGitHubEntities returns entities matching the given filter.
// Results are ordered: open/empty-state first, then by gh_updated_at DESC, first_seen_at DESC.
func ListGitHubEntities(db *sql.DB, filter GitHubFilter) ([]GitHubEntity, error) {
	query := `SELECT id, owner, repo, number, kind, title, state, author, assignees,
	                 review_status, checks_status, first_seen_at, first_seen_source,
	                 last_refreshed_at, gh_updated_at
	          FROM github_entities WHERE 1=1`
	var args []interface{}

	if filter.State != "" {
		query += " AND state = ?"
		args = append(args, filter.State)
	}
	if filter.Kind != "" {
		query += " AND kind = ?"
		args = append(args, filter.Kind)
	}
	if filter.Repo != "" {
		parts := strings.SplitN(filter.Repo, "/", 2)
		if len(parts) == 2 {
			query += " AND owner = ? AND repo = ?"
			args = append(args, parts[0], parts[1])
		}
	}

	query += ` ORDER BY
		CASE WHEN state = 'open' OR state = '' THEN 0 ELSE 1 END,
		COALESCE(gh_updated_at, '1970-01-01') DESC,
		first_seen_at DESC`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query github entities: %w", err)
	}
	defer rows.Close()

	var result []GitHubEntity
	for rows.Next() {
		var e GitHubEntity
		var reviewStatus, checksStatus sql.NullString
		var lastRefreshedAt, ghUpdatedAt sql.NullTime
		if err := rows.Scan(&e.ID, &e.Owner, &e.Repo, &e.Number, &e.Kind, &e.Title, &e.State,
			&e.Author, &e.Assignees, &reviewStatus, &checksStatus,
			&e.FirstSeenAt, &e.FirstSeenSource, &lastRefreshedAt, &ghUpdatedAt); err != nil {
			return nil, fmt.Errorf("scan github entity: %w", err)
		}
		if reviewStatus.Valid {
			e.ReviewStatus = &reviewStatus.String
		}
		if checksStatus.Valid {
			e.ChecksStatus = &checksStatus.String
		}
		if lastRefreshedAt.Valid {
			e.LastRefreshedAt = &lastRefreshedAt.Time
		}
		if ghUpdatedAt.Valid {
			e.GHUpdatedAt = &ghUpdatedAt.Time
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

// ListGitHubEntityEvents returns all events for an entity, ordered by created_at ASC.
func ListGitHubEntityEvents(db *sql.DB, entityID int64) ([]GitHubEntityEvent, error) {
	rows, err := db.Query(
		`SELECT id, entity_id, event_type, signal_id, snapshot_id, detail, created_at
		 FROM github_entity_events WHERE entity_id = ? ORDER BY created_at ASC`,
		entityID,
	)
	if err != nil {
		return nil, fmt.Errorf("query github entity events: %w", err)
	}
	defer rows.Close()

	var result []GitHubEntityEvent
	for rows.Next() {
		var ev GitHubEntityEvent
		var signalID, snapshotID sql.NullInt64
		if err := rows.Scan(&ev.ID, &ev.EntityID, &ev.EventType, &signalID, &snapshotID,
			&ev.Detail, &ev.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan github entity event: %w", err)
		}
		if signalID.Valid {
			v := signalID.Int64
			ev.SignalID = &v
		}
		if snapshotID.Valid {
			v := snapshotID.Int64
			ev.SnapshotID = &v
		}
		result = append(result, ev)
	}
	return result, rows.Err()
}

// UpdateGitHubEntityStatus updates an entity with fresh data from a gh CLI refresh.
// Sets last_refreshed_at to CURRENT_TIMESTAMP.
func UpdateGitHubEntityStatus(db *sql.DB, id int64, update GitHubStatusUpdate) error {
	res, err := db.Exec(
		`UPDATE github_entities
		 SET title = ?, state = ?, author = ?, assignees = ?,
		     review_status = ?, checks_status = ?,
		     gh_updated_at = ?, last_refreshed_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		update.Title, update.State, update.Author, update.Assignees,
		update.ReviewStatus, update.ChecksStatus,
		update.GHUpdatedAt, id,
	)
	if err != nil {
		return fmt.Errorf("update github entity status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("github entity %d not found", id)
	}
	return nil
}

// OpenGitHubEntityCount returns the number of entities where state is 'open' or ''.
func OpenGitHubEntityCount(db *sql.DB) (int, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM github_entities WHERE state = 'open' OR state = ''`,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count open github entities: %w", err)
	}
	return count, nil
}

// ghRef holds the parsed components of a GitHub issue/PR URL.
type ghRef struct {
	owner  string
	repo   string
	number int
	kind   string
}

var ghURLPattern = regexp.MustCompile(`https?://github\.com/([^/]+)/([^/]+)/(issues|pull)/(\d+)`)

func extractGitHubRef(rawURL string) *ghRef {
	matches := ghURLPattern.FindStringSubmatch(rawURL)
	if matches == nil {
		return nil
	}
	num, _ := strconv.Atoi(matches[4])
	kind := "issue"
	if matches[3] == "pull" {
		kind = "pull"
	}
	return &ghRef{owner: matches[1], repo: matches[2], number: num, kind: kind}
}

// signalGHSubjectPattern matches [owner/repo] ... (#123) in email subjects.
var signalGHSubjectPattern = regexp.MustCompile(`\[([a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+)\].*#(\d+)`)

// ExtractGitHubFromSignals scans signal fields for GitHub references and upserts entities.
// Returns the number of entities found.
func ExtractGitHubFromSignals(db *sql.DB, signals []SignalRecord) (int, error) {
	count := 0
	for _, sig := range signals {
		ref := extractGitHubFromSignalRecord(sig)
		if ref == nil {
			continue
		}
		kind := ref.kind
		if kind == "" {
			kind = "pull" // default, will be resolved by gh refresh
		}
		id, _, err := UpsertGitHubEntity(db, ref.owner, ref.repo, ref.number, kind, "signal")
		if err != nil {
			continue
		}
		sigID := sig.ID
		_ = RecordGitHubEvent(db, id, "signal_seen", &sigID, nil, "")
		count++
	}
	return count, nil
}

func extractGitHubFromSignalRecord(sig SignalRecord) *ghRef {
	// Try subject pattern: [owner/repo] ... (#123)
	for _, text := range []string{sig.Preview, sig.Title} {
		matches := signalGHSubjectPattern.FindStringSubmatch(text)
		if matches != nil {
			num, _ := strconv.Atoi(matches[2])
			ownerRepo := matches[1]
			for i, c := range ownerRepo {
				if c == '/' {
					return &ghRef{
						owner:  ownerRepo[:i],
						repo:   ownerRepo[i+1:],
						number: num,
						kind:   "", // unknown from subject
					}
				}
			}
		}
	}

	// Try raw URL in snippet or preview
	for _, text := range []string{sig.Snippet, sig.Preview} {
		ref := extractGitHubRef(text)
		if ref != nil {
			return ref
		}
	}

	return nil
}

// BackfillGitHubEntities scans all existing snapshot tabs and signals for GitHub references.
// Safe to run multiple times (upsert-based). Returns total unique entity count.
func BackfillGitHubEntities(db *sql.DB) (int, error) {
	seen := make(map[string]bool) // "owner/repo/number"

	// Scan all snapshot tabs, ordered by snapshot creation time (oldest first)
	rows, err := db.Query(`
		SELECT st.url, s.id, s.created_at
		FROM snapshot_tabs st
		JOIN snapshots s ON s.id = st.snapshot_id
		ORDER BY s.created_at ASC`)
	if err != nil {
		return 0, fmt.Errorf("query snapshot tabs: %w", err)
	}

	for rows.Next() {
		var tabURL string
		var snapID int64
		var createdAt time.Time
		if err := rows.Scan(&tabURL, &snapID, &createdAt); err != nil {
			continue
		}
		ref := extractGitHubRef(tabURL)
		if ref == nil {
			continue
		}
		key := fmt.Sprintf("%s/%s/%d", ref.owner, ref.repo, ref.number)
		id, isNew, err := UpsertGitHubEntity(db, ref.owner, ref.repo, ref.number, ref.kind, "tab")
		if err != nil {
			continue
		}
		if isNew {
			// Set first_seen_at to the earliest snapshot's created_at
			db.Exec("UPDATE github_entities SET first_seen_at = ? WHERE id = ?", createdAt, id)
		}
		if !seen[key] {
			// Record first sighting event only
			_ = RecordGitHubEvent(db, id, "tab_seen", nil, &snapID, "")
		}
		seen[key] = true
	}
	rows.Close()

	// Scan all signals
	signals, err := ListSignals(db, "", true) // include completed
	if err != nil {
		return 0, fmt.Errorf("list signals for backfill: %w", err)
	}
	for _, sig := range signals {
		ref := extractGitHubFromSignalRecord(sig)
		if ref == nil {
			continue
		}
		key := fmt.Sprintf("%s/%s/%d", ref.owner, ref.repo, ref.number)
		kind := ref.kind
		if kind == "" {
			kind = "pull"
		}
		id, isNew, err := UpsertGitHubEntity(db, ref.owner, ref.repo, ref.number, kind, "signal")
		if err != nil {
			continue
		}
		if isNew {
			db.Exec("UPDATE github_entities SET first_seen_at = ? WHERE id = ?", sig.CapturedAt, id)
		}
		if !seen[key] {
			sigID := sig.ID
			_ = RecordGitHubEvent(db, id, "signal_seen", &sigID, nil, "")
		}
		seen[key] = true
	}

	return len(seen), nil
}

// ExtractGitHubFromSnapshot scans a snapshot's tabs for GitHub URLs and upserts entities.
// Returns the number of entities found.
func ExtractGitHubFromSnapshot(db *sql.DB, snapshotID int64) (int, error) {
	rows, err := db.Query("SELECT url FROM snapshot_tabs WHERE snapshot_id = ?", snapshotID)
	if err != nil {
		return 0, fmt.Errorf("query snapshot tabs: %w", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var tabURL string
		if err := rows.Scan(&tabURL); err != nil {
			continue
		}
		ref := extractGitHubRef(tabURL)
		if ref == nil {
			continue
		}
		id, _, err := UpsertGitHubEntity(db, ref.owner, ref.repo, ref.number, ref.kind, "tab")
		if err != nil {
			continue
		}
		_ = RecordGitHubEvent(db, id, "tab_seen", nil, &snapshotID, "")
		count++
	}
	return count, rows.Err()
}
