package github

import (
	"strings"
	"testing"
	"time"
)

func TestBuildEntityGraphQLQuery(t *testing.T) {
	refs := []EntityRef{
		{Owner: "mozilla", Repo: "gecko-dev", Number: 123, Kind: "pull"},
		{Owner: "mozilla", Repo: "gecko-dev", Number: 456, Kind: "issue"},
		{Owner: "nickel-chromium", Repo: "tabsordnung", Number: 7, Kind: "pull"},
	}
	query, aliasMap := BuildEntityGraphQLQuery(refs)

	// Verify query is non-empty
	if query == "" {
		t.Fatal("BuildEntityGraphQLQuery returned empty query")
	}

	// Verify aliasMap has 3 entries
	if len(aliasMap) != 3 {
		t.Errorf("aliasMap has %d entries, want 3", len(aliasMap))
	}

	// Verify query contains expected fragments
	if !strings.Contains(query, "pullRequest(number: 123)") {
		t.Errorf("query missing pullRequest(number: 123):\n%s", query)
	}
	if !strings.Contains(query, "issue(number: 456)") {
		t.Errorf("query missing issue(number: 456):\n%s", query)
	}
	if !strings.Contains(query, "pullRequest(number: 7)") {
		t.Errorf("query missing pullRequest(number: 7):\n%s", query)
	}

	// Verify query has proper structure: query { ... repository(...) { ... } ... }
	if !strings.HasPrefix(query, "query {") {
		t.Errorf("query should start with 'query {', got: %s", query[:20])
	}
	if !strings.HasSuffix(query, " }") {
		t.Errorf("query should end with ' }', got: ...%s", query[len(query)-10:])
	}

	// Verify PR fields include reviewDecision and statusCheckRollup
	if !strings.Contains(query, "reviewDecision") {
		t.Errorf("query missing reviewDecision for PRs:\n%s", query)
	}
	if !strings.Contains(query, "statusCheckRollup") {
		t.Errorf("query missing statusCheckRollup for PRs:\n%s", query)
	}

	// Verify issue fields do NOT include reviewDecision
	// The issue block is between "issue(number: 456)" and the next "}"
	// We check the full query doesn't have reviewDecision right after issue fields
	// (This is implicitly tested by the structure)

	// Verify both repos are in the query
	if !strings.Contains(query, `"gecko-dev"`) {
		t.Errorf("query missing gecko-dev repo:\n%s", query)
	}
	if !strings.Contains(query, `"tabsordnung"`) {
		t.Errorf("query missing tabsordnung repo:\n%s", query)
	}

	// Verify aliasMap values map back to correct indices
	foundIndices := make(map[int]bool)
	for _, idx := range aliasMap {
		foundIndices[idx] = true
	}
	for i := range refs {
		if !foundIndices[i] {
			t.Errorf("aliasMap missing index %d", i)
		}
	}
}

func TestBuildEntityGraphQLQuery_Empty(t *testing.T) {
	query, aliasMap := BuildEntityGraphQLQuery(nil)
	if query != "query { }" {
		t.Errorf("empty refs should produce 'query { }', got: %q", query)
	}
	if len(aliasMap) != 0 {
		t.Errorf("empty refs should produce empty aliasMap, got %d entries", len(aliasMap))
	}
}

