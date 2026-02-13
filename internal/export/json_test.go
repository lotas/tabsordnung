package export

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/types"
)

func TestJSON_GroupedAndUngrouped(t *testing.T) {
	now := time.Now()
	data := &types.SessionData{
		Profile: types.Profile{Name: "default"},
		Groups: []*types.TabGroup{
			{
				ID:    "1",
				Name:  "Research",
				Color: "blue",
				Tabs: []*types.Tab{
					{Title: "Go docs", URL: "https://go.dev/doc", LastAccessed: now.Add(-3 * 24 * time.Hour)},
					{Title: "Bubble Tea", URL: "https://github.com/charmbracelet/bubbletea", LastAccessed: now.Add(-1 * 24 * time.Hour)},
				},
			},
			{
				ID:   "",
				Name: "Ungrouped",
				Tabs: []*types.Tab{
					{Title: "Example", URL: "https://example.com", LastAccessed: now.Add(-5 * time.Hour)},
				},
			},
		},
	}

	result, err := JSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed jsonExport
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\noutput:\n%s", err, result)
	}

	if parsed.Profile != "default" {
		t.Errorf("expected profile 'default', got %q", parsed.Profile)
	}
	if len(parsed.Groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(parsed.Groups))
	}
	if parsed.Groups[0].Name != "Research" {
		t.Errorf("expected group 'Research', got %q", parsed.Groups[0].Name)
	}
	if parsed.Groups[0].Color != "blue" {
		t.Errorf("expected color 'blue', got %q", parsed.Groups[0].Color)
	}
	if len(parsed.Groups[0].Tabs) != 2 {
		t.Errorf("expected 2 tabs in Research, got %d", len(parsed.Groups[0].Tabs))
	}
	if parsed.Groups[1].Name != "Ungrouped" {
		t.Errorf("expected group 'Ungrouped', got %q", parsed.Groups[1].Name)
	}
	if len(parsed.Groups[1].Tabs) != 1 {
		t.Errorf("expected 1 tab in Ungrouped, got %d", len(parsed.Groups[1].Tabs))
	}

	// Check new fields
	tab0 := parsed.Groups[0].Tabs[0]
	if tab0.Category != "Research" {
		t.Errorf("expected category 'Research', got %q", tab0.Category)
	}
	if tab0.Domain != "go.dev" {
		t.Errorf("expected domain 'go.dev', got %q", tab0.Domain)
	}
	if tab0.LastAccessedPretty != "3d ago" {
		t.Errorf("expected last_accessed_pretty '3d ago', got %q", tab0.LastAccessedPretty)
	}
	if tab0.LastAccessedDays != 3 {
		t.Errorf("expected last_accessed_days 3, got %d", tab0.LastAccessedDays)
	}

	tab1 := parsed.Groups[0].Tabs[1]
	if tab1.Domain != "github.com" {
		t.Errorf("expected domain 'github.com', got %q", tab1.Domain)
	}

	tab2 := parsed.Groups[1].Tabs[0]
	if tab2.Category != "Ungrouped" {
		t.Errorf("expected category 'Ungrouped', got %q", tab2.Category)
	}
	if tab2.Domain != "example.com" {
		t.Errorf("expected domain 'example.com', got %q", tab2.Domain)
	}
}

func TestJSON_AnalysisFields(t *testing.T) {
	now := time.Now()
	data := &types.SessionData{
		Profile: types.Profile{Name: "test"},
		Groups: []*types.TabGroup{
			{
				Name: "Mixed",
				Tabs: []*types.Tab{
					{Title: "Stale", URL: "https://stale.com", LastAccessed: now, IsStale: true, StaleDays: 14},
					{Title: "Dead", URL: "https://dead.com", LastAccessed: now, IsDead: true, DeadReason: "404"},
					{Title: "Dup", URL: "https://dup.com", LastAccessed: now, IsDuplicate: true},
					{Title: "Clean", URL: "https://clean.com", LastAccessed: now},
				},
			},
		},
	}

	result, err := JSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed jsonExport
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	tabs := parsed.Groups[0].Tabs
	if !tabs[0].IsStale || tabs[0].StaleDays != 14 {
		t.Errorf("expected stale tab with 14 days, got stale=%v days=%d", tabs[0].IsStale, tabs[0].StaleDays)
	}
	if !tabs[1].IsDead || tabs[1].DeadReason != "404" {
		t.Errorf("expected dead tab with reason '404', got dead=%v reason=%q", tabs[1].IsDead, tabs[1].DeadReason)
	}
	if !tabs[2].IsDuplicate {
		t.Errorf("expected duplicate tab")
	}
	if tabs[3].IsStale || tabs[3].IsDead || tabs[3].IsDuplicate {
		t.Errorf("expected clean tab with no flags")
	}
}

func TestJSON_EmptySession(t *testing.T) {
	data := &types.SessionData{
		Profile: types.Profile{Name: "empty"},
		Groups:  []*types.TabGroup{},
	}

	result, err := JSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed jsonExport
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if parsed.Profile != "empty" {
		t.Errorf("expected profile 'empty', got %q", parsed.Profile)
	}
	if len(parsed.Groups) != 0 {
		t.Errorf("expected 0 groups, got %d", len(parsed.Groups))
	}
}
