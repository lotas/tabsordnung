# Tabsordnung Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a Go TUI tool that reads Firefox session data and presents tab analytics (stale tabs, dead links, duplicates, group summaries) in an interactive terminal interface.

**Architecture:** Bubble Tea TUI with split-pane layout (tree + detail). Session data read from Firefox's `recovery.jsonlz4` files. Independent analyzers run concurrently and stream results to the TUI via messages.

**Tech Stack:** Go, Bubble Tea, Lip Gloss, Bubbles, pierrec/lz4

---

### Task 1: Project Setup

**Files:**
- Create: `go.mod`
- Create: `main.go`

**Step 1: Initialize Go module**

Run: `go mod init github.com/nickel-chromium/tabsordnung`

**Step 2: Create minimal main.go**

```go
package main

import "fmt"

func main() {
	fmt.Println("tabsordnung")
}
```

**Step 3: Verify it compiles**

Run: `go build -o tabsordnung .`
Expected: Binary created, no errors.

**Step 4: Install dependencies**

Run:
```bash
go get github.com/charmbracelet/bubbletea
go get github.com/charmbracelet/lipgloss
go get github.com/charmbracelet/bubbles
go get github.com/pierrec/lz4/v4
```

**Step 5: Commit**

```bash
git add go.mod go.sum main.go
git commit -m "feat: initialize Go project with dependencies"
```

---

### Task 2: Shared Types

**Files:**
- Create: `internal/types/types.go`

**Step 1: Write types**

```go
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

	// Analyzer findings (populated after analysis)
	IsStale     bool
	IsDead      bool
	IsDuplicate bool
	DeadReason  string // e.g. "404", "timeout", "dns"
	StaleDays   int
	DuplicateOf []int // indices of duplicate tabs
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
	Groups     []*TabGroup
	AllTabs    []*Tab
	Profile    Profile
	ParsedAt   time.Time
}

// Stats holds aggregate statistics.
type Stats struct {
	TotalTabs   int
	TotalGroups int
	StaleTabs   int
	DeadTabs    int
	DuplicateTabs int
}

// FilterMode controls which tabs are shown.
type FilterMode int

const (
	FilterAll FilterMode = iota
	FilterStale
	FilterDead
	FilterDuplicate
)

// SortMode controls tab ordering.
type SortMode int

const (
	SortByGroup SortMode = iota
	SortByAge
	SortByStatus
)
```

**Step 2: Verify it compiles**

Run: `go build ./internal/types/`
Expected: No errors.

**Step 3: Commit**

```bash
git add internal/types/types.go
git commit -m "feat: add shared types for tabs, groups, profiles"
```

---

### Task 3: Firefox Profile Discovery

**Files:**
- Create: `internal/firefox/profiles.go`
- Create: `internal/firefox/profiles_test.go`

**Step 1: Write the failing test**

```go
package firefox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseProfilesINI(t *testing.T) {
	dir := t.TempDir()
	iniContent := `[General]
StartWithLastProfile=1
Version=2

[Profile0]
Name=default-release
IsRelative=1
Path=abc123.default-release
Default=1

[Profile1]
Name=dev-edition
IsRelative=0
Path=/absolute/path/to/profile
Default=0

[Install308046B0AF4A39CB]
Default=abc123.default-release
Locked=1
`
	iniPath := filepath.Join(dir, "profiles.ini")
	os.WriteFile(iniPath, []byte(iniContent), 0644)

	// Create the relative profile dir so it "exists"
	os.MkdirAll(filepath.Join(dir, "abc123.default-release"), 0755)

	profiles, err := ParseProfilesINI(iniPath, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(profiles))
	}

	// First profile: relative path
	if profiles[0].Name != "default-release" {
		t.Errorf("expected name 'default-release', got %q", profiles[0].Name)
	}
	if profiles[0].Path != filepath.Join(dir, "abc123.default-release") {
		t.Errorf("expected resolved path, got %q", profiles[0].Path)
	}
	if !profiles[0].IsDefault {
		t.Error("expected profile 0 to be default")
	}

	// Second profile: absolute path
	if profiles[1].Name != "dev-edition" {
		t.Errorf("expected name 'dev-edition', got %q", profiles[1].Name)
	}
	if profiles[1].Path != "/absolute/path/to/profile" {
		t.Errorf("expected absolute path, got %q", profiles[1].Path)
	}
	if profiles[1].IsDefault {
		t.Error("expected profile 1 to not be default")
	}
}

func TestFindFirefoxDir(t *testing.T) {
	// Test that FindFirefoxDir returns a path (may not exist in CI)
	dir := FindFirefoxDir()
	if dir == "" {
		t.Skip("no Firefox directory found on this system")
	}
	t.Logf("found Firefox dir: %s", dir)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/firefox/ -run TestParseProfilesINI -v`
Expected: FAIL — `ParseProfilesINI` not defined.

**Step 3: Write the implementation**

```go
package firefox

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// FindFirefoxDir returns the platform-specific Firefox profile directory.
func FindFirefoxDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "linux":
		return filepath.Join(home, ".mozilla", "firefox")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Firefox")
	default:
		return ""
	}
}

// ParseProfilesINI reads profiles.ini and returns all profiles found.
// firefoxDir is the directory containing profiles.ini (used to resolve relative paths).
func ParseProfilesINI(iniPath, firefoxDir string) ([]types.Profile, error) {
	f, err := os.Open(iniPath)
	if err != nil {
		return nil, fmt.Errorf("open profiles.ini: %w", err)
	}
	defer f.Close()

	var profiles []types.Profile
	var current *types.Profile

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Section header
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			// Save previous profile if any
			if current != nil {
				profiles = append(profiles, *current)
				current = nil
			}
			section := line[1 : len(line)-1]
			if strings.HasPrefix(section, "Profile") {
				current = &types.Profile{}
			}
			continue
		}

		if current == nil {
			continue
		}

		// Key=Value
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, value := parts[0], parts[1]

		switch key {
		case "Name":
			current.Name = value
		case "Path":
			current.Path = value
		case "IsRelative":
			current.IsRelative = value == "1"
		case "Default":
			current.IsDefault = value == "1"
		}
	}

	// Don't forget the last profile
	if current != nil {
		profiles = append(profiles, *current)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan profiles.ini: %w", err)
	}

	// Resolve relative paths
	for i := range profiles {
		if profiles[i].IsRelative {
			profiles[i].Path = filepath.Join(firefoxDir, profiles[i].Path)
		}
	}

	return profiles, nil
}

// DiscoverProfiles finds and parses Firefox profiles on this system.
func DiscoverProfiles() ([]types.Profile, error) {
	dir := FindFirefoxDir()
	if dir == "" {
		return nil, fmt.Errorf("could not find Firefox directory for %s", runtime.GOOS)
	}
	iniPath := filepath.Join(dir, "profiles.ini")
	return ParseProfilesINI(iniPath, dir)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/firefox/ -run TestParseProfilesINI -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/firefox/profiles.go internal/firefox/profiles_test.go
git commit -m "feat: add Firefox profile discovery and INI parsing"
```

