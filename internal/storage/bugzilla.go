package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// BugzillaEntity represents a tracked bug from any Bugzilla instance.
type BugzillaEntity struct {
	ID              int64
	Host            string
	BugID           int
	Title           string
	Status          string
	Resolution      string
	Assignee        string
	FirstSeenAt     time.Time
	FirstSeenSource string
	LastRefreshedAt *time.Time
}

// BugzillaStatusUpdate holds API-fetched fields to persist.
type BugzillaStatusUpdate struct {
	Title, Status, Resolution, Assignee string
}

// BugzillaEntityEvent is a timeline entry for a Bugzilla entity.
type BugzillaEntityEvent struct {
	ID         int64
	EntityID   int64
	EventType  string // "tab_seen" or "signal_seen"
	SignalID   *int64
	SnapshotID *int64
	Detail     string
	CreatedAt  time.Time
}

type bugzillaRef struct {
	host  string
	bugID int
}

var (
	urlCandidatePattern = regexp.MustCompile(`https?://[^\s<>()"']+`)
)

// UpsertBugzillaEntity looks up a bug by host+bug_id. If it does not exist,
// it inserts a new row. Returns (id, isNew, error).
func UpsertBugzillaEntity(db *sql.DB, host string, bugID int, source string) (int64, bool, error) {
	var id int64
	err := db.QueryRow(
		`SELECT id FROM bugzilla_entities WHERE host = ? AND bug_id = ?`,
		host, bugID,
	).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if err != sql.ErrNoRows {
		return 0, false, fmt.Errorf("select bugzilla entity: %w", err)
	}

	res, err := db.Exec(
		`INSERT INTO bugzilla_entities (host, bug_id, first_seen_source)
		 VALUES (?, ?, ?)`,
		host, bugID, source,
	)
	if err != nil {
		return 0, false, fmt.Errorf("insert bugzilla entity: %w", err)
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, false, fmt.Errorf("get last insert id: %w", err)
	}
	return id, true, nil
}

// RecordBugzillaEvent inserts a timeline event for a Bugzilla entity.
func RecordBugzillaEvent(db *sql.DB, entityID int64, eventType string, signalID *int64, snapshotID *int64, detail string) error {
	_, err := db.Exec(
		`INSERT INTO bugzilla_entity_events (entity_id, event_type, signal_id, snapshot_id, detail)
		 VALUES (?, ?, ?, ?, ?)`,
		entityID, eventType, signalID, snapshotID, detail,
	)
	if err != nil {
		return fmt.Errorf("insert bugzilla entity event: %w", err)
	}
	return nil
}

// ListBugzillaEntities returns tracked entities ordered by first_seen_at DESC.
func ListBugzillaEntities(db *sql.DB) ([]BugzillaEntity, error) {
	rows, err := db.Query(
		`SELECT id, host, bug_id, title, status, resolution, assignee,
		        first_seen_at, first_seen_source, last_refreshed_at
		 FROM bugzilla_entities
		 ORDER BY first_seen_at DESC, id DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query bugzilla entities: %w", err)
	}
	defer rows.Close()

	var result []BugzillaEntity
	for rows.Next() {
		var e BugzillaEntity
		var lr sql.NullTime
		if err := rows.Scan(&e.ID, &e.Host, &e.BugID,
			&e.Title, &e.Status, &e.Resolution, &e.Assignee,
			&e.FirstSeenAt, &e.FirstSeenSource, &lr); err != nil {
			return nil, fmt.Errorf("scan bugzilla entity: %w", err)
		}
		if lr.Valid {
			e.LastRefreshedAt = &lr.Time
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

// UpdateBugzillaEntityStatus persists API-fetched fields and sets last_refreshed_at.
func UpdateBugzillaEntityStatus(db *sql.DB, id int64, u BugzillaStatusUpdate) error {
	res, err := db.Exec(
		`UPDATE bugzilla_entities SET title=?, status=?, resolution=?, assignee=?,
		 last_refreshed_at=CURRENT_TIMESTAMP WHERE id=?`,
		u.Title, u.Status, u.Resolution, u.Assignee, id)
	if err != nil {
		return fmt.Errorf("update bugzilla entity status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("entity %d not found", id)
	}
	return nil
}

// CleanBugzillaTabTitle extracts the bug summary from a Firefox tab title.
// Handles "Bug 12345 – Summary – Bugzilla" and "Bug 12345 - Summary - Bugzilla".
func CleanBugzillaTabTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	for _, sep := range []string{" \u2013 ", " - "} {
		parts := strings.SplitN(title, sep, 3)
		if len(parts) == 3 && strings.HasPrefix(strings.ToLower(parts[0]), "bug ") {
			if summary := strings.TrimSpace(parts[1]); summary != "" {
				return summary
			}
		}
	}
	return title
}

// BugzillaEntityCount returns the number of tracked Bugzilla issues.
func BugzillaEntityCount(db *sql.DB) (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM bugzilla_entities`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count bugzilla entities: %w", err)
	}
	return count, nil
}

