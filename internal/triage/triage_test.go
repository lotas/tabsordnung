package triage

import (
	"testing"
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/types"
)

func TestClassify(t *testing.T) {
	now := time.Now()
	tabs := []*types.Tab{
		// Needs attention: review requested
		{URL: "https://github.com/org/repo/pull/1", Title: "PR1", GitHubStatus: "open", GitHubTriage: &types.GitHubTriageInfo{ReviewRequested: true}},
		// Needs attention: assigned + new activity
		{URL: "https://github.com/org/repo/issues/2", Title: "Issue2", GitHubStatus: "open", LastAccessed: now.Add(-48 * time.Hour), GitHubTriage: &types.GitHubTriageInfo{Assigned: true, UpdatedAt: now.Add(-1 * time.Hour)}},
		// Needs attention: new activity since last access
		{URL: "https://github.com/org/repo/pull/3", Title: "PR3", GitHubStatus: "open", LastAccessed: now.Add(-24 * time.Hour), GitHubTriage: &types.GitHubTriageInfo{UpdatedAt: now.Add(-1 * time.Hour)}},
		// Open PR (no attention needed, visited recently)
		{URL: "https://github.com/org/repo/pull/4", Title: "PR4", GitHubStatus: "open", LastAccessed: now, GitHubTriage: &types.GitHubTriageInfo{UpdatedAt: now.Add(-1 * time.Hour)}},
		// Open issue
		{URL: "https://github.com/org/repo/issues/5", Title: "Issue5", GitHubStatus: "open", LastAccessed: now, GitHubTriage: &types.GitHubTriageInfo{UpdatedAt: now.Add(-1 * time.Hour)}},
		// Closed issue
		{URL: "https://github.com/org/repo/issues/6", Title: "Issue6", GitHubStatus: "closed", GitHubTriage: &types.GitHubTriageInfo{}},
		// Merged PR
		{URL: "https://github.com/org/repo/pull/7", Title: "PR7", GitHubStatus: "merged", GitHubTriage: &types.GitHubTriageInfo{}},
		// Non-GitHub tab
		{URL: "https://google.com", Title: "Google"},
	}
	result := Classify(tabs)
	if len(result.NeedsAttention) != 3 {
		t.Errorf("NeedsAttention: got %d, want 3", len(result.NeedsAttention))
	}
	if len(result.OpenPRs) != 1 {
		t.Errorf("OpenPRs: got %d, want 1", len(result.OpenPRs))
	}
	if len(result.OpenIssues) != 1 {
		t.Errorf("OpenIssues: got %d, want 1", len(result.OpenIssues))
	}
	if len(result.ClosedMerged) != 2 {
		t.Errorf("ClosedMerged: got %d, want 2", len(result.ClosedMerged))
	}
	if result.Skipped != 1 {
		t.Errorf("Skipped: got %d, want 1", result.Skipped)
	}
}
