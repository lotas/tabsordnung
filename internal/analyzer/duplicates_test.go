package analyzer

import (
	"testing"

	"github.com/lotas/tabsordnung/internal/types"
)

func TestAnalyzeDuplicates(t *testing.T) {
	tabs := []*types.Tab{
		{URL: "https://example.com/page#section1"},
		{URL: "https://example.com/page#section2"},
		{URL: "https://example.com/other"},
		{URL: "https://example.com/page?b=2&a=1"},
		{URL: "https://example.com/page?a=1&b=2"},
	}

	AnalyzeDuplicates(tabs)

	if !tabs[0].IsDuplicate {
		t.Error("tab 0 should be duplicate")
	}
	if !tabs[1].IsDuplicate {
		t.Error("tab 1 should be duplicate")
	}
	if tabs[2].IsDuplicate {
		t.Error("tab 2 should not be duplicate")
	}
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
