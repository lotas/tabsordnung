package analyzer

import "github.com/lotas/tabsordnung/internal/types"

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
		if tab.GitHubStatus == "closed" || tab.GitHubStatus == "merged" {
			stats.GitHubDoneTabs++
		}
	}
	return stats
}
