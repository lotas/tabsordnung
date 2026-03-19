package types

import "time"

// Note represents a text note attached to a tab URL.
type Note struct {
	ID           int64
	URL          string
	Body         string
	Source       string    // "tui" or "extension"
	CreatedAt    time.Time
	BrowserTabID int       // live tab ID if created while tab was open; 0 otherwise
}