---

### Task 4: mozlz4 Decompression and Session Parsing

**Files:**
- Create: `internal/firefox/session.go`
- Create: `internal/firefox/session_test.go`

**Step 1: Write the failing test for mozlz4 decompression**

```go
package firefox

import (
	"encoding/binary"
	"testing"

	"github.com/pierrec/lz4/v4"
)

func TestDecompressMozLz4(t *testing.T) {
	// Create a valid mozlz4 payload
	original := []byte(`{"version":["sessionrestore",1],"windows":[]}`)

	// Compress with lz4 block
	compressed := make([]byte, lz4.CompressBlockBound(len(original)))
	n, err := lz4.CompressBlock(original, compressed, nil)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	compressed = compressed[:n]

	// Build mozlz4 file: magic(8) + size(4) + compressed data
	mozlz4 := make([]byte, 0, 12+len(compressed))
	mozlz4 = append(mozlz4, []byte("mozLz40\x00")...)
	sizeBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(sizeBuf, uint32(len(original)))
	mozlz4 = append(mozlz4, sizeBuf...)
	mozlz4 = append(mozlz4, compressed...)

	result, err := DecompressMozLz4(mozlz4)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if string(result) != string(original) {
		t.Errorf("expected %q, got %q", original, result)
	}
}

func TestDecompressMozLz4_InvalidHeader(t *testing.T) {
	_, err := DecompressMozLz4([]byte("not a mozlz4 file"))
	if err == nil {
		t.Error("expected error for invalid header")
	}
}

func TestDecompressMozLz4_TooShort(t *testing.T) {
	_, err := DecompressMozLz4([]byte("mozLz40"))
	if err == nil {
		t.Error("expected error for too-short data")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/firefox/ -run TestDecompressMozLz4 -v`
Expected: FAIL — `DecompressMozLz4` not defined.

**Step 3: Write the failing test for session parsing**

```go
func TestParseSession(t *testing.T) {
	sessionJSON := []byte(`{
		"version": ["sessionrestore", 1],
		"windows": [{
			"tabs": [
				{
					"entries": [
						{"url": "https://github.com/org/repo/pull/42", "title": "PR #42"}
					],
					"index": 1,
					"lastAccessed": 1707654321000,
					"image": "https://github.com/favicon.ico",
					"group": "group-1"
				},
				{
					"entries": [
						{"url": "about:newtab", "title": "New Tab"},
						{"url": "https://stackoverflow.com/q/12345", "title": "How to parse LZ4?"}
					],
					"index": 2,
					"lastAccessed": 1706000000000
				}
			],
			"groups": [
				{
					"id": "group-1",
					"name": "Work",
					"color": "blue",
					"collapsed": false
				}
			]
		}]
	}`)

	data, err := ParseSession(sessionJSON)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Should have 2 groups: "Work" + "Ungrouped"
	if len(data.Groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(data.Groups))
	}

	// Check total tabs
	if len(data.AllTabs) != 2 {
		t.Fatalf("expected 2 tabs, got %d", len(data.AllTabs))
	}

	// First tab should use entries[index-1]
	tab0 := data.AllTabs[0]
	if tab0.URL != "https://github.com/org/repo/pull/42" {
		t.Errorf("tab 0 URL: got %q", tab0.URL)
	}
	if tab0.Title != "PR #42" {
		t.Errorf("tab 0 title: got %q", tab0.Title)
	}
	if tab0.GroupID != "group-1" {
		t.Errorf("tab 0 group: got %q", tab0.GroupID)
	}

	// Second tab: index=2 means entries[1]
	tab1 := data.AllTabs[1]
	if tab1.URL != "https://stackoverflow.com/q/12345" {
		t.Errorf("tab 1 URL: got %q", tab1.URL)
	}

	// Work group should have 1 tab
	workGroup := data.Groups[0]
	if workGroup.Name != "Work" {
		t.Errorf("expected group 'Work', got %q", workGroup.Name)
	}
	if len(workGroup.Tabs) != 1 {
		t.Errorf("expected 1 tab in Work, got %d", len(workGroup.Tabs))
	}

	// Ungrouped should have 1 tab
	ungrouped := data.Groups[1]
	if ungrouped.Name != "Ungrouped" {
		t.Errorf("expected 'Ungrouped', got %q", ungrouped.Name)
	}
	if len(ungrouped.Tabs) != 1 {
		t.Errorf("expected 1 tab in Ungrouped, got %d", len(ungrouped.Tabs))
	}
}
```

**Step 4: Write the implementation**

