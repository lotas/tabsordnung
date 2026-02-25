package types

import "time"

// Tab represents a single browser tab.
type Tab struct {
	URL          string
	Title        string
	LastAccessed time.Time
	GroupID      string // empty if ungrouped
	Favicon      string
	WindowIndex  int
	TabIndex     int
	BrowserID    int // live Firefox tab ID; 0 in offline mode
	Pinned       bool

	// Analyzer findings (populated after analysis)
	IsStale      bool
	IsDead       bool
	IsDuplicate  bool
	DeadReason   string // e.g. "404", "timeout", "dns"
	StaleDays    int
	DuplicateOf  []int  // indices of duplicate tabs
	GitHubStatus string           // "open", "closed", "merged", "" (not a GitHub URL)
	GitHubTriage *GitHubTriageInfo // populated by triage analyzer; nil if not a GitHub URL
}

// GitHubTriageInfo holds extended GitHub metadata for triage classification.
type GitHubTriageInfo struct {
	ReviewRequested bool      // current user is a requested reviewer
	Assigned        bool      // current user is an assignee
	UpdatedAt       time.Time // last update time on GitHub
}

// TabGroup represents a Firefox tab group.
type TabGroup struct {
	ID        string
	Name      string
	Color     string
	Collapsed bool
	Tabs      []*Tab
}

// Profile represents a Firefox profile.
type Profile struct {
	Name       string
	Path       string // absolute path to profile directory
	IsDefault  bool
	IsRelative bool
}

// SessionData holds all parsed data from a Firefox session.
type SessionData struct {
	Groups   []*TabGroup
	AllTabs  []*Tab
	Profile  Profile
	ParsedAt time.Time
}

// Stats holds aggregate statistics.
type Stats struct {
	TotalTabs      int
	TotalGroups    int
	StaleTabs      int
	DeadTabs       int
	DuplicateTabs  int
	GitHubDoneTabs int
}

// FilterMode controls which tabs are shown.
type FilterMode int

const (
	FilterAll FilterMode = iota
	FilterStale
	FilterDead
	FilterDuplicate
	FilterAge7
	FilterAge30
	FilterAge90
	FilterGitHubDone
	FilterHasSummary
	FilterNoSummary
)

// SortMode controls tab ordering.
type SortMode int

const (
	SortByGroup SortMode = iota
	SortByAge
	SortByStatus
)

// TabDisplayMode controls what text is shown for each tab in the tree.
type TabDisplayMode int

const (
	TabDisplayURL   TabDisplayMode = iota // show URL (default)
	TabDisplayTitle                       // show page title
	TabDisplayBoth                        // show "Title · URL" truncated
)
