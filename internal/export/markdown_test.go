package export

import (
	"strings"
	"testing"
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/types"
)

func TestMarkdown_GroupedAndUngrouped(t *testing.T) {
	now := time.Now()
	data := &types.SessionData{
		Profile: types.Profile{Name: "default"},
		Groups: []*types.TabGroup{
			{
				ID:   "1",
				Name: "Research",
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

	result := Markdown(data)

	// Header
	if !strings.Contains(result, "# Firefox Tabs — default") {
		t.Errorf("missing header, got:\n%s", result)
	}
	// Group heading with count
	if !strings.Contains(result, "## Research (2 tabs)") {
		t.Errorf("missing Research group heading, got:\n%s", result)
	}
	if !strings.Contains(result, "## Ungrouped (1 tab)") {
		t.Errorf("missing Ungrouped group heading, got:\n%s", result)
	}
	// Tab entries as markdown links
	if !strings.Contains(result, "[Go docs](https://go.dev/doc)") {
		t.Errorf("missing Go docs link, got:\n%s", result)
	}
	if !strings.Contains(result, "[Bubble Tea](https://github.com/charmbracelet/bubbletea)") {
		t.Errorf("missing Bubble Tea link, got:\n%s", result)
	}
	if !strings.Contains(result, "[Example](https://example.com)") {
		t.Errorf("missing Example link, got:\n%s", result)
	}
}

func TestMarkdown_TitleFallbackToURL(t *testing.T) {
	now := time.Now()
	data := &types.SessionData{
		Profile: types.Profile{Name: "test"},
		Groups: []*types.TabGroup{
			{
				Name: "Ungrouped",
				Tabs: []*types.Tab{
					{Title: "", URL: "https://notitle.com/page", LastAccessed: now},
				},
			},
		},
	}

	result := Markdown(data)

	if !strings.Contains(result, "[https://notitle.com/page](https://notitle.com/page)") {
		t.Errorf("expected URL as title fallback, got:\n%s", result)
	}
}

func TestMarkdown_RelativeTime(t *testing.T) {
	now := time.Now()
	data := &types.SessionData{
		Profile: types.Profile{Name: "test"},
		Groups: []*types.TabGroup{
			{
				Name: "Time",
				Tabs: []*types.Tab{
					{Title: "days", URL: "https://a.com", LastAccessed: now.Add(-3 * 24 * time.Hour)},
					{Title: "hours", URL: "https://b.com", LastAccessed: now.Add(-5 * time.Hour)},
					{Title: "minutes", URL: "https://c.com", LastAccessed: now.Add(-30 * time.Minute)},
					{Title: "just now", URL: "https://d.com", LastAccessed: now},
				},
			},
		},
	}

	result := Markdown(data)

	if !strings.Contains(result, "3d ago") {
		t.Errorf("expected '3d ago' for 3-day-old tab, got:\n%s", result)
	}
	if !strings.Contains(result, "5h ago") {
		t.Errorf("expected '5h ago' for 5-hour-old tab, got:\n%s", result)
	}
	if !strings.Contains(result, "30m ago") {
		t.Errorf("expected '30m ago' for 30-min-old tab, got:\n%s", result)
	}
	if !strings.Contains(result, "just now") {
		t.Errorf("expected 'just now' for recent tab, got:\n%s", result)
	}
}

func TestMarkdown_EmptySession(t *testing.T) {
	data := &types.SessionData{
		Profile: types.Profile{Name: "empty"},
		Groups:  []*types.TabGroup{},
	}

	result := Markdown(data)

	if !strings.Contains(result, "# Firefox Tabs — empty") {
		t.Errorf("expected header even for empty session, got:\n%s", result)
	}
}

func TestMarkdown_SingularTabCount(t *testing.T) {
	now := time.Now()
	data := &types.SessionData{
		Profile: types.Profile{Name: "test"},
		Groups: []*types.TabGroup{
			{
				Name: "Solo",
				Tabs: []*types.Tab{
					{Title: "One", URL: "https://one.com", LastAccessed: now},
				},
			},
		},
	}

	result := Markdown(data)

	if !strings.Contains(result, "## Solo (1 tab)") {
		t.Errorf("expected singular 'tab' not 'tabs', got:\n%s", result)
	}
}