```go
package firefox

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/types"
	"github.com/pierrec/lz4/v4"
)

const mozLz4Magic = "mozLz40\x00"

// DecompressMozLz4 decompresses a Mozilla-specific lz4 file (.jsonlz4).
func DecompressMozLz4(data []byte) ([]byte, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("mozlz4: data too short (%d bytes)", len(data))
	}
	if string(data[:8]) != mozLz4Magic {
		return nil, fmt.Errorf("mozlz4: invalid magic header")
	}

	uncompressedSize := binary.LittleEndian.Uint32(data[8:12])
	dst := make([]byte, uncompressedSize)

	n, err := lz4.UncompressBlock(data[12:], dst)
	if err != nil {
		return nil, fmt.Errorf("mozlz4: decompress: %w", err)
	}
	return dst[:n], nil
}

// Raw JSON types for Firefox session format.
type rawSession struct {
	Windows []rawWindow `json:"windows"`
}

type rawWindow struct {
	Tabs   []rawTab   `json:"tabs"`
	Groups []rawGroup `json:"groups"`
}

type rawTab struct {
	Entries      []rawEntry `json:"entries"`
	Index        int        `json:"index"` // 1-based
	LastAccessed int64      `json:"lastAccessed"`
	Image        string     `json:"image"`
	Group        string     `json:"group"`
}

type rawEntry struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

type rawGroup struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	Collapsed bool   `json:"collapsed"`
}

// ParseSession parses decompressed Firefox session JSON into structured data.
func ParseSession(data []byte) (*types.SessionData, error) {
	var raw rawSession
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse session JSON: %w", err)
	}

	result := &types.SessionData{
		ParsedAt: time.Now(),
	}

	// Collect groups across all windows, keyed by ID
	groupMap := make(map[string]*types.TabGroup)
	var groupOrder []string

	for _, w := range raw.Windows {
		for _, g := range w.Groups {
			if _, exists := groupMap[g.ID]; !exists {
				groupMap[g.ID] = &types.TabGroup{
					ID:        g.ID,
					Name:      g.Name,
					Color:     g.Color,
					Collapsed: g.Collapsed,
				}
				groupOrder = append(groupOrder, g.ID)
			}
		}
	}

	// Parse tabs
	var ungroupedTabs []*types.Tab

	for wi, w := range raw.Windows {
		for ti, t := range w.Tabs {
			if len(t.Entries) == 0 {
				continue
			}

			// Current page is at entries[index-1] (index is 1-based)
			entryIdx := t.Index - 1
			if entryIdx < 0 || entryIdx >= len(t.Entries) {
				entryIdx = len(t.Entries) - 1
			}
			entry := t.Entries[entryIdx]

			tab := &types.Tab{
				URL:          entry.URL,
				Title:        entry.Title,
				LastAccessed: time.UnixMilli(t.LastAccessed),
				GroupID:      t.Group,
				Favicon:      t.Image,
				WindowIndex:  wi,
				TabIndex:     ti,
			}
			result.AllTabs = append(result.AllTabs, tab)

			if t.Group != "" {
				if g, ok := groupMap[t.Group]; ok {
					g.Tabs = append(g.Tabs, tab)
				} else {
					ungroupedTabs = append(ungroupedTabs, tab)
				}
			} else {
				ungroupedTabs = append(ungroupedTabs, tab)
			}
		}
	}

	// Build ordered group list
	for _, id := range groupOrder {
		result.Groups = append(result.Groups, groupMap[id])
	}

	// Add ungrouped tabs as a virtual group
	if len(ungroupedTabs) > 0 {
		result.Groups = append(result.Groups, &types.TabGroup{
			ID:   "",
			Name: "Ungrouped",
			Tabs: ungroupedTabs,
		})
	}

	return result, nil
}

// ReadSessionFile reads and parses a session file from a profile directory.
func ReadSessionFile(profileDir string) (*types.SessionData, error) {
	path := profileDir + "/sessionstore-backups/recovery.jsonlz4"
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}

	jsonData, err := DecompressMozLz4(data)
	if err != nil {
		return nil, err
	}

	return ParseSession(jsonData)
}
```

Note: `ReadSessionFile` uses `os`, so add `"os"` to the import list.

**Step 5: Run all tests**

Run: `go test ./internal/firefox/ -v`
Expected: All tests pass.

**Step 6: Commit**

```bash
git add internal/firefox/session.go internal/firefox/session_test.go
git commit -m "feat: add mozlz4 decompression and session file parsing"
```

---

### Task 5: Stale Tabs Analyzer

**Files:**
- Create: `internal/analyzer/stale.go`
- Create: `internal/analyzer/stale_test.go`

**Step 1: Write the failing test**

```go
package analyzer

import (
	"testing"
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/types"
)

func TestAnalyzeStale(t *testing.T) {
	now := time.Now()
	tabs := []*types.Tab{
		{URL: "https://fresh.com", LastAccessed: now.Add(-1 * time.Hour)},
		{URL: "https://stale.com", LastAccessed: now.Add(-10 * 24 * time.Hour)},
		{URL: "https://very-stale.com", LastAccessed: now.Add(-30 * 24 * time.Hour)},
	}

	AnalyzeStale(tabs, 7)

	if tabs[0].IsStale {
		t.Error("fresh tab should not be stale")
	}
	if !tabs[1].IsStale {
		t.Error("10-day tab should be stale")
	}
	if tabs[1].StaleDays != 10 {
		t.Errorf("expected 10 stale days, got %d", tabs[1].StaleDays)
	}
	if !tabs[2].IsStale {
		t.Error("30-day tab should be stale")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/analyzer/ -run TestAnalyzeStale -v`
Expected: FAIL

**Step 3: Write the implementation**

```go
package analyzer

import (
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// AnalyzeStale marks tabs as stale if not accessed within thresholdDays.
func AnalyzeStale(tabs []*types.Tab, thresholdDays int) {
	threshold := time.Duration(thresholdDays) * 24 * time.Hour
	now := time.Now()

	for _, tab := range tabs {
		age := now.Sub(tab.LastAccessed)
		if age > threshold {
			tab.IsStale = true
			tab.StaleDays = int(age.Hours() / 24)
		}
	}
}
```

**Step 4: Run test**

Run: `go test ./internal/analyzer/ -run TestAnalyzeStale -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/analyzer/stale.go internal/analyzer/stale_test.go
git commit -m "feat: add stale tabs analyzer"
```

---

### Task 6: Duplicate Tabs Analyzer

**Files:**
- Create: `internal/analyzer/duplicates.go`
- Create: `internal/analyzer/duplicates_test.go`

**Step 1: Write the failing test**

```go
package analyzer

import (
	"testing"

	"github.com/nickel-chromium/tabsordnung/internal/types"
)

func TestAnalyzeDuplicates(t *testing.T) {
	tabs := []*types.Tab{
		{URL: "https://example.com/page#section1"},
		{URL: "https://example.com/page#section2"},  // same after stripping fragment
		{URL: "https://example.com/other"},
		{URL: "https://example.com/page?b=2&a=1"},
		{URL: "https://example.com/page?a=1&b=2"},  // same after sorting params
	}

	AnalyzeDuplicates(tabs)

	// tabs[0] and tabs[1] are duplicates (same URL after fragment strip)
	if !tabs[0].IsDuplicate {
		t.Error("tab 0 should be duplicate")
	}
	if !tabs[1].IsDuplicate {
		t.Error("tab 1 should be duplicate")
	}

	// tab[2] is unique
	if tabs[2].IsDuplicate {
		t.Error("tab 2 should not be duplicate")
	}

	// tabs[3] and tabs[4] are duplicates (same after param sort)
	if !tabs[3].IsDuplicate {
		t.Error("tab 3 should be duplicate")
	}
	if !tabs[4].IsDuplicate {
		t.Error("tab 4 should be duplicate")
	}
}

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://example.com/page#section", "https://example.com/page"},
		{"https://example.com/page/", "https://example.com/page"},
		{"https://example.com/page?b=2&a=1", "https://example.com/page?a=1&b=2"},
		{"https://example.com", "https://example.com"},
	}

	for _, tt := range tests {
		got := NormalizeURL(tt.input)
		if got != tt.expected {
			t.Errorf("NormalizeURL(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/analyzer/ -run TestAnalyzeDuplicates -v`