// ListBugzillaEntityEvents returns all events for an entity, ordered by created_at ASC.
func ListBugzillaEntityEvents(db *sql.DB, entityID int64) ([]BugzillaEntityEvent, error) {
	rows, err := db.Query(
		`SELECT id, entity_id, event_type, signal_id, snapshot_id, detail, created_at
		 FROM bugzilla_entity_events WHERE entity_id = ? ORDER BY created_at ASC`,
		entityID,
	)
	if err != nil {
		return nil, fmt.Errorf("query bugzilla entity events: %w", err)
	}
	defer rows.Close()

	var result []BugzillaEntityEvent
	for rows.Next() {
		var ev BugzillaEntityEvent
		var signalID, snapshotID sql.NullInt64
		if err := rows.Scan(&ev.ID, &ev.EntityID, &ev.EventType, &signalID, &snapshotID, &ev.Detail, &ev.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan bugzilla entity event: %w", err)
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

// BugzillaJSONOutput is the structure for `tabsordnung bugzilla --json` output.
type BugzillaJSONOutput struct {
	Host            string `json:"host"`
	BugID           int    `json:"bug_id"`
	URL             string `json:"url"`
	Title           string `json:"title"`
	Status          string `json:"status"`
	Resolution      string `json:"resolution"`
	Assignee        string `json:"assignee"`
	FirstSeenAt     string `json:"first_seen_at"`
	FirstSeenSource string `json:"first_seen_source"`
	LastRefreshedAt string `json:"last_refreshed_at,omitempty"`
}

// FormatBugzillaMarkdown formats entities grouped by host as markdown.
func FormatBugzillaMarkdown(entities []BugzillaEntity, events map[int64][]BugzillaEntityEvent) string {
	if len(entities) == 0 {
		return "No Bugzilla issues found.\n"
	}

	grouped := make(map[string][]BugzillaEntity)
	for _, e := range entities {
		grouped[e.Host] = append(grouped[e.Host], e)
	}
	hosts := make([]string, 0, len(grouped))
	for host := range grouped {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)

	var b strings.Builder
	for _, host := range hosts {
		items := grouped[host]
		fmt.Fprintf(&b, "## %s (%d)\n\n", host, len(items))
		for _, e := range items {
			statusStr := ""
			if e.Status != "" {
				statusStr = " [" + e.Status
				if e.Resolution != "" {
					statusStr += "/" + e.Resolution
				}
				statusStr += "]"
			}
			titleStr := ""
			if t := strings.TrimSpace(e.Title); t != "" {
				titleStr = " " + t
			}
			fmt.Fprintf(&b, "- %s#%d%s%s\n", e.Host, e.BugID, statusStr, titleStr)
			source := e.FirstSeenSource
			if source == "" {
				source = firstSeenSourceBugzilla(e, events)
			}
			fmt.Fprintf(
				&b,
				"  First seen: %s (%s)\n  URL: https://%s/show_bug.cgi?id=%d\n\n",
				e.FirstSeenAt.Format("2006-01-02"),
				source,
				e.Host,
				e.BugID,
			)
		}
	}
	return b.String()
}

func firstSeenSourceBugzilla(e BugzillaEntity, events map[int64][]BugzillaEntityEvent) string {
	entityEvents, ok := events[e.ID]
	if !ok || len(entityEvents) == 0 {
		return "unknown"
	}
	switch entityEvents[0].EventType {
	case "tab_seen":
		return "tab"
	case "signal_seen":
		return "signal"
	default:
		return "unknown"
	}
}

// FormatBugzillaJSON formats entities as a flat JSON array.
func FormatBugzillaJSON(entities []BugzillaEntity) (string, error) {
	out := make([]BugzillaJSONOutput, 0, len(entities))
	for _, e := range entities {
		item := BugzillaJSONOutput{
			Host:            e.Host,
			BugID:           e.BugID,
			URL:             fmt.Sprintf("https://%s/show_bug.cgi?id=%d", e.Host, e.BugID),
			Title:           e.Title,
			Status:          e.Status,
			Resolution:      e.Resolution,
			Assignee:        e.Assignee,
			FirstSeenAt:     e.FirstSeenAt.Format(time.RFC3339),
			FirstSeenSource: e.FirstSeenSource,
		}
		if e.LastRefreshedAt != nil {
			item.LastRefreshedAt = e.LastRefreshedAt.Format(time.RFC3339)
		}
		out = append(out, item)
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}

// ExtractBugzillaFromSnapshot scans a snapshot's tabs for Bugzilla URLs and
// upserts entities. Returns the number of entities found.
func ExtractBugzillaFromSnapshot(db *sql.DB, snapshotID int64) (int, error) {
	rows, err := db.Query("SELECT url, title FROM snapshot_tabs WHERE snapshot_id = ?", snapshotID)
	if err != nil {
		return 0, fmt.Errorf("query snapshot tabs: %w", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var tabURL, tabTitle string
		if err := rows.Scan(&tabURL, &tabTitle); err != nil {
			continue
		}
		ref := extractBugzillaFromURL(tabURL)
		if ref == nil {
			continue
		}
		id, isNew, err := UpsertBugzillaEntity(db, ref.host, ref.bugID, "tab")
		if err != nil {
			continue
		}
		if isNew && tabTitle != "" {
			if cleaned := CleanBugzillaTabTitle(tabTitle); cleaned != "" {
				db.Exec(`UPDATE bugzilla_entities SET title=? WHERE id=? AND title=''`, cleaned, id)
			}
		}
		_ = RecordBugzillaEvent(db, id, "tab_seen", nil, &snapshotID, "")
		count++
	}
	return count, rows.Err()
}

// ExtractBugzillaFromSignals scans signal fields for Bugzilla references and
// upserts entities. Returns the number of entities found.
func ExtractBugzillaFromSignals(db *sql.DB, signals []SignalRecord) (int, error) {
	count := 0
	for _, sig := range signals {
		ref := extractBugzillaFromSignalRecord(sig)
		if ref == nil {
			continue
		}
		id, _, err := UpsertBugzillaEntity(db, ref.host, ref.bugID, "signal")
		if err != nil {
			continue
		}
		sigID := sig.ID
		_ = RecordBugzillaEvent(db, id, "signal_seen", &sigID, nil, "")
		count++
	}
	return count, nil
}

// BackfillBugzillaEntities scans all existing snapshot tabs and signals for
// Bugzilla references. Safe to run multiple times (upsert-based).
func BackfillBugzillaEntities(db *sql.DB) (int, error) {
	seen := make(map[string]bool) // "host/bug_id"

	rows, err := db.Query(`
		SELECT st.url, st.title, s.id, s.created_at
		FROM snapshot_tabs st
		JOIN snapshots s ON s.id = st.snapshot_id
		ORDER BY s.created_at ASC`)
	if err != nil {
		return 0, fmt.Errorf("query snapshot tabs: %w", err)
	}

	for rows.Next() {
		var tabURL, tabTitle string
		var snapID int64
		var createdAt time.Time
		if err := rows.Scan(&tabURL, &tabTitle, &snapID, &createdAt); err != nil {
			continue
		}
		ref := extractBugzillaFromURL(tabURL)
		if ref == nil {
			continue
		}
		key := fmt.Sprintf("%s/%d", ref.host, ref.bugID)
		id, isNew, err := UpsertBugzillaEntity(db, ref.host, ref.bugID, "tab")
		if err != nil {
			continue
		}
		if isNew {
			db.Exec("UPDATE bugzilla_entities SET first_seen_at = ? WHERE id = ?", createdAt, id)
			if tabTitle != "" {
				if cleaned := CleanBugzillaTabTitle(tabTitle); cleaned != "" {
					db.Exec(`UPDATE bugzilla_entities SET title=? WHERE id=? AND title=''`, cleaned, id)
				}
			}
		}
		if !seen[key] {
			_ = RecordBugzillaEvent(db, id, "tab_seen", nil, &snapID, "")
		}
		seen[key] = true
	}
	rows.Close()

	signals, err := ListSignals(db, "", true)
	if err != nil {
		return 0, fmt.Errorf("list signals for backfill: %w", err)
	}
	for _, sig := range signals {
		ref := extractBugzillaFromSignalRecord(sig)
		if ref == nil {
			continue
		}
		key := fmt.Sprintf("%s/%d", ref.host, ref.bugID)
		id, isNew, err := UpsertBugzillaEntity(db, ref.host, ref.bugID, "signal")
		if err != nil {
			continue
		}
		if isNew {
			db.Exec("UPDATE bugzilla_entities SET first_seen_at = ? WHERE id = ?", sig.CapturedAt, id)
		}
		if !seen[key] {
			sigID := sig.ID
			_ = RecordBugzillaEvent(db, id, "signal_seen", &sigID, nil, "")
		}
		seen[key] = true
	}

	return len(seen), nil
}

func extractBugzillaFromSignalRecord(sig SignalRecord) *bugzillaRef {
	for _, text := range []string{sig.Snippet, sig.Preview, sig.Title} {
		if ref := extractBugzillaURLFromText(text); ref != nil {
			return ref
		}
	}
	return nil
}

func extractBugzillaURLFromText(text string) *bugzillaRef {
	if text == "" {
		return nil
	}
	candidates := urlCandidatePattern.FindAllString(text, -1)
	for _, candidate := range candidates {
		if ref := extractBugzillaFromURL(candidate); ref != nil {
			return ref
		}
	}
	return nil
}

func extractBugzillaFromURL(rawURL string) *bugzillaRef {
	if rawURL == "" {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	if u.Host == "" {
		return nil
	}

	host := strings.ToLower(u.Hostname())
	path := strings.Trim(u.EscapedPath(), "/")
	if path == "" {
		path = strings.Trim(u.Path, "/")
	}

	// Common Bugzilla URL: /show_bug.cgi?id=12345
	if strings.HasSuffix(path, "show_bug.cgi") {
		if id := u.Query().Get("id"); id != "" {
			if bugID, ok := parsePositiveInt(id); ok {
				return &bugzillaRef{host: host, bugID: bugID}
			}
		}
	}

	parts := strings.Split(path, "/")
	// Common REST endpoint: /rest/bug/12345
	if len(parts) >= 3 && parts[0] == "rest" && parts[1] == "bug" {
		if bugID, ok := parsePositiveInt(parts[2]); ok {
			return &bugzillaRef{host: host, bugID: bugID}
		}
	}
	// Some installations use /bug/12345
	if len(parts) >= 2 && parts[0] == "bug" {
		if bugID, ok := parsePositiveInt(parts[1]); ok {
			return &bugzillaRef{host: host, bugID: bugID}
		}
	}

	return nil
}

func parsePositiveInt(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
