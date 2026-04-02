package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lotas/tabsordnung/internal/applog"
)

// SignalRecord represents a single signal item stored in the database.
type SignalRecord struct {
	ID            int64
	Source        string
	Account       string
	Title         string
	Preview       string
	Snippet       string
	Kind          string // "dm", "mention", "channel", or ""
	SourceTS      string
	CapturedAt    time.Time
	CompletedAt   *time.Time
	AutoCompleted bool
	Pinned        bool
	Urgency       *string // "urgent", "review", "fyi", or nil (unclassified)
	UrgencySource *string // "heuristic", "llm", or nil
	Entity        *SignalEntityLink
}

// SignalEntityLink describes a GitHub or Bugzilla entity linked to a signal.
type SignalEntityLink struct {
	Provider string // "github" or "bugzilla"
	Kind     string // "pull", "issue", or "bug"
	Label    string // "owner/repo#123" or "host#123"
	Title    string
	URL      string
	State    string
	Closed   bool
}

// ClassifyByKind returns urgency for signals with a known kind.
func ClassifyByKind(kind string) (urgency string, ok bool) {
	switch kind {
	case "dm":
		return "urgent", true
	case "mention":
		return "review", true
	case "channel":
		return "fyi", true
	default:
		return "", false
	}
}

// InsertSignal inserts a signal, silently ignoring duplicates (same source+title+source_ts).
// If source_ts is empty, it is set to captured_at formatted as RFC3339 to give the signal
// a unique episode identity.
func InsertSignal(db *sql.DB, sig SignalRecord) error {
	sourceTS := sig.SourceTS
	if sourceTS == "" {
		sourceTS = sig.CapturedAt.Format(time.RFC3339)
	}
	_, err := db.Exec(
		`INSERT OR IGNORE INTO signals (source, account, title, preview, snippet, kind, source_ts, captured_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sig.Source, sig.Account, sig.Title, sig.Preview, sig.Snippet, sig.Kind, sourceTS, sig.CapturedAt,
	)
	return err
}

// ListSignals returns signals. If source is non-empty, filters by source.
// If includeCompleted is false, only returns active signals (completed_at IS NULL).
// Results are ordered: active first (newest captured_at first), then completed (newest completed_at first).
func ListSignals(db *sql.DB, source string, account string, includeCompleted bool) ([]SignalRecord, error) {
	query := `SELECT id, source, account, title, preview, snippet, kind, source_ts, captured_at, completed_at, auto_completed, pinned, urgency, urgency_source
		FROM signals WHERE 1=1`
	var args []interface{}

	if source != "" {
		query += " AND source = ?"
		args = append(args, source)
	}
	if account != "" {
		query += " AND account = ?"
		args = append(args, account)
	}
	if !includeCompleted {
		query += " AND completed_at IS NULL"
	}

	query += ` ORDER BY
		CASE WHEN completed_at IS NULL THEN 0 ELSE 1 END,
		captured_at DESC`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []SignalRecord
	for rows.Next() {
		var s SignalRecord
		var completedAt sql.NullTime
		var urgency, urgencySource sql.NullString
		if err := rows.Scan(&s.ID, &s.Source, &s.Account, &s.Title, &s.Preview, &s.Snippet, &s.Kind, &s.SourceTS,
			&s.CapturedAt, &completedAt, &s.AutoCompleted, &s.Pinned, &urgency, &urgencySource); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			s.CompletedAt = &completedAt.Time
		}
		if urgency.Valid {
			s.Urgency = &urgency.String
		}
		if urgencySource.Valid {
			s.UrgencySource = &urgencySource.String
		}
		result = append(result, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := attachSignalEntityLinks(db, result); err != nil {
		return nil, err
	}
	return result, nil
}

// ActiveSignalCounts returns the number of active (non-completed) signals per source.
// Counts are aggregated across all accounts for each source.
func ActiveSignalCounts(db *sql.DB) (map[string]int, error) {
	rows, err := db.Query(`SELECT source, COUNT(*) FROM signals WHERE completed_at IS NULL GROUP BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var source string
		var count int
		if err := rows.Scan(&source, &count); err != nil {
			return nil, err
		}
		counts[source] = count
	}
	return counts, rows.Err()
}