Expected: FAIL

**Step 3: Write the implementation**

```go
package analyzer

import (
	"net/url"
	"sort"
	"strings"

	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// NormalizeURL strips fragments, sorts query params, and removes trailing slashes.
func NormalizeURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	// Strip fragment
	u.Fragment = ""

	// Sort query parameters
	params := u.Query()
	for k := range params {
		sort.Strings(params[k])
	}
	u.RawQuery = params.Encode()

	result := u.String()

	// Remove trailing slash (but not for root path)
	if strings.HasSuffix(result, "/") && result != u.Scheme+"://"+u.Host+"/" {
		result = strings.TrimRight(result, "/")
	}

	return result
}

// AnalyzeDuplicates marks tabs with duplicate URLs.
func AnalyzeDuplicates(tabs []*types.Tab) {
	// Group tab indices by normalized URL
	groups := make(map[string][]int)
	for i, tab := range tabs {
		normalized := NormalizeURL(tab.URL)
		groups[normalized] = append(groups[normalized], i)
	}

	// Mark duplicates
	for _, indices := range groups {
		if len(indices) < 2 {
			continue
		}
		for _, i := range indices {
			tabs[i].IsDuplicate = true
			// Store indices of the other duplicates
			var others []int
			for _, j := range indices {
				if j != i {
					others = append(others, j)
				}
			}
			tabs[i].DuplicateOf = others
		}
	}
}
```

**Step 4: Run tests**

Run: `go test ./internal/analyzer/ -run "TestAnalyzeDuplicates|TestNormalizeURL" -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/analyzer/duplicates.go internal/analyzer/duplicates_test.go
git commit -m "feat: add duplicate tabs analyzer with URL normalization"
```

---

### Task 7: Dead Links Analyzer

**Files:**
- Create: `internal/analyzer/deadlinks.go`
- Create: `internal/analyzer/deadlinks_test.go`

**Step 1: Write the failing test**

```go
package analyzer

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nickel-chromium/tabsordnung/internal/types"
)

func TestAnalyzeDeadLinks(t *testing.T) {
	// Set up test HTTP servers
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer okServer.Close()

	notFoundServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer notFoundServer.Close()

	goneServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(410)
	}))
	defer goneServer.Close()

	tabs := []*types.Tab{
		{URL: okServer.URL + "/page"},
		{URL: notFoundServer.URL + "/missing"},
		{URL: goneServer.URL + "/gone"},
		{URL: "about:newtab"},                // should be skipped
		{URL: "moz-extension://abc/page"},     // should be skipped
	}

	results := make(chan DeadLinkResult, len(tabs))
	AnalyzeDeadLinks(tabs, results)
	close(results)

	for r := range results {
		_ = r // drain
	}

	if tabs[0].IsDead {
		t.Error("200 tab should not be dead")
	}
	if !tabs[1].IsDead {
		t.Error("404 tab should be dead")
	}
	if tabs[1].DeadReason != "404" {
		t.Errorf("expected reason '404', got %q", tabs[1].DeadReason)
	}
	if !tabs[2].IsDead {
		t.Error("410 tab should be dead")
	}
	if tabs[3].IsDead {
		t.Error("about: tab should not be checked")
	}
	if tabs[4].IsDead {
		t.Error("moz-extension: tab should not be checked")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/analyzer/ -run TestAnalyzeDeadLinks -v`
Expected: FAIL

**Step 3: Write the implementation**

```go
package analyzer

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// DeadLinkResult reports the outcome of checking a single tab.
type DeadLinkResult struct {
	TabIndex int
	IsDead   bool
	Reason   string
}

var skipPrefixes = []string{"about:", "moz-extension:", "file:", "chrome:", "resource:", "data:"}

func shouldSkip(url string) bool {
	for _, prefix := range skipPrefixes {
		if strings.HasPrefix(url, prefix) {
			return true
		}
	}
	return false
}

// AnalyzeDeadLinks checks tabs for dead links concurrently.
// Results are sent to the channel as they complete, allowing progressive TUI updates.
func AnalyzeDeadLinks(tabs []*types.Tab, results chan<- DeadLinkResult) {
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	sem := make(chan struct{}, 10) // concurrency limit
	var wg sync.WaitGroup

	for i, tab := range tabs {
		if shouldSkip(tab.URL) {
			continue
		}

		wg.Add(1)
		go func(idx int, t *types.Tab) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result := DeadLinkResult{TabIndex: idx}

			req, err := http.NewRequest(http.MethodHead, t.URL, nil)
			if err != nil {
				result.IsDead = true
				result.Reason = "invalid URL"
				t.IsDead = true
				t.DeadReason = result.Reason
				results <- result
				return
			}

			resp, err := client.Do(req)
			if err != nil {
				result.IsDead = true
				result.Reason = "unreachable"
				t.IsDead = true
				t.DeadReason = result.Reason
				results <- result
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode == 404 || resp.StatusCode == 410 {
				result.IsDead = true
				result.Reason = fmt.Sprintf("%d", resp.StatusCode)
				t.IsDead = true
				t.DeadReason = result.Reason
			}

			results <- result
		}(i, tab)
	}

	wg.Wait()
}
```

**Step 4: Run tests**

Run: `go test ./internal/analyzer/ -run TestAnalyzeDeadLinks -v -timeout 30s`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/analyzer/deadlinks.go internal/analyzer/deadlinks_test.go
git commit -m "feat: add dead links analyzer with concurrent HTTP checks"
```

---

### Task 8: Group Summary

**Files:**
- Create: `internal/analyzer/summary.go`
- Create: `internal/analyzer/summary_test.go`

**Step 1: Write the failing test**

```go
package analyzer

import (
	"testing"

	"github.com/nickel-chromium/tabsordnung/internal/types"
)

