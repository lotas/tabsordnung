package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
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
	Kind         string
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
		`INSERT OR IGNORE INTO github_entity_events (entity_id, event_type, signal_id, snapshot_id, detail)
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
		 SET title = CASE WHEN ? <> '' THEN ? ELSE title END,
		     state = CASE WHEN ? <> '' THEN ? ELSE state END,
		     kind = CASE WHEN ? <> '' THEN ? ELSE kind END,
		     author = ?, assignees = ?,
		     review_status = ?, checks_status = ?,
		     gh_updated_at = ?, last_refreshed_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		update.Title, update.Title, update.State, update.State, update.Kind, update.Kind, update.Author, update.Assignees,
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

// OpenGitHubEntityCount returns the number of entities treated as open.
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

// GitHubJSONOutput is the structure for `tabsordnung github --json` output.
type GitHubJSONOutput struct {
	Owner           string `json:"owner"`
	Repo            string `json:"repo"`
	Number          int    `json:"number"`
	Kind            string `json:"kind"`
	URL             string `json:"url"`
	Title           string `json:"title"`
	State           string `json:"state"`
	Author          string `json:"author"`
	Assignees       string `json:"assignees"`
	ReviewStatus    string `json:"review_status"`
	ChecksStatus    string `json:"checks_status"`
	FirstSeenAt     string `json:"first_seen_at"`
	FirstSeenSource string `json:"first_seen_source"`
	LastRefreshedAt string `json:"last_refreshed_at"`
	GHUpdatedAt     string `json:"gh_updated_at"`
}

// FormatGitHubMarkdown formats entities grouped by state as markdown.
func FormatGitHubMarkdown(entities []GitHubEntity, events map[int64][]GitHubEntityEvent) string {
	if len(entities) == 0 {
		return "No GitHub entities found.\n"
	}

	stateTitle := func(state string) string {
		switch state {
		case "open", "":
			return "Open"
		case "merged":
			return "Merged"
		case "closed":
			return "Closed"
		default:
			return capitalize(state)
		}
	}
	stateBucket := func(state string) string {
		if state == "" {
			return "open"
		}
		return state
	}
	stateOrder := map[string]int{
		"open":   0,
		"merged": 1,
		"closed": 2,
	}

	grouped := make(map[string][]GitHubEntity)
	for _, e := range entities {
		grouped[stateBucket(e.State)] = append(grouped[stateBucket(e.State)], e)
	}

	states := make([]string, 0, len(grouped))
	for s := range grouped {
		states = append(states, s)
	}
	sort.Slice(states, func(i, j int) bool {
		oi, okI := stateOrder[states[i]]
		oj, okJ := stateOrder[states[j]]
		if !okI {
			oi = 100
		}
		if !okJ {
			oj = 100
		}
		if oi != oj {
			return oi < oj
		}
		return states[i] < states[j]
	})

	var b strings.Builder
	for _, state := range states {
		items := grouped[state]
		fmt.Fprintf(&b, "## %s (%d)\n\n", stateTitle(state), len(items))
		for _, e := range items {
			title := strings.TrimSpace(e.Title)
			if title == "" {
				title = "(untitled)"
			}
			fmt.Fprintf(&b, "- %s/%s#%d [%s] %s\n", e.Owner, e.Repo, e.Number, e.Kind, title)

			var details []string
			if e.Author != "" {
				details = append(details, "Author: "+e.Author)
			}
			if e.ReviewStatus != nil && *e.ReviewStatus != "" {
				details = append(details, "Review: "+*e.ReviewStatus)
			}
			if e.ChecksStatus != nil && *e.ChecksStatus != "" {
				details = append(details, "Checks: "+*e.ChecksStatus)
			}
			if len(details) > 0 {
				fmt.Fprintf(&b, "  %s\n", strings.Join(details, " | "))
			}

			firstSource := firstSeenSource(e, events)
			lastUpdated := e.FirstSeenAt
			if e.GHUpdatedAt != nil {
				lastUpdated = *e.GHUpdatedAt
			} else if e.LastRefreshedAt != nil {
				lastUpdated = *e.LastRefreshedAt
			}
			fmt.Fprintf(
				&b,
				"  First seen: %s (%s) | Last updated: %s\n\n",
				e.FirstSeenAt.Format("2006-01-02"),
				firstSource,
				formatAge(lastUpdated),
			)
		}
	}

	return b.String()
}

func firstSeenSource(e GitHubEntity, events map[int64][]GitHubEntityEvent) string {
	if e.FirstSeenSource != "" {
		return e.FirstSeenSource
	}
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

// FormatGitHubJSON formats entities as a flat JSON array.
func FormatGitHubJSON(entities []GitHubEntity) (string, error) {
	out := make([]GitHubJSONOutput, 0, len(entities))
	for _, e := range entities {
		item := GitHubJSONOutput{
			Owner:           e.Owner,
			Repo:            e.Repo,
			Number:          e.Number,
			Kind:            e.Kind,
			URL:             fmt.Sprintf("https://github.com/%s/%s/%s/%d", e.Owner, e.Repo, entityURLPath(e.Kind), e.Number),
			Title:           e.Title,
			State:           e.State,
			Author:          e.Author,
			Assignees:       e.Assignees,
			FirstSeenAt:     e.FirstSeenAt.Format(time.RFC3339),
			FirstSeenSource: e.FirstSeenSource,
		}
		if e.ReviewStatus != nil {
			item.ReviewStatus = *e.ReviewStatus
		}
		if e.ChecksStatus != nil {
			item.ChecksStatus = *e.ChecksStatus
		}
		if e.LastRefreshedAt != nil {
			item.LastRefreshedAt = e.LastRefreshedAt.Format(time.RFC3339)
		}
		if e.GHUpdatedAt != nil {
			item.GHUpdatedAt = e.GHUpdatedAt.Format(time.RFC3339)
		}
		out = append(out, item)
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}

func GitHubEntityURL(owner, repo, kind string, number int) string {
	return fmt.Sprintf("https://github.com/%s/%s/%s/%d", owner, repo, entityURLPath(kind), number)
}

func entityURLPath(kind string) string {
	if kind == "pull" {
		return "pull"
	}
	return "issues"
}

// ghRef holds the parsed components of a GitHub issue/PR URL.
type ghRef struct {
	owner  string
	repo   string
	number int
	kind   string
	title  string
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

// signalGHSubjectPattern matches GitHub notification subjects such as:
// [owner/repo] Fix thing (PR #123), [owner/repo] Fix thing (Issue #123), or
// [owner/repo] Fix thing (#123).
var signalGHSubjectPattern = regexp.MustCompile(`(?i)\[([a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+)\]\s*(.*?)\s*\((?:(PR|Issue)\s*)?#(\d+)\)`)

// ExtractGitHubFromSignals scans signal fields for GitHub references and upserts entities.
// Returns the number of entities found.
func ExtractGitHubFromSignals(db *sql.DB, signals []SignalRecord) (int, error) {
	count := 0
	for _, sig := range signals {
		ref := extractGitHubFromSignalRecord(sig)
		if ref == nil {
			continue
		}
		id, _, err := UpsertGitHubEntity(db, ref.owner, ref.repo, ref.number, ref.kind, "signal")
		if err != nil {
			continue
		}
		_ = updateGitHubEntityLocalMetadata(db, id, ref.title, ref.kind)
		sigID := sig.ID
		_ = RecordGitHubEvent(db, id, "signal_seen", &sigID, nil, "")
		count++
	}
	return count, nil
}

func extractGitHubFromSignalRecord(sig SignalRecord) *ghRef {
	// Try subject pattern: [owner/repo] ... (#123)
	var subjectRef *ghRef
	for _, text := range []string{sig.Preview, sig.Title} {
		if ref := extractGitHubSubjectRef(text); ref != nil {
			subjectRef = ref
			break
		}
	}

	// Try raw URL in snippet or preview. This is authoritative for kind and can
	// complete a subject-derived ref that only had a title/number.
	for _, text := range []string{sig.Snippet, sig.Preview} {
		ref := extractGitHubRef(text)
		if ref != nil {
			if subjectRef != nil && subjectRef.owner == ref.owner && subjectRef.repo == ref.repo && subjectRef.number == ref.number {
				subjectRef.kind = ref.kind
				return subjectRef
			}
			return ref
		}
	}

	return subjectRef
}

func extractGitHubSubjectRef(text string) *ghRef {
	matches := signalGHSubjectPattern.FindStringSubmatch(text)
	if matches == nil {
		return nil
	}
	ownerRepo := matches[1]
	title := strings.TrimSpace(matches[2])
	kindMarker := strings.ToLower(matches[3])
	num, _ := strconv.Atoi(matches[4])
	kind := ""
	switch kindMarker {
	case "pr":
		kind = "pull"
	case "issue":
		kind = "issue"
	}
	for i, c := range ownerRepo {
		if c == '/' {
			return &ghRef{
				owner:  ownerRepo[:i],
				repo:   ownerRepo[i+1:],
				number: num,
				kind:   kind,
				title:  title,
			}
		}
	}
	return nil
}

func cleanGitHubTabTitle(title string, ref *ghRef) string {
	title = strings.TrimSpace(title)
	if title == "" || ref == nil {
		return title
	}
	entity := "Issue"
	if ref.kind == "pull" {
		entity = "Pull Request"
	}
	suffix := fmt.Sprintf(" · %s #%d · %s/%s", entity, ref.number, ref.owner, ref.repo)
	if !strings.HasSuffix(title, suffix) {
		return title
	}
	title = strings.TrimSpace(strings.TrimSuffix(title, suffix))
	if ref.kind == "pull" {
		if idx := strings.LastIndex(title, " by "); idx > 0 {
			title = strings.TrimSpace(title[:idx])
		}
	}
	return title
}

func updateGitHubEntityLocalMetadata(db *sql.DB, id int64, title, kind string) error {
	title = strings.TrimSpace(title)
	kind = strings.TrimSpace(kind)
	if title == "" && kind == "" {
		return nil
	}
	_, err := db.Exec(
		`UPDATE github_entities
		 SET title = CASE
		         WHEN trim(coalesce(title, '')) = '' AND ? <> '' THEN ?
		         ELSE title
		     END,
		     kind = CASE
		         WHEN ? <> '' AND kind <> ? THEN ?
		         ELSE kind
		     END
		 WHERE id = ?`,
		title, title, kind, kind, kind, id,
	)
	if err != nil {
		return fmt.Errorf("update github entity local metadata: %w", err)
	}
	return nil
}

func updateExistingGitHubEntityLocalMetadata(db *sql.DB, ref *ghRef) (bool, error) {
	if ref == nil {
		return false, nil
	}
	var id int64
	err := db.QueryRow(
		`SELECT id FROM github_entities WHERE owner = ? AND repo = ? AND number = ?`,
		ref.owner, ref.repo, ref.number,
	).Scan(&id)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("select github entity for metadata backfill: %w", err)
	}
	if err := updateGitHubEntityLocalMetadata(db, id, ref.title, ref.kind); err != nil {
		return false, err
	}
	return true, nil
}

// BackfillGitHubEntityMetadata fills titles/kinds for already-tracked entities
// from persisted snapshot tab titles and signal subjects. It does not mark
// entities as refreshed; API refresh is still responsible for current state.
func BackfillGitHubEntityMetadata(db *sql.DB) (int, error) {
	updated := 0

	rows, err := db.Query(`SELECT url, title FROM snapshot_tabs`)
	if err != nil {
		return 0, fmt.Errorf("query snapshot tabs for github metadata: %w", err)
	}
	for rows.Next() {
		var tabURL, tabTitle string
		if err := rows.Scan(&tabURL, &tabTitle); err != nil {
			continue
		}
		ref := extractGitHubRef(tabURL)
		if ref == nil {
			continue
		}
		ref.title = cleanGitHubTabTitle(tabTitle, ref)
		ok, err := updateExistingGitHubEntityLocalMetadata(db, ref)
		if err != nil {
			rows.Close()
			return updated, err
		}
		if ok {
			updated++
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return updated, err
	}
	rows.Close()

	signals, err := ListSignals(db, "", "", true)
	if err != nil {
		return updated, fmt.Errorf("list signals for github metadata backfill: %w", err)
	}
	for _, sig := range signals {
		ref := extractGitHubFromSignalRecord(sig)
		if ref == nil {
			continue
		}
		ok, err := updateExistingGitHubEntityLocalMetadata(db, ref)
		if err != nil {
			return updated, err
		}
		if ok {
			updated++
		}
	}
	return updated, nil
}

// BackfillGitHubEntities scans all existing snapshot tabs and signals for GitHub references.
// Safe to run multiple times (upsert-based). Returns total unique entity count.
func BackfillGitHubEntities(db *sql.DB) (int, error) {
	seen := make(map[string]bool) // "owner/repo/number"

	// Scan all snapshot tabs, ordered by snapshot creation time (oldest first)
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
		_ = updateGitHubEntityLocalMetadata(db, id, cleanGitHubTabTitle(tabTitle, ref), ref.kind)
		if !seen[key] {
			// Record first sighting event only
			_ = RecordGitHubEvent(db, id, "tab_seen", nil, &snapID, "")
		}
		seen[key] = true
	}
	rows.Close()

	// Scan all signals
	signals, err := ListSignals(db, "", "", true) // include completed
	if err != nil {
		return 0, fmt.Errorf("list signals for backfill: %w", err)
	}
	for _, sig := range signals {
		ref := extractGitHubFromSignalRecord(sig)
		if ref == nil {
			continue
		}
		key := fmt.Sprintf("%s/%s/%d", ref.owner, ref.repo, ref.number)
		id, isNew, err := UpsertGitHubEntity(db, ref.owner, ref.repo, ref.number, ref.kind, "signal")
		if err != nil {
			continue
		}
		if isNew {
			db.Exec("UPDATE github_entities SET first_seen_at = ? WHERE id = ?", sig.CapturedAt, id)
		}
		_ = updateGitHubEntityLocalMetadata(db, id, ref.title, ref.kind)
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
		ref := extractGitHubRef(tabURL)
		if ref == nil {
			continue
		}
		id, _, err := UpsertGitHubEntity(db, ref.owner, ref.repo, ref.number, ref.kind, "tab")
		if err != nil {
			continue
		}
		_ = updateGitHubEntityLocalMetadata(db, id, cleanGitHubTabTitle(tabTitle, ref), ref.kind)
		_ = RecordGitHubEvent(db, id, "tab_seen", nil, &snapshotID, "")
		count++
	}
	return count, rows.Err()
}