// HighestUrgencyBySource returns the highest urgency level per source for active signals.
func HighestUrgencyBySource(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(`SELECT source,
		CASE
			WHEN SUM(CASE WHEN urgency = 'urgent' THEN 1 ELSE 0 END) > 0 THEN 'urgent'
			WHEN SUM(CASE WHEN urgency = 'review' THEN 1 ELSE 0 END) > 0 THEN 'review'
			WHEN SUM(CASE WHEN urgency = 'fyi' THEN 1 ELSE 0 END) > 0 THEN 'fyi'
			ELSE ''
		END as highest
		FROM signals WHERE completed_at IS NULL GROUP BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var source, highest string
		if err := rows.Scan(&source, &highest); err != nil {
			return nil, err
		}
		if highest != "" {
			result[source] = highest
		}
	}
	return result, rows.Err()
}

// CompleteSignal marks a signal as manually completed. Clears pinned flag.
func CompleteSignal(db *sql.DB, id int64) error {
	res, err := db.Exec(
		`UPDATE signals SET completed_at = CURRENT_TIMESTAMP, auto_completed = 0, pinned = 0
		 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("signal %d not found", id)
	}
	return nil
}

// ReopenSignal reactivates a completed signal. Sets pinned=true to prevent auto-complete.
func ReopenSignal(db *sql.DB, id int64) error {
	res, err := db.Exec(
		`UPDATE signals SET completed_at = NULL, auto_completed = 0, pinned = 1
		 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("signal %d not found", id)
	}
	return nil
}

// ReconcileSignals processes a scrape result for a source in a single transaction.
// Each unread→read→unread cycle creates a distinct "episode" signal:
// 1. Query active signals for this source
// 2. Insert new episodes for scraped items that have no active signal (source_ts = capturedAt for uniqueness)
// 3. Auto-complete active signals missing from scrape (unless pinned)
// No reactivation — once completed, a signal stays completed and new unreads create a new episode.
func ReconcileSignals(db *sql.DB, source string, account string, items []SignalRecord, capturedAt time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Query active signals for this source+account.
	rows, err := tx.Query(
		`SELECT id, title, preview FROM signals WHERE source = ? AND account = ? AND completed_at IS NULL`, source, account)
	if err != nil {
		return err
	}
	activeKeys := make(map[string]bool) // key = title + "\n" + preview
	var activeDescList []string
	for rows.Next() {
		var id int64
		var title, preview string
		if err := rows.Scan(&id, &title, &preview); err != nil {
			rows.Close()
			return err
		}
		activeKeys[title+"\n"+preview] = true
		activeDescList = append(activeDescList, title+" | "+preview)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	applog.Info("signal.reconcile.active", "source", source, "activeCount", len(activeDescList), "signals", strings.Join(activeDescList, "; "))

	// 2. Insert new episodes for items without an active signal.
	tsStr := capturedAt.Format(time.RFC3339)
	insertStmt, err := tx.Prepare(
		`INSERT OR IGNORE INTO signals (source, account, title, preview, snippet, kind, source_ts, captured_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insertStmt.Close()

	scrapedKeys := make(map[string]bool)
	inserted := 0
	for _, item := range items {
		key := item.Title + "\n" + item.Preview
		scrapedKeys[key] = true
		if activeKeys[key] {
			applog.Info("signal.reconcile.insert", "source", source, "title", item.Title, "preview", item.Preview, "action", "skip-active")
			continue // episode still running
		}
		sourceTS := item.SourceTS
		if sourceTS == "" {
			sourceTS = tsStr
		}
		if _, err := insertStmt.Exec(source, account, item.Title, item.Preview, item.Snippet, item.Kind, sourceTS, capturedAt); err != nil {
			return err
		}
		applog.Info("signal.reconcile.insert", "source", source, "title", item.Title, "preview", item.Preview, "action", "new", "sourceTS", sourceTS)
		inserted++

		// Heuristic classification for signals with known kind
		if urgency, ok := ClassifyByKind(item.Kind); ok {
			if _, err := tx.Exec(`UPDATE signals SET urgency = ?, urgency_source = 'heuristic'
				WHERE source = ? AND account = ? AND title = ? AND preview = ? AND source_ts = ? AND urgency IS NULL`,
				urgency, source, account, item.Title, item.Preview, sourceTS); err != nil {
				return err
			}
		}
	}

	// 3. Auto-complete active signals not in current scrape (unless pinned).
	scrapedJSON := keysToJSON(scrapedKeys)
	res, err := tx.Exec(`
		UPDATE signals
		SET completed_at = CURRENT_TIMESTAMP, auto_completed = 1
		WHERE source = ? AND account = ? AND completed_at IS NULL AND pinned = 0
		  AND (title || char(10) || preview) NOT IN (SELECT value FROM json_each(?))`,
		source, account, scrapedJSON)
	if err != nil {
		return err
	}
	autoCompleted, _ := res.RowsAffected()
	applog.Info("signal.reconcile.autoComplete", "source", source, "completedCount", autoCompleted, "scrapedKeysJSON", scrapedJSON)

	if err := tx.Commit(); err != nil {
		return err
	}
	applog.Info("signal.reconcile.done", "source", source, "inserted", inserted, "autoCompleted", autoCompleted)
	return nil
}

// ListUnclassifiedSignals returns active signals that have not been classified yet.
func ListUnclassifiedSignals(db *sql.DB) ([]SignalRecord, error) {
	rows, err := db.Query(`SELECT id, source, account, title, preview, snippet, kind, source_ts, captured_at, completed_at, auto_completed, pinned, urgency, urgency_source
		FROM signals WHERE urgency IS NULL AND completed_at IS NULL
		ORDER BY captured_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []SignalRecord
	for rows.Next() {
		var s SignalRecord
		var completedAt sql.NullTime
		var urgency, urgencySource sql.NullString
		if err := rows.Scan(&s.ID, &s.Source, &s.Account, &s.Title, &s.Preview, &s.Snippet, &s.Kind, &s.SourceTS,
			&s.CapturedAt, &completedAt, &s.AutoCompleted, &s.Pinned, &urgency, &urgencySource); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			s.CompletedAt = &completedAt.Time
		}
		if urgency.Valid {
			s.Urgency = &urgency.String
		}
		if urgencySource.Valid {
			s.UrgencySource = &urgencySource.String
		}
		result = append(result, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := attachSignalEntityLinks(db, result); err != nil {
		return nil, err
	}
	return result, nil
}

func attachSignalEntityLinks(db *sql.DB, signals []SignalRecord) error {
	if len(signals) == 0 {
		return nil
	}

	byID := make(map[int64]*SignalRecord, len(signals))
	ids := make([]interface{}, 0, len(signals))
	placeholders := make([]string, 0, len(signals))
	for i := range signals {
		byID[signals[i].ID] = &signals[i]
		ids = append(ids, signals[i].ID)
		placeholders = append(placeholders, "?")
	}
	inClause := strings.Join(placeholders, ",")

	ghQuery := fmt.Sprintf(`
		SELECT gee.signal_id, ge.owner, ge.repo, ge.number, ge.kind, ge.title, ge.state
		FROM github_entity_events gee
		JOIN github_entities ge ON ge.id = gee.entity_id
		WHERE gee.event_type = 'signal_seen' AND gee.signal_id IN (%s)
		ORDER BY gee.id DESC`, inClause)
	ghRows, err := db.Query(ghQuery, ids...)
	if err != nil {
		return fmt.Errorf("query github signal links: %w", err)
	}
	defer ghRows.Close()
	for ghRows.Next() {
		var signalID int64
		var owner, repo, kind, title, state string
		var number int
		if err := ghRows.Scan(&signalID, &owner, &repo, &number, &kind, &title, &state); err != nil {
			return fmt.Errorf("scan github signal link: %w", err)
		}
		sig := byID[signalID]
		if sig == nil || sig.Entity != nil {
			continue
		}
		sig.Entity = &SignalEntityLink{
			Provider: "github",
			Kind:     kind,
			Label:    fmt.Sprintf("%s/%s#%d", owner, repo, number),
			Title:    title,
			URL:      GitHubEntityURL(owner, repo, kind, number),
			State:    state,
			Closed:   state == "closed" || state == "merged",
		}
	}
	if err := ghRows.Err(); err != nil {
		return fmt.Errorf("iterate github signal links: %w", err)
	}

	bzQuery := fmt.Sprintf(`
		SELECT bee.signal_id, be.host, be.bug_id, be.title, be.status, be.resolution
		FROM bugzilla_entity_events bee
		JOIN bugzilla_entities be ON be.id = bee.entity_id
		WHERE bee.event_type = 'signal_seen' AND bee.signal_id IN (%s)
		ORDER BY bee.id DESC`, inClause)
	bzRows, err := db.Query(bzQuery, ids...)
	if err != nil {
		return fmt.Errorf("query bugzilla signal links: %w", err)
	}
	defer bzRows.Close()
	for bzRows.Next() {
		var signalID int64
		var host, title, status, resolution string
		var bugID int
		if err := bzRows.Scan(&signalID, &host, &bugID, &title, &status, &resolution); err != nil {
			return fmt.Errorf("scan bugzilla signal link: %w", err)
		}
		sig := byID[signalID]
		if sig == nil || sig.Entity != nil {
			continue
		}
		state := status
		if resolution != "" {
			state += "/" + resolution
		}
		sig.Entity = &SignalEntityLink{
			Provider: "bugzilla",
			Kind:     "bug",
			Label:    fmt.Sprintf("%s#%d", host, bugID),
			Title:    title,
			URL:      BugzillaEntityURL(host, bugID),
			State:    state,
			Closed:   IsBugzillaClosed(status),
		}
	}
	if err := bzRows.Err(); err != nil {
		return fmt.Errorf("iterate bugzilla signal links: %w", err)
	}

	return nil
}

// AutoCompleteSignalsForClosedEntities completes active, unpinned signals whose
// linked GitHub or Bugzilla entity is already closed or resolved.
func AutoCompleteSignalsForClosedEntities(db *sql.DB) (int64, error) {
	if db == nil {
		return 0, nil
	}
	var total int64

	res, err := db.Exec(`
		UPDATE signals
		SET completed_at = CURRENT_TIMESTAMP, auto_completed = 1
		WHERE completed_at IS NULL
		  AND pinned = 0
		  AND EXISTS (
			SELECT 1
			FROM github_entity_events gee
			JOIN github_entities ge ON ge.id = gee.entity_id
			WHERE gee.signal_id = signals.id
			  AND gee.event_type = 'signal_seen'
			  AND ge.state IN ('closed', 'merged')
		  )`)
	if err != nil {
		return 0, fmt.Errorf("auto-complete github-linked signals: %w", err)
	}
	n, _ := res.RowsAffected()
	total += n

	res, err = db.Exec(`
		UPDATE signals
		SET completed_at = CURRENT_TIMESTAMP, auto_completed = 1
		WHERE completed_at IS NULL
		  AND pinned = 0
		  AND EXISTS (
			SELECT 1
			FROM bugzilla_entity_events bee
			JOIN bugzilla_entities be ON be.id = bee.entity_id
			WHERE bee.signal_id = signals.id
			  AND bee.event_type = 'signal_seen'
			  AND UPPER(be.status) IN ('RESOLVED', 'VERIFIED', 'CLOSED')
		  )`)
	if err != nil {
		return 0, fmt.Errorf("auto-complete bugzilla-linked signals: %w", err)
	}
	n, _ = res.RowsAffected()
	total += n

	return total, nil
}

// UpdateUrgency sets the urgency and urgency_source for a signal.
func UpdateUrgency(db *sql.DB, id int64, urgency, source string) error {
	res, err := db.Exec(`UPDATE signals SET urgency = ?, urgency_source = ? WHERE id = ?`,
		urgency, source, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("signal %d not found", id)
	}
	return nil
}

// keysToJSON converts a key set to a JSON array string for use with json_each().
func keysToJSON(keys map[string]bool) string {
	parts := make([]string, 0, len(keys))
	for k := range keys {
		escaped := strings.ReplaceAll(k, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `"`, `\"`)
		parts = append(parts, `"`+escaped+`"`)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// FormatSignalsMarkdown formats signals grouped by source and account as markdown.
func FormatSignalsMarkdown(signals []SignalRecord) string {
	if len(signals) == 0 {
		return "No signals found.\n"
	}

	type accountGroup struct {
		account string
		signals []SignalRecord
	}
	type sourceGroup struct {
		accounts     []*accountGroup
		accountIndex map[string]*accountGroup
	}

	sources := make(map[string]*sourceGroup)
	var sourceOrder []string
	for _, s := range signals {
		sg, ok := sources[s.Source]
		if !ok {
			sg = &sourceGroup{accountIndex: make(map[string]*accountGroup)}
			sources[s.Source] = sg
			sourceOrder = append(sourceOrder, s.Source)
		}
		ag, ok := sg.accountIndex[s.Account]
		if !ok {
			ag = &accountGroup{account: s.Account}
			sg.accounts = append(sg.accounts, ag)
			sg.accountIndex[s.Account] = ag
		}
		ag.signals = append(ag.signals, s)
	}

	var b strings.Builder
	for _, source := range sourceOrder {
		sg := sources[source]
		activeCount := 0
		for _, ag := range sg.accounts {
			for _, s := range ag.signals {
				if s.CompletedAt == nil {
					activeCount++
				}
			}
		}
		fmt.Fprintf(&b, "## %s (%d active)\n\n", capitalize(source), activeCount)
		for _, ag := range sg.accounts {
			if ag.account != "" {
				acctActive := 0
				for _, s := range ag.signals {
					if s.CompletedAt == nil {
						acctActive++
					}
				}
				fmt.Fprintf(&b, "### %s (%d active)\n\n", ag.account, acctActive)
			}
			for _, s := range ag.signals {
				age := formatAge(s.CapturedAt)
				prefix := fmt.Sprintf("- [%d]", s.ID)
				if s.CompletedAt != nil {
					prefix += " ✓"
				}
				urgencyTag := "[pending] "
				if s.Urgency != nil {
					switch *s.Urgency {
					case "urgent":
						urgencyTag = "[urgent] "
					case "review":
						urgencyTag = "[review] "
					case "fyi":
						urgencyTag = "[fyi] "
					}
				}
				if s.Preview != "" {
					fmt.Fprintf(&b, "%s %s%s — %s (%s)\n", prefix, urgencyTag, s.Title, s.Preview, age)
				} else {
					fmt.Fprintf(&b, "%s %s%s (%s)\n", prefix, urgencyTag, s.Title, age)
				}
				if s.Snippet != "" {
					fmt.Fprintf(&b, "  > %s\n", s.Snippet)
				}
				if s.Entity != nil {
					fmt.Fprintf(&b, "  Link: %s [%s]\n", s.Entity.Label, SignalEntityStateLabel(s.Entity))
					fmt.Fprintf(&b, "  URL: %s\n", s.Entity.URL)
				}
			}
		}
		b.WriteString("\n")
	}

	return b.String()
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func formatAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// SignalJSONOutput is the structure for --json output.
type SignalJSONOutput struct {
	ID            int64                       `json:"id"`
	Account       string                      `json:"account,omitempty"`
	Title         string                      `json:"title"`
	Preview       string                      `json:"preview"`
	Snippet       string                      `json:"snippet,omitempty"`
	SourceTS      string                      `json:"source_ts,omitempty"`
	CapturedAt    string                      `json:"captured_at"`
	Active        bool                        `json:"active"`
	Urgency       string                      `json:"urgency,omitempty"`
	UrgencySource string                      `json:"urgency_source,omitempty"`
	LinkedEntity  *SignalEntityLinkJSONOutput `json:"linked_entity,omitempty"`
}

type SignalEntityLinkJSONOutput struct {
	Provider string `json:"provider"`
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Title    string `json:"title,omitempty"`
	URL      string `json:"url"`
	State    string `json:"state,omitempty"`
	Closed   bool   `json:"closed"`
}

// FormatSignalsJSON formats signals grouped by source as JSON.
func FormatSignalsJSON(signals []SignalRecord) (string, error) {
	grouped := make(map[string][]SignalJSONOutput)
	for _, s := range signals {
		out := SignalJSONOutput{
			ID:         s.ID,
			Account:    s.Account,
			Title:      s.Title,
			Preview:    s.Preview,
			Snippet:    s.Snippet,
			SourceTS:   s.SourceTS,
			CapturedAt: s.CapturedAt.Format(time.RFC3339),
			Active:     s.CompletedAt == nil,
		}
		if s.Urgency != nil {
			out.Urgency = *s.Urgency
		}
		if s.UrgencySource != nil {
			out.UrgencySource = *s.UrgencySource
		}
		if s.Entity != nil {
			out.LinkedEntity = &SignalEntityLinkJSONOutput{
				Provider: s.Entity.Provider,
				Kind:     s.Entity.Kind,
				Label:    s.Entity.Label,
				Title:    s.Entity.Title,
				URL:      s.Entity.URL,
				State:    s.Entity.State,
				Closed:   s.Entity.Closed,
			}
		}
		grouped[s.Source] = append(grouped[s.Source], out)
	}
	data, err := json.MarshalIndent(grouped, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}

func SignalEntityStateLabel(link *SignalEntityLink) string {
	if link == nil {
		return ""
	}
	if link.State != "" {
		return link.State
	}
	if link.Closed {
		return "closed"
	}
	return "open"
}
