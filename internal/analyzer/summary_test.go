package analyzer

import (
	"testing"

	"github.com/lotas/tabsordnung/internal/types"
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