func TestComputeStats(t *testing.T) {
	data := &types.SessionData{
		AllTabs: []*types.Tab{
			{IsStale: true},
			{IsDead: true},
			{IsDuplicate: true},
			{IsStale: true, IsDead: true},
			{},
		},
		Groups: []*types.TabGroup{
			{Name: "A"},
			{Name: "B"},
		},
	}

	stats := ComputeStats(data)
	if stats.TotalTabs != 5 {
		t.Errorf("total tabs: got %d, want 5", stats.TotalTabs)
	}
	if stats.TotalGroups != 2 {
		t.Errorf("total groups: got %d, want 2", stats.TotalGroups)
	}
	if stats.StaleTabs != 2 {
		t.Errorf("stale: got %d, want 2", stats.StaleTabs)
	}
	if stats.DeadTabs != 2 {
		t.Errorf("dead: got %d, want 2", stats.DeadTabs)
	}
	if stats.DuplicateTabs != 1 {
		t.Errorf("duplicate: got %d, want 1", stats.DuplicateTabs)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/analyzer/ -run TestComputeStats -v`
Expected: FAIL

**Step 3: Write the implementation**

```go
package analyzer

import "github.com/nickel-chromium/tabsordnung/internal/types"

// ComputeStats calculates aggregate statistics from session data.
func ComputeStats(data *types.SessionData) types.Stats {
	stats := types.Stats{
		TotalTabs:   len(data.AllTabs),
		TotalGroups: len(data.Groups),
	}
	for _, tab := range data.AllTabs {
		if tab.IsStale {
			stats.StaleTabs++
		}
		if tab.IsDead {
			stats.DeadTabs++
		}
		if tab.IsDuplicate {
			stats.DuplicateTabs++
		}
	}
	return stats
}
```

**Step 4: Run tests**

Run: `go test ./internal/analyzer/ -v`
Expected: All pass.

**Step 5: Commit**

```bash
git add internal/analyzer/summary.go internal/analyzer/summary_test.go
git commit -m "feat: add stats computation for session data"
```

---

### Task 9: TUI — Tree Component

**Files:**
- Create: `internal/tui/tree.go`

This is the collapsible group/tab tree for the left pane. It manages a flat list of visible nodes (groups and their tabs when expanded), cursor position, and scrolling.

**Step 1: Write the implementation**

```go
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// TreeNode represents a visible row in the tree.
type TreeNode struct {
	Group *types.TabGroup // non-nil for group headers
	Tab   *types.Tab      // non-nil for tab rows
}

// TreeModel manages the collapsible tree view.
type TreeModel struct {
	Groups   []*types.TabGroup
	Expanded map[string]bool // group ID -> expanded
	Cursor   int
	Offset   int // scroll offset
	Width    int
	Height   int
	Filter   types.FilterMode
}

func NewTreeModel(groups []*types.TabGroup) TreeModel {
	expanded := make(map[string]bool)
	for _, g := range groups {
		expanded[g.ID] = !g.Collapsed
	}
	return TreeModel{
		Groups:   groups,
		Expanded: expanded,
	}
}

// VisibleNodes returns the flat list of currently visible nodes.
func (m TreeModel) VisibleNodes() []TreeNode {
	var nodes []TreeNode
	for _, g := range m.Groups {
		nodes = append(nodes, TreeNode{Group: g})
		if m.Expanded[g.ID] {
			for _, tab := range g.Tabs {
				if m.matchesFilter(tab) {
					nodes = append(nodes, TreeNode{Tab: tab})
				}
			}
		}
	}
	return nodes
}

func (m TreeModel) matchesFilter(tab *types.Tab) bool {
	switch m.Filter {
	case types.FilterStale:
		return tab.IsStale
	case types.FilterDead:
		return tab.IsDead
	case types.FilterDuplicate:
		return tab.IsDuplicate
	default:
		return true
	}
}

// SelectedNode returns the currently selected node, or nil.
func (m TreeModel) SelectedNode() *TreeNode {
	nodes := m.VisibleNodes()
	if m.Cursor >= 0 && m.Cursor < len(nodes) {
		return &nodes[m.Cursor]
	}
	return nil
}

// MoveUp moves the cursor up.
func (m *TreeModel) MoveUp() {
	if m.Cursor > 0 {
		m.Cursor--
	}
	if m.Cursor < m.Offset {
		m.Offset = m.Cursor
	}
}

// MoveDown moves the cursor down.
func (m *TreeModel) MoveDown() {
	nodes := m.VisibleNodes()
	if m.Cursor < len(nodes)-1 {
		m.Cursor++
	}
	visibleRows := m.Height - 2 // account for padding
	if visibleRows < 1 {
		visibleRows = 1
	}
	if m.Cursor >= m.Offset+visibleRows {
		m.Offset = m.Cursor - visibleRows + 1
	}
}

// Toggle expands/collapses the selected group.
func (m *TreeModel) Toggle() {
	node := m.SelectedNode()
	if node == nil || node.Group == nil {
		return
	}
	m.Expanded[node.Group.ID] = !m.Expanded[node.Group.ID]
}

// View renders the tree.
func (m TreeModel) View() string {
	nodes := m.VisibleNodes()
	if len(nodes) == 0 {
		return "No tabs found."
	}

	visibleRows := m.Height
	if visibleRows < 1 {
		visibleRows = 20
	}

	var b strings.Builder
	end := m.Offset + visibleRows
	if end > len(nodes) {
		end = len(nodes)
	}

	cursorStyle := lipgloss.NewStyle().Bold(true).Reverse(true)
	staleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))  // orange
	deadStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))   // red
	dupStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("33"))     // blue
	groupStyle := lipgloss.NewStyle().Bold(true)

	for i := m.Offset; i < end; i++ {
		node := nodes[i]
		var line string

		if node.Group != nil {
			icon := "▶"
			if m.Expanded[node.Group.ID] {
				icon = "▼"
			}
			label := fmt.Sprintf("%s %s (%d tabs)", icon, node.Group.Name, len(node.Group.Tabs))
			line = groupStyle.Render(label)
		} else if node.Tab != nil {
			prefix := "  "
			var markers []string
			if node.Tab.IsDead {
				markers = append(markers, deadStyle.Render("●"))
			}
			if node.Tab.IsStale {
				markers = append(markers, staleStyle.Render("◷"))
			}
			if node.Tab.IsDuplicate {
				markers = append(markers, dupStyle.Render("⇄"))
			}

			marker := ""
			if len(markers) > 0 {
				marker = strings.Join(markers, "") + " "
			}

			// Truncate URL to fit width
			maxURLLen := m.Width - len(prefix) - len(marker) - 2
			if maxURLLen < 10 {
				maxURLLen = 10
			}
			url := node.Tab.URL
			if len(url) > maxURLLen {
				url = url[:maxURLLen-1] + "…"
			}
			line = prefix + marker + url
		}

		// Apply cursor highlight
		if i == m.Cursor {
			// Pad to full width for highlight
			for len(line) < m.Width {
				line += " "
			}
			line = cursorStyle.Render(line)
		}

		b.WriteString(line)
		if i < end-1 {
			b.WriteString("\n")
		}
	}

	return b.String()
}
```

**Step 2: Verify it compiles**

Run: `go build ./internal/tui/`
Expected: No errors.

**Step 3: Commit**

```bash
git add internal/tui/tree.go
git commit -m "feat: add collapsible tree component for tab groups"
```

---

### Task 10: TUI — Detail Pane

**Files:**
- Create: `internal/tui/detail.go`

**Step 1: Write the implementation**

```go
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// DetailModel shows information about the selected item.
type DetailModel struct {
	Width  int
	Height int
}

