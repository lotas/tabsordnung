package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// TabVisit is a single completed page view, as received from the extension.
type TabVisit struct {
	URL        string
	Title      string
	TabID      int
	StartedAt  int64 // unix ms
	EndedAt    int64 // unix ms
	DurationMs int64
}

// TabVisitSummary aggregates visits for a URL over a time range.
type TabVisitSummary struct {
	URL       string
	Title     string
	Visits    int
	TotalMs   int64
	FirstSeen time.Time
	LastSeen  time.Time
}

// InsertTabVisits bulk-inserts completed visits into tab_visits.
func InsertTabVisits(db *sql.DB, visits []TabVisit) error {
	if len(visits) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO tab_visits (url, title, tab_id, started_at, ended_at, duration_ms) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for _, v := range visits {
		if _, err := stmt.Exec(v.URL, v.Title, v.TabID, v.StartedAt, v.EndedAt, v.DurationMs); err != nil {
			return fmt.Errorf("insert visit %q: %w", v.URL, err)
		}
	}
	return tx.Commit()
}

// QueryTabVisitSummary returns visits aggregated by (url, title) within [from, to),
// sorted by visit count descending.
func QueryTabVisitSummary(db *sql.DB, from, to time.Time) ([]TabVisitSummary, error) {
	rows, err := db.Query(`
		SELECT url, title, COUNT(*) as visits, SUM(duration_ms) as total_ms,
		       MIN(started_at) as first_seen, MAX(started_at) as last_seen
		FROM tab_visits
		WHERE started_at >= ? AND started_at < ?
		GROUP BY url, title
		ORDER BY visits DESC, first_seen ASC`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var result []TabVisitSummary
	for rows.Next() {
		var s TabVisitSummary
		var firstMs, lastMs int64
		if err := rows.Scan(&s.URL, &s.Title, &s.Visits, &s.TotalMs, &firstMs, &lastMs); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		s.FirstSeen = time.UnixMilli(firstMs)
		s.LastSeen = time.UnixMilli(lastMs)
		result = append(result, s)
	}
	return result, rows.Err()
}

// ListSignalsInRange returns signals captured within [from, to).
func ListSignalsInRange(db *sql.DB, from, to time.Time) ([]SignalRecord, error) {
	rows, err := db.Query(`
		SELECT id, source, title, preview, snippet, kind, source_ts, captured_at,
		       completed_at, auto_completed, pinned, urgency, urgency_source
		FROM signals
		WHERE captured_at >= ? AND captured_at < ?
		ORDER BY captured_at ASC`,
		from.Format("2006-01-02T15:04:05"), to.Format("2006-01-02T15:04:05"))
	if err != nil {
		return nil, fmt.Errorf("query signals: %w", err)
	}
	defer rows.Close()

	var result []SignalRecord
	for rows.Next() {
		var s SignalRecord
		var completedAt sql.NullTime
		var urgency, urgencySource sql.NullString
		if err := rows.Scan(&s.ID, &s.Source, &s.Title, &s.Preview, &s.Snippet, &s.Kind,
			&s.SourceTS, &s.CapturedAt, &completedAt, &s.AutoCompleted, &s.Pinned,
			&urgency, &urgencySource); err != nil {
			return nil, fmt.Errorf("scan signal: %w", err)
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
	return result, rows.Err()
}

// FormatHistoryMarkdown renders the activity digest as markdown.
func FormatHistoryMarkdown(visits []TabVisitSummary, signals []SignalRecord, label string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Activity: %s\n\n", label)

	// Tabs section
	totalPages := len(visits)
	var totalMs int64
	for _, v := range visits {
		totalMs += v.TotalMs
	}
	fmt.Fprintf(&b, "## Tabs (%d pages, %s total)\n\n", totalPages, formatDuration(totalMs))
	if totalPages == 0 {
		b.WriteString("No tab activity recorded.\n\n")
	} else {
		b.WriteString("| Visits | Time    | Title                                                  | URL\n")
		b.WriteString("|--------|---------|--------------------------------------------------------|-------------------------------------------------------------\n")
		for _, v := range visits {
			title := v.Title
			if len(title) > 54 {
				title = title[:51] + "..."
			}
			url := v.URL
			if len(url) > 61 {
				url = url[:58] + "..."
			}
			fmt.Fprintf(&b, "| %6d | %-7s | %-54s | %s\n",
				v.Visits, formatDuration(v.TotalMs), title, url)
		}
		b.WriteString("\n")
	}

	// Signals section
	fmt.Fprintf(&b, "## Signals (%d)\n\n", len(signals))
	if len(signals) == 0 {
		b.WriteString("No signals captured.\n")
	} else {
		for _, s := range signals {
			ts := s.CapturedAt.Format("15:04")
			preview := s.Preview
			if preview != "" {
				fmt.Fprintf(&b, "- [%s] %s — %s @ %s\n", s.Source, s.Title, preview, ts)
			} else {
				fmt.Fprintf(&b, "- [%s] %s @ %s\n", s.Source, s.Title, ts)
			}
		}
	}

	return b.String()
}

// HistoryJSONOutput is the JSON representation of the history export.
type HistoryJSONOutput struct {
	Label   string             `json:"label"`
	Tabs    []TabVisitJSONRow  `json:"tabs"`
	Signals []SignalJSONOutput `json:"signals"`
}

// TabVisitJSONRow is one entry in the tabs array.
type TabVisitJSONRow struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Visits    int    `json:"visits"`
	TotalMs   int64  `json:"total_ms"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
}

// FormatHistoryJSON renders the activity digest as JSON.
func FormatHistoryJSON(visits []TabVisitSummary, signals []SignalRecord, label string) (string, error) {
	out := HistoryJSONOutput{Label: label}
	for _, v := range visits {
		out.Tabs = append(out.Tabs, TabVisitJSONRow{
			URL:       v.URL,
			Title:     v.Title,
			Visits:    v.Visits,
			TotalMs:   v.TotalMs,
			FirstSeen: v.FirstSeen.Format(time.RFC3339),
			LastSeen:  v.LastSeen.Format(time.RFC3339),
		})
	}
	for _, s := range signals {
		row := SignalJSONOutput{
			ID:         s.ID,
			Title:      s.Title,
			Preview:    s.Preview,
			Snippet:    s.Snippet,
			SourceTS:   s.SourceTS,
			CapturedAt: s.CapturedAt.Format(time.RFC3339),
			Active:     s.CompletedAt == nil,
		}
		if s.Urgency != nil {
			row.Urgency = *s.Urgency
		}
		out.Signals = append(out.Signals, row)
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}

// formatDuration renders milliseconds as "1h 02m" or "23m" or "45s".
func formatDuration(ms int64) string {
	secs := ms / 1000
	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
