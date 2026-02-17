package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SignalRecord represents a single signal item stored in the database.
type SignalRecord struct {
	ID            int64
	Source        string
	Title         string
	Preview       string
	Snippet       string
	SourceTS      string
	CapturedAt    time.Time
	CompletedAt   *time.Time
	AutoCompleted bool
	Pinned        bool
}

// InsertSignal inserts a signal, silently ignoring duplicates (same source+title+source_ts).
func InsertSignal(db *sql.DB, sig SignalRecord) error {
	_, err := db.Exec(
		`INSERT OR IGNORE INTO signals (source, title, preview, snippet, source_ts, captured_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sig.Source, sig.Title, sig.Preview, sig.Snippet, sig.SourceTS, sig.CapturedAt,
	)
	return err
}

// ListSignals returns signals. If source is non-empty, filters by source.
// If includeCompleted is false, only returns active signals (completed_at IS NULL).
// Results are ordered: active first (newest captured_at first), then completed (newest completed_at first).
func ListSignals(db *sql.DB, source string, includeCompleted bool) ([]SignalRecord, error) {
	query := `SELECT id, source, title, preview, snippet, source_ts, captured_at, completed_at, auto_completed, pinned
		FROM signals WHERE 1=1`
	var args []interface{}

	if source != "" {
		query += " AND source = ?"
		args = append(args, source)
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
		if err := rows.Scan(&s.ID, &s.Source, &s.Title, &s.Preview, &s.Snippet, &s.SourceTS,
			&s.CapturedAt, &completedAt, &s.AutoCompleted, &s.Pinned); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			s.CompletedAt = &completedAt.Time
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// ActiveSignalCounts returns the number of active (non-completed) signals per source.
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

// ReconcileSignals processes a scrape result for a source in a single transaction:
// 1. Insert new items (dedup via INSERT OR IGNORE)
// 2. Auto-complete signals missing from scrape (unless pinned)
// 3. Reactivate auto-completed signals that reappear
// Manually completed signals (auto_completed=0, completed_at IS NOT NULL) are never reactivated.
func ReconcileSignals(db *sql.DB, source string, items []SignalRecord, capturedAt time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Insert new items.
	insertStmt, err := tx.Prepare(
		`INSERT OR IGNORE INTO signals (source, title, preview, snippet, source_ts, captured_at)
		 VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insertStmt.Close()

	for _, item := range items {
		if _, err := insertStmt.Exec(source, item.Title, item.Preview, item.Snippet, item.SourceTS, capturedAt); err != nil {
			return err
		}
	}

	// Build a set of current item keys for the WHERE NOT IN clause.
	_, err = tx.Exec(`CREATE TEMP TABLE _current_items (title TEXT, source_ts TEXT)`)
	if err != nil {
		return err
	}
	tempStmt, err := tx.Prepare(`INSERT INTO _current_items (title, source_ts) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer tempStmt.Close()
	for _, item := range items {
		if _, err := tempStmt.Exec(item.Title, item.SourceTS); err != nil {
			return err
		}
	}

	// 2. Auto-complete active signals not in current scrape (unless pinned).
	_, err = tx.Exec(`
		UPDATE signals
		SET completed_at = CURRENT_TIMESTAMP, auto_completed = 1
		WHERE source = ? AND completed_at IS NULL AND pinned = 0
		  AND (title, source_ts) NOT IN (SELECT title, source_ts FROM _current_items)`,
		source)
	if err != nil {
		return err
	}

	// 3. Reactivate auto-completed signals that reappear.
	_, err = tx.Exec(`
		UPDATE signals
		SET completed_at = NULL, auto_completed = 0
		WHERE source = ? AND auto_completed = 1 AND completed_at IS NOT NULL
		  AND (title, source_ts) IN (SELECT title, source_ts FROM _current_items)`,
		source)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`DROP TABLE _current_items`)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// FormatSignalsMarkdown formats signals grouped by source as markdown.
func FormatSignalsMarkdown(signals []SignalRecord) string {
	if len(signals) == 0 {
		return "No signals found.\n"
	}

	grouped := make(map[string][]SignalRecord)
	var sourceOrder []string
	for _, s := range signals {
		if _, exists := grouped[s.Source]; !exists {
			sourceOrder = append(sourceOrder, s.Source)
		}
		grouped[s.Source] = append(grouped[s.Source], s)
	}

	var b strings.Builder
	for _, source := range sourceOrder {
		sigs := grouped[source]
		activeCount := 0
		for _, s := range sigs {
			if s.CompletedAt == nil {
				activeCount++
			}
		}
		fmt.Fprintf(&b, "## %s (%d active)\n\n", capitalize(source), activeCount)
		for _, s := range sigs {
			age := formatAge(s.CapturedAt)
			prefix := fmt.Sprintf("- [%d]", s.ID)
			if s.CompletedAt != nil {
				prefix += " ✓"
			}
			if s.Preview != "" {
				fmt.Fprintf(&b, "%s %s — %s (%s)\n", prefix, s.Title, s.Preview, age)
			} else {
				fmt.Fprintf(&b, "%s %s (%s)\n", prefix, s.Title, age)
			}
			if s.Snippet != "" {
				fmt.Fprintf(&b, "  > %s\n", s.Snippet)
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
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Preview    string `json:"preview"`
	Snippet    string `json:"snippet,omitempty"`
	SourceTS   string `json:"source_ts,omitempty"`
	CapturedAt string `json:"captured_at"`
	Active     bool   `json:"active"`
}

// FormatSignalsJSON formats signals grouped by source as JSON.
func FormatSignalsJSON(signals []SignalRecord) (string, error) {
	grouped := make(map[string][]SignalJSONOutput)
	for _, s := range signals {
		grouped[s.Source] = append(grouped[s.Source], SignalJSONOutput{
			ID:         s.ID,
			Title:      s.Title,
			Preview:    s.Preview,
			Snippet:    s.Snippet,
			SourceTS:   s.SourceTS,
			CapturedAt: s.CapturedAt.Format(time.RFC3339),
			Active:     s.CompletedAt == nil,
		})
	}
	data, err := json.MarshalIndent(grouped, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}