func (m DetailModel) ViewTab(tab *types.Tab) string {
	if tab == nil {
		return ""
	}

	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	valueStyle := lipgloss.NewStyle()
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	staleWarnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)

	var b strings.Builder

	b.WriteString(labelStyle.Render("Title") + "\n")
	title := tab.Title
	if len(title) > m.Width-2 {
		title = title[:m.Width-3] + "…"
	}
	b.WriteString(valueStyle.Render(title) + "\n\n")

	b.WriteString(labelStyle.Render("URL") + "\n")
	url := tab.URL
	// Wrap long URLs
	for len(url) > m.Width-2 {
		b.WriteString(valueStyle.Render(url[:m.Width-2]) + "\n")
		url = url[m.Width-2:]
	}
	b.WriteString(valueStyle.Render(url) + "\n\n")

	b.WriteString(labelStyle.Render("Last Visited") + "\n")
	age := time.Since(tab.LastAccessed)
	days := int(age.Hours() / 24)
	var ageStr string
	if days == 0 {
		hours := int(age.Hours())
		if hours == 0 {
			ageStr = "just now"
		} else {
			ageStr = fmt.Sprintf("%d hours ago", hours)
		}
	} else {
		ageStr = fmt.Sprintf("%d days ago", days)
	}
	b.WriteString(valueStyle.Render(ageStr) + "\n\n")

	// Status section
	var statuses []string
	if tab.IsDead {
		statuses = append(statuses, warnStyle.Render(fmt.Sprintf("Dead link (%s)", tab.DeadReason)))
	}
	if tab.IsStale {
		statuses = append(statuses, staleWarnStyle.Render(fmt.Sprintf("Stale (%d days)", tab.StaleDays)))
	}
	if tab.IsDuplicate {
		statuses = append(statuses, lipgloss.NewStyle().
			Foreground(lipgloss.Color("33")).Bold(true).
			Render(fmt.Sprintf("Duplicate (%d copies)", len(tab.DuplicateOf)+1)))
	}

	if len(statuses) > 0 {
		b.WriteString(labelStyle.Render("Status") + "\n")
		for _, s := range statuses {
			b.WriteString(s + "\n")
		}
	}

	return b.String()
}

func (m DetailModel) ViewGroup(group *types.TabGroup) string {
	if group == nil {
		return ""
	}

	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	valueStyle := lipgloss.NewStyle()

	var b strings.Builder

	b.WriteString(labelStyle.Render("Group") + "\n")
	b.WriteString(valueStyle.Render(group.Name) + "\n\n")

	b.WriteString(labelStyle.Render("Tabs") + "\n")
	b.WriteString(valueStyle.Render(fmt.Sprintf("%d", len(group.Tabs))) + "\n\n")

	b.WriteString(labelStyle.Render("Color") + "\n")
	b.WriteString(valueStyle.Render(group.Color) + "\n\n")

	state := "expanded"
	if group.Collapsed {
		state = "collapsed"
	}
	b.WriteString(labelStyle.Render("State") + "\n")
	b.WriteString(valueStyle.Render(state) + "\n")

	// Count issues in group
	var stale, dead, dup int
	for _, tab := range group.Tabs {
		if tab.IsStale {
			stale++
		}
		if tab.IsDead {
			dead++
		}
		if tab.IsDuplicate {
			dup++
		}
	}

	if stale+dead+dup > 0 {
		b.WriteString("\n" + labelStyle.Render("Issues") + "\n")
		if dead > 0 {
			b.WriteString(fmt.Sprintf("  %d dead links\n", dead))
		}
		if stale > 0 {
			b.WriteString(fmt.Sprintf("  %d stale tabs\n", stale))
		}
		if dup > 0 {
			b.WriteString(fmt.Sprintf("  %d duplicates\n", dup))
		}
	}

	return b.String()
}
```

**Step 2: Verify it compiles**

Run: `go build ./internal/tui/`
Expected: No errors.

**Step 3: Commit**

```bash
git add internal/tui/detail.go
git commit -m "feat: add detail pane component for tab/group info"
```

---

### Task 11: TUI — Profile Picker

**Files:**
- Create: `internal/tui/profile_picker.go`

**Step 1: Write the implementation**

```go
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// ProfilePicker is an overlay for selecting a Firefox profile.
type ProfilePicker struct {
	Profiles []types.Profile
	Cursor   int
	Width    int
	Height   int
}

func NewProfilePicker(profiles []types.Profile) ProfilePicker {
	// Pre-select the default profile
	cursor := 0
	for i, p := range profiles {
		if p.IsDefault {
			cursor = i
			break
		}
	}
	return ProfilePicker{
		Profiles: profiles,
		Cursor:   cursor,
	}
}

func (m *ProfilePicker) MoveUp() {
	if m.Cursor > 0 {
		m.Cursor--
	}
}

func (m *ProfilePicker) MoveDown() {
	if m.Cursor < len(m.Profiles)-1 {
		m.Cursor++
	}
}

func (m ProfilePicker) Selected() types.Profile {
	return m.Profiles[m.Cursor]
}

func (m ProfilePicker) View() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Padding(0, 1)
	selectedStyle := lipgloss.NewStyle().Bold(true).Reverse(true).Padding(0, 1)
	normalStyle := lipgloss.NewStyle().Padding(0, 1)
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(1, 2)

	var b strings.Builder
	b.WriteString(titleStyle.Render("Select a Firefox profile:") + "\n\n")

	for i, p := range m.Profiles {
		label := p.Name
		if p.IsDefault {
			label += " (default)"
		}
		line := fmt.Sprintf("  %s", label)
		if i == m.Cursor {
			line = selectedStyle.Render("> " + label)
		} else {
			line = normalStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}

	b.WriteString("\n" + normalStyle.Render("↑↓ navigate · enter select · esc cancel"))

	return boxStyle.Render(b.String())
}
```

**Step 2: Verify it compiles**

Run: `go build ./internal/tui/`
Expected: No errors.

**Step 3: Commit**

```bash
git add internal/tui/profile_picker.go
git commit -m "feat: add profile picker overlay component"
```

---

### Task 12: TUI — Main App

**Files:**
- Create: `internal/tui/app.go`

This ties everything together: the Bubble Tea model with split-pane layout, keybindings, async analyzer commands, and profile switching.

**Step 1: Write the implementation**

```go
package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/nickel-chromium/tabsordnung/internal/analyzer"
	"github.com/nickel-chromium/tabsordnung/internal/firefox"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// --- Messages ---

