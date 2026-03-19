package storage

import (
	"database/sql"
	"time"

	"github.com/lotas/tabsordnung/internal/types"
)

// InsertNote creates a new note and returns its ID.
func InsertNote(db *sql.DB, url, body, source string, browserTabID int) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO tab_notes (url, body, source, browser_tab_id) VALUES (?, ?, ?, ?)`,
		url, body, source, browserTabID,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListNotesByURL returns all notes for a URL, newest first.
func ListNotesByURL(db *sql.DB, url string) ([]types.Note, error) {
	rows, err := db.Query(
		`SELECT id, url, body, source, created_at, browser_tab_id
		 FROM tab_notes WHERE url = ? ORDER BY created_at DESC`, url,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNotes(rows)
}

// ListNotesByTabID returns all notes for a URL, newest first.
// The browser_tab_id is stored for provenance but not used for lookups
// because Firefox reuses tab IDs across sessions.
func ListNotesByTabID(db *sql.DB, tabID int, url string) ([]types.Note, error) {
	return ListNotesByURL(db, url)
}

// HasNotes returns true if there are any notes for the given URL.
func HasNotes(db *sql.DB, url string) bool {
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM tab_notes WHERE url = ?`, url).Scan(&count)
	return count > 0
}

// NoteCountsByURL returns a map of URL -> note count for all URLs that have notes.
func NoteCountsByURL(db *sql.DB) (map[string]int, error) {
	rows, err := db.Query(`SELECT url, COUNT(*) FROM tab_notes GROUP BY url`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var url string
		var count int
		if err := rows.Scan(&url, &count); err != nil {
			return nil, err
		}
		counts[url] = count
	}
	return counts, rows.Err()
}

func scanNotes(rows *sql.Rows) ([]types.Note, error) {
	var notes []types.Note
	for rows.Next() {
		var n types.Note
		var createdAt string
		if err := rows.Scan(&n.ID, &n.URL, &n.Body, &n.Source, &createdAt, &n.BrowserTabID); err != nil {
			return nil, err
		}
		n.CreatedAt = parseTime(createdAt)
		notes = append(notes, n)
	}
	return notes, rows.Err()
}

func parseTime(s string) time.Time {
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05-07:00",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Now()
}