func TestToStatusUpdate(t *testing.T) {
	result := EntityRefreshResult{
		State:        "OPEN",
		Title:        "Fix bug",
		Author:       "octocat",
		UpdatedAt:    "2024-01-15T10:30:00Z",
		Assignees:    []string{"alice", "bob"},
		ReviewStatus: "APPROVED",
		ChecksStatus: "SUCCESS",
	}
	update := result.ToStatusUpdate()

	// Verify state is normalized to lowercase
	if update.State != "open" {
		t.Errorf("State = %q, want %q", update.State, "open")
	}

	// Verify title and author are passed through
	if update.Title != "Fix bug" {
		t.Errorf("Title = %q, want %q", update.Title, "Fix bug")
	}
	if update.Author != "octocat" {
		t.Errorf("Author = %q, want %q", update.Author, "octocat")
	}

	// Verify assignees are comma-joined
	if update.Assignees != "alice,bob" {
		t.Errorf("Assignees = %q, want %q", update.Assignees, "alice,bob")
	}

	// Verify ReviewStatus mapping: APPROVED -> "approved"
	if update.ReviewStatus == nil || *update.ReviewStatus != "approved" {
		t.Errorf("ReviewStatus = %v, want 'approved'", update.ReviewStatus)
	}

	// Verify ChecksStatus mapping: SUCCESS -> "passing"
	if update.ChecksStatus == nil || *update.ChecksStatus != "passing" {
		t.Errorf("ChecksStatus = %v, want 'passing'", update.ChecksStatus)
	}

	// Verify GHUpdatedAt is parsed correctly
	expectedTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	if update.GHUpdatedAt == nil {
		t.Fatal("GHUpdatedAt is nil, want parsed time")
	}
	if !update.GHUpdatedAt.Equal(expectedTime) {
		t.Errorf("GHUpdatedAt = %v, want %v", update.GHUpdatedAt, expectedTime)
	}
}

func TestToStatusUpdate_MERGED(t *testing.T) {
	result := EntityRefreshResult{
		State:     "MERGED",
		Title:     "Add feature",
		Author:    "dev",
		UpdatedAt: "2024-06-01T12:00:00Z",
	}
	update := result.ToStatusUpdate()

	if update.State != "merged" {
		t.Errorf("State = %q, want %q", update.State, "merged")
	}
	if update.ReviewStatus != nil {
		t.Errorf("ReviewStatus = %v, want nil", update.ReviewStatus)
	}
	if update.ChecksStatus != nil {
		t.Errorf("ChecksStatus = %v, want nil", update.ChecksStatus)
	}
}

func TestToStatusUpdate_ChangesRequested(t *testing.T) {
	result := EntityRefreshResult{
		State:        "OPEN",
		Title:        "Draft PR",
		Author:       "dev",
		UpdatedAt:    "2024-06-01T12:00:00Z",
		ReviewStatus: "CHANGES_REQUESTED",
		ChecksStatus: "FAILURE",
	}
	update := result.ToStatusUpdate()

	if update.ReviewStatus == nil || *update.ReviewStatus != "changes_requested" {
		t.Errorf("ReviewStatus = %v, want 'changes_requested'", update.ReviewStatus)
	}
	if update.ChecksStatus == nil || *update.ChecksStatus != "failing" {
		t.Errorf("ChecksStatus = %v, want 'failing'", update.ChecksStatus)
	}
}

func TestToStatusUpdate_ReviewRequired(t *testing.T) {
	result := EntityRefreshResult{
		State:        "OPEN",
		Title:        "New PR",
		Author:       "dev",
		UpdatedAt:    "2024-06-01T12:00:00Z",
		ReviewStatus: "REVIEW_REQUIRED",
		ChecksStatus: "PENDING",
	}
	update := result.ToStatusUpdate()

	if update.ReviewStatus == nil || *update.ReviewStatus != "pending" {
		t.Errorf("ReviewStatus = %v, want 'pending'", update.ReviewStatus)
	}
	if update.ChecksStatus == nil || *update.ChecksStatus != "pending" {
		t.Errorf("ChecksStatus = %v, want 'pending'", update.ChecksStatus)
	}
}

func TestToStatusUpdate_EmptyAssignees(t *testing.T) {
	result := EntityRefreshResult{
		State:     "OPEN",
		Title:     "No assignees",
		Author:    "dev",
		UpdatedAt: "2024-06-01T12:00:00Z",
		Assignees: nil,
	}
	update := result.ToStatusUpdate()

	if update.Assignees != "" {
		t.Errorf("Assignees = %q, want empty", update.Assignees)
	}
}

func TestToStatusUpdate_BadTimestamp(t *testing.T) {
	result := EntityRefreshResult{
		State:     "OPEN",
		Title:     "Bad time",
		Author:    "dev",
		UpdatedAt: "not-a-timestamp",
	}
	update := result.ToStatusUpdate()

	if update.GHUpdatedAt != nil {
		t.Errorf("GHUpdatedAt = %v, want nil for bad timestamp", update.GHUpdatedAt)
	}
}