type sessionLoadedMsg struct {
	data *types.SessionData
	err  error
}

type deadLinkResultMsg analyzer.DeadLinkResult

type analysisCompleteMsg struct{}

// --- Model ---

type pane int

const (
	treePane pane = iota
	detailPane
)

type Model struct {
	// Data
	profiles    []types.Profile
	profile     types.Profile
	session     *types.SessionData
	stats       types.Stats
	staleDays   int

	// UI state
	tree          TreeModel
	detail        DetailModel
	picker        ProfilePicker
	showPicker    bool
	loading       bool
	err           error
	width         int
	height        int

	// Dead link checking
	deadChecking  bool
	deadChecked   int
	deadTotal     int
}

func NewModel(profiles []types.Profile, staleDays int) Model {
	return Model{
		profiles:  profiles,
		staleDays: staleDays,
		loading:   true,
	}
}

func (m Model) Init() tea.Cmd {
	if len(m.profiles) == 1 {
		m.profile = m.profiles[0]
		return loadSession(m.profile)
	}
	// Show picker
	return nil
}

func loadSession(profile types.Profile) tea.Cmd {
	return func() tea.Msg {
		data, err := firefox.ReadSessionFile(profile.Path)
		if err != nil {
			return sessionLoadedMsg{err: err}
		}
		data.Profile = profile
		return sessionLoadedMsg{data: data}
	}
}

func runDeadLinkChecks(tabs []*types.Tab) tea.Cmd {
	return func() tea.Msg {
		results := make(chan analyzer.DeadLinkResult, len(tabs))
		go func() {
			analyzer.AnalyzeDeadLinks(tabs, results)
			close(results)
		}()
		for r := range results {
			// We can't send individual messages from here directly.
			// Instead, we batch process all results.
			_ = r
		}
		return analysisCompleteMsg{}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		treeWidth := m.width * 60 / 100
		detailWidth := m.width - treeWidth - 3 // borders
		paneHeight := m.height - 5              // top bar + bottom bar
		m.tree.Width = treeWidth
		m.tree.Height = paneHeight
		m.detail.Width = detailWidth
		m.detail.Height = paneHeight
		m.picker.Width = m.width
		m.picker.Height = m.height
		return m, nil

	case tea.KeyMsg:
		// Profile picker mode
		if m.showPicker {
			switch msg.String() {
			case "up", "k":
				m.picker.MoveUp()
			case "down", "j":
				m.picker.MoveDown()
			case "enter":
				m.profile = m.picker.Selected()
				m.showPicker = false
				m.loading = true
				return m, loadSession(m.profile)
			case "esc":
				if m.session != nil {
					m.showPicker = false
				}
			case "q", "ctrl+c":
				return m, tea.Quit
			}
			return m, nil
		}

		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			m.tree.MoveUp()
		case "down", "j":
			m.tree.MoveDown()
		case "enter":
			m.tree.Toggle()
		case "f":
			m.tree.Filter = (m.tree.Filter + 1) % 4
			m.tree.Cursor = 0
			m.tree.Offset = 0
		case "r":
			m.loading = true
			return m, loadSession(m.profile)
		case "p":
			m.showPicker = true
			m.picker = NewProfilePicker(m.profiles)
		}
		return m, nil

	case sessionLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.session = msg.data

		// Run synchronous analyzers
		analyzer.AnalyzeStale(m.session.AllTabs, m.staleDays)
		analyzer.AnalyzeDuplicates(m.session.AllTabs)
		m.stats = analyzer.ComputeStats(m.session)

		// Set up tree
		m.tree = NewTreeModel(m.session.Groups)
		m.tree.Width = m.width * 60 / 100
		m.tree.Height = m.height - 5

		// Start dead link checks async
		m.deadChecking = true
		m.deadTotal = len(m.session.AllTabs)
		m.deadChecked = 0
		return m, runDeadLinkChecks(m.session.AllTabs)

	case analysisCompleteMsg:
		m.deadChecking = false
		m.stats = analyzer.ComputeStats(m.session)
		return m, nil
	}

	return m, nil
}

func (m Model) View() string {
	if m.loading {
		return "\n  Loading session data...\n"
	}

	if m.showPicker {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.picker.View())
	}

	if m.err != nil {
		return fmt.Sprintf("\n  Error: %v\n\n  Press 'p' to pick a profile, 'q' to quit.\n", m.err)
	}

	if m.session == nil {
		m.showPicker = true
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.picker.View())
	}

	// Top bar
	topBarStyle := lipgloss.NewStyle().Bold(true).Padding(0, 1)
	profileStr := fmt.Sprintf("Profile: %s", m.profile.Name)
	statsStr := fmt.Sprintf("%d tabs · %d groups", m.stats.TotalTabs, m.stats.TotalGroups)
	if m.stats.DeadTabs > 0 {
		statsStr += fmt.Sprintf(" · %d dead", m.stats.DeadTabs)
	}
	if m.stats.StaleTabs > 0 {
		statsStr += fmt.Sprintf(" · %d stale", m.stats.StaleTabs)
	}
	if m.stats.DuplicateTabs > 0 {
		statsStr += fmt.Sprintf(" · %d dup", m.stats.DuplicateTabs)
	}
	if m.deadChecking {
		statsStr += " · checking links..."
	}
	topBar := topBarStyle.Render(profileStr + "  " + statsStr)

	// Filter indicator
	filterNames := []string{"all", "stale", "dead", "duplicate"}
	filterStr := fmt.Sprintf("[filter: %s]", filterNames[m.tree.Filter])

	// Panes
	treeBorder := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Width(m.tree.Width).
		Height(m.tree.Height)

	detailBorder := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Width(m.detail.Width).
		Height(m.detail.Height)

	// Render detail based on selection
	var detailContent string
	if node := m.tree.SelectedNode(); node != nil {
		if node.Tab != nil {
			detailContent = m.detail.ViewTab(node.Tab)
		} else if node.Group != nil {
			detailContent = m.detail.ViewGroup(node.Group)
		}
	}

	left := treeBorder.Render(m.tree.View())
	right := detailBorder.Render(detailContent)
	panes := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	// Bottom bar
	bottomBarStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Padding(0, 1)
	bottomBar := bottomBarStyle.Render(
		"↑↓/jk navigate · enter expand · f filter · r refresh · p profile · q quit  " + filterStr,
	)

	return lipgloss.JoinVertical(lipgloss.Left, topBar, panes, bottomBar)
}
```

**Step 2: Verify it compiles**

Run: `go build ./internal/tui/`
Expected: No errors.

**Step 3: Commit**

```bash
git add internal/tui/app.go
git commit -m "feat: add main TUI app with split-pane layout and keybindings"
```

---

### Task 13: Main Entry Point

**Files:**
- Modify: `main.go`

**Step 1: Write the implementation**

```go
package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nickel-chromium/tabsordnung/internal/firefox"
	"github.com/nickel-chromium/tabsordnung/internal/tui"
)

