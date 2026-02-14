package summarize

import (
	"testing"

	"github.com/lotas/tabsordnung/internal/types"
)

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"How Go Channels Work | Blog", "how-go-channels-work-blog"},
		{"Simple Title", "simple-title"},
		{"  Leading/Trailing Spaces  ", "leading-trailing-spaces"},
		{"Special!!!Characters???Here", "special-characters-here"},
		{"Multiple---Hyphens", "multiple-hyphens"},
		{"", "untitled"},
		{"   ", "untitled"},
	}
	for _, tt := range tests {
		got := sanitizeFilename(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSanitizeFilename_Truncation(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "a"
	}
	got := sanitizeFilename(long)
	if len(got) > 100 {
		t.Errorf("expected max 100 chars, got %d", len(got))
	}
}

func TestFindGroup(t *testing.T) {
	groups := []*types.TabGroup{
		{Name: "Work", Tabs: []*types.Tab{{URL: "https://a.com"}}},
		{Name: "Summarize This", Tabs: []*types.Tab{{URL: "https://b.com"}, {URL: "https://c.com"}}},
	}
	session := &types.SessionData{Groups: groups}

	group := findGroup(session, "Summarize This")
	if group == nil {
		t.Fatal("expected to find group")
	}
	if len(group.Tabs) != 2 {
		t.Errorf("expected 2 tabs, got %d", len(group.Tabs))
	}
}

func TestFindGroup_NotFound(t *testing.T) {
	groups := []*types.TabGroup{
		{Name: "Work"},
	}
	session := &types.SessionData{Groups: groups}

	group := findGroup(session, "Summarize This")
	if group != nil {
		t.Error("expected nil for missing group")
	}
}
