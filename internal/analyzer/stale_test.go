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