func main() {
	profileName := flag.String("profile", "", "Firefox profile name (skip picker)")
	staleDays := flag.Int("stale-days", 7, "Days before a tab is considered stale")
	flag.Parse()

	profiles, err := firefox.DiscoverProfiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error discovering Firefox profiles: %v\n", err)
		os.Exit(1)
	}
	if len(profiles) == 0 {
		fmt.Fprintln(os.Stderr, "No Firefox profiles found.")
		os.Exit(1)
	}

	// If --profile flag is set, filter to just that profile
	if *profileName != "" {
		var filtered []types.Profile
		for _, p := range profiles {
			if p.Name == *profileName {
				filtered = append(filtered, p)
				break
			}
		}
		if len(filtered) == 0 {
			fmt.Fprintf(os.Stderr, "Profile %q not found. Available profiles:\n", *profileName)
			for _, p := range profiles {
				fmt.Fprintf(os.Stderr, "  - %s\n", p.Name)
			}
			os.Exit(1)
		}
		profiles = filtered
	}

	model := tui.NewModel(profiles, *staleDays)
	p := tea.NewProgram(model, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
```

Note: This file needs `"github.com/nickel-chromium/tabsordnung/internal/types"` imported for the `types.Profile` reference. However, looking at it again, the `profiles` variable from `firefox.DiscoverProfiles()` already returns `[]types.Profile`, so we just need the import. Actually, we can pass `profiles` directly to `tui.NewModel` since both use `types.Profile`. Let me fix:

The import should be:
```go
import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nickel-chromium/tabsordnung/internal/firefox"
	"github.com/nickel-chromium/tabsordnung/internal/tui"
)
```

The `filtered` variable should use the return type of `firefox.DiscoverProfiles()` which is `[]types.Profile`. Since `main.go` already imports the `firefox` package which returns that type, we just need to also import `types`:

```go
import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nickel-chromium/tabsordnung/internal/firefox"
	"github.com/nickel-chromium/tabsordnung/internal/tui"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)
```

**Step 2: Verify it compiles**

Run: `go build -o tabsordnung .`
Expected: Binary created successfully.

**Step 3: Commit**

```bash
git add main.go
git commit -m "feat: add main entry point with CLI flags and TUI startup"
```

---

### Task 14: End-to-End Smoke Test

**Files:**
- Create: `internal/firefox/integration_test.go`

A smoke test that creates a fake mozlz4 session file and verifies the full pipeline works.

**Step 1: Write the test**

```go
package firefox

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/nickel-chromium/tabsordnung/internal/analyzer"
	"github.com/pierrec/lz4/v4"
)

func TestIntegration_FullPipeline(t *testing.T) {
	// Create a fake profile directory with a session file
	profileDir := t.TempDir()
	backupDir := filepath.Join(profileDir, "sessionstore-backups")
	os.MkdirAll(backupDir, 0755)

	sessionJSON := `{
		"version": ["sessionrestore", 1],
		"windows": [{
			"tabs": [
				{
					"entries": [{"url": "https://example.com", "title": "Example"}],
					"index": 1,
					"lastAccessed": 1000000000000,
					"group": "g1"
				},
				{
					"entries": [{"url": "https://example.com", "title": "Example Dup"}],
					"index": 1,
					"lastAccessed": 1000000000000,
					"group": "g1"
				},
				{
					"entries": [{"url": "https://other.com/page", "title": "Other"}],
					"index": 1,
					"lastAccessed": 1707654321000
				}
			],
			"groups": [
				{"id": "g1", "name": "Test Group", "color": "blue", "collapsed": false}
			]
		}]
	}`

	// Compress to mozlz4
	jsonBytes := []byte(sessionJSON)
	compressed := make([]byte, lz4.CompressBlockBound(len(jsonBytes)))
	n, err := lz4.CompressBlock(jsonBytes, compressed, nil)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}

	mozlz4 := make([]byte, 0, 12+n)
	mozlz4 = append(mozlz4, []byte("mozLz40\x00")...)
	sizeBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(sizeBuf, uint32(len(jsonBytes)))
	mozlz4 = append(mozlz4, sizeBuf...)
	mozlz4 = append(mozlz4, compressed[:n]...)

	os.WriteFile(filepath.Join(backupDir, "recovery.jsonlz4"), mozlz4, 0644)

	// Run the full pipeline
	data, err := ReadSessionFile(profileDir)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}

	// Run analyzers
	analyzer.AnalyzeStale(data.AllTabs, 7)
	analyzer.AnalyzeDuplicates(data.AllTabs)
	stats := analyzer.ComputeStats(data)

	// Verify results
	if stats.TotalTabs != 3 {
		t.Errorf("expected 3 tabs, got %d", stats.TotalTabs)
	}
	if stats.TotalGroups != 2 {
		t.Errorf("expected 2 groups, got %d", stats.TotalGroups)
	}
	if stats.DuplicateTabs != 2 {
		t.Errorf("expected 2 duplicates, got %d", stats.DuplicateTabs)
	}
	// First two tabs have very old lastAccessed, should be stale
	if stats.StaleTabs < 2 {
		t.Errorf("expected at least 2 stale tabs, got %d", stats.StaleTabs)
	}

	t.Logf("Pipeline passed: %d tabs, %d groups, %d stale, %d dead, %d dup",
		stats.TotalTabs, stats.TotalGroups, stats.StaleTabs, stats.DeadTabs, stats.DuplicateTabs)
}
```

**Step 2: Run the test**

Run: `go test ./internal/firefox/ -run TestIntegration -v`
Expected: PASS

**Step 3: Run all tests**

Run: `go test ./... -v`
Expected: All tests pass.

**Step 4: Commit**

```bash
git add internal/firefox/integration_test.go
git commit -m "test: add end-to-end integration test for full pipeline"
```

---

### Task 15: Final Build and Verification

**Step 1: Run all tests one final time**

Run: `go test ./... -v`
Expected: All tests pass.

**Step 2: Build the binary**

Run: `go build -o tabsordnung .`
Expected: Binary created.

**Step 3: Run the tool**

Run: `./tabsordnung`
Expected: Either shows TUI with profile picker, or error about Firefox not being found (depends on environment).

**Step 4: Final commit (if any remaining changes)**

```bash
git add -A
git commit -m "chore: finalize build"
```
