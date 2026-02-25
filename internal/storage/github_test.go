package storage

import (
	"testing"
	"time"
)

func TestUpsertGitHubEntity(t *testing.T) {
	db := testDB(t)

	// First upsert should create a new entity.
	id1, isNew, err := UpsertGitHubEntity(db, "mozilla", "gecko-dev", 42, "pull", "tab")
	if err != nil {
		t.Fatalf("first UpsertGitHubEntity: %v", err)
	}
	if !isNew {
		t.Error("expected isNew=true for first insert")
	}
	if id1 == 0 {
		t.Error("expected non-zero id")
	}

	// Second upsert with same owner/repo/number should return same id.
	id2, isNew2, err := UpsertGitHubEntity(db, "mozilla", "gecko-dev", 42, "pull", "signal")
	if err != nil {
		t.Fatalf("second UpsertGitHubEntity: %v", err)
	}
	if isNew2 {
		t.Error("expected isNew=false for existing entity")
	}
	if id2 != id1 {
		t.Errorf("expected same id %d, got %d", id1, id2)
	}

	// Different number should create a new entity.
	id3, isNew3, err := UpsertGitHubEntity(db, "mozilla", "gecko-dev", 99, "issue", "signal")
	if err != nil {
		t.Fatalf("third UpsertGitHubEntity: %v", err)
	}
	if !isNew3 {
		t.Error("expected isNew=true for different number")
	}
	if id3 == id1 {
		t.Error("expected different id for different number")
	}

	// Verify the entity can be retrieved.
	ent, err := GetGitHubEntity(db, "mozilla", "gecko-dev", 42)
	if err != nil {
		t.Fatalf("GetGitHubEntity: %v", err)
	}
	if ent == nil {
		t.Fatal("expected non-nil entity")
	}
	if ent.Kind != "pull" {
		t.Errorf("expected kind 'pull', got %q", ent.Kind)
	}
	if ent.FirstSeenSource != "tab" {
		t.Errorf("expected first_seen_source 'tab', got %q", ent.FirstSeenSource)
	}
}

func TestRecordAndListGitHubEvents(t *testing.T) {
	db := testDB(t)

	// Create an entity first.
	entityID, _, err := UpsertGitHubEntity(db, "owner", "repo", 1, "pull", "tab")
	if err != nil {
		t.Fatalf("UpsertGitHubEntity: %v", err)
	}

	// Create a real signal and snapshot so FK constraints are satisfied.
	db.Exec(`INSERT INTO signals (id, source, title, preview, source_ts, captured_at)
		VALUES (100, 'slack', 'test', 'preview', 'ts1', CURRENT_TIMESTAMP)`)
	_, err = CreateSnapshot(db, "default", nil, []SnapshotTab{
		{URL: "https://example.com", Title: "Example"},
	}, "")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	// snapshot id=1 after insert
	var snapshotID int64
	db.QueryRow(`SELECT id FROM snapshots LIMIT 1`).Scan(&snapshotID)
	signalID := int64(100)

	// Record several events.
	err = RecordGitHubEvent(db, entityID, "tab_seen", nil, nil, "seen in tab bar")
	if err != nil {
		t.Fatalf("RecordGitHubEvent (tab_seen): %v", err)
	}
	err = RecordGitHubEvent(db, entityID, "signal_seen", &signalID, nil, "from slack signal")
	if err != nil {
		t.Fatalf("RecordGitHubEvent (signal_seen): %v", err)
	}
	err = RecordGitHubEvent(db, entityID, "status_changed", nil, &snapshotID, "open -> merged")
	if err != nil {
		t.Fatalf("RecordGitHubEvent (status_changed): %v", err)
	}

	// List events — should be ordered by created_at ASC.
	events, err := ListGitHubEntityEvents(db, entityID)
	if err != nil {
		t.Fatalf("ListGitHubEntityEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	// Verify first event.
	if events[0].EventType != "tab_seen" {
		t.Errorf("expected event_type 'tab_seen', got %q", events[0].EventType)
	}
	if events[0].Detail != "seen in tab bar" {
		t.Errorf("expected detail 'seen in tab bar', got %q", events[0].Detail)
	}
	if events[0].SignalID != nil {
		t.Errorf("expected nil signal_id, got %d", *events[0].SignalID)
	}

	// Verify second event has signal_id.
	if events[1].EventType != "signal_seen" {
		t.Errorf("expected event_type 'signal_seen', got %q", events[1].EventType)
	}
	if events[1].SignalID == nil || *events[1].SignalID != 100 {
		t.Errorf("expected signal_id=100, got %v", events[1].SignalID)
	}

	// Verify third event has snapshot_id.
	if events[2].SnapshotID == nil || *events[2].SnapshotID != snapshotID {
		t.Errorf("expected snapshot_id=%d, got %v", snapshotID, events[2].SnapshotID)
	}

	// Events for non-existent entity should return empty.
	noEvents, err := ListGitHubEntityEvents(db, 9999)
	if err != nil {
		t.Fatalf("ListGitHubEntityEvents for missing: %v", err)
	}
	if len(noEvents) != 0 {
		t.Errorf("expected 0 events for non-existent entity, got %d", len(noEvents))
	}
}

func TestListGitHubEntities(t *testing.T) {
	db := testDB(t)

	// Create a mix of entities.
	UpsertGitHubEntity(db, "mozilla", "gecko-dev", 1, "pull", "tab")
	UpsertGitHubEntity(db, "mozilla", "gecko-dev", 2, "issue", "signal")
	UpsertGitHubEntity(db, "nickel", "tabsordnung", 10, "pull", "tab")
	UpsertGitHubEntity(db, "nickel", "tabsordnung", 11, "issue", "signal")

	// Set some states for ordering tests.
	UpdateGitHubEntityStatus(db, 1, GitHubStatusUpdate{
		Title: "Fix gecko bug",
		State: "open",
	})
	UpdateGitHubEntityStatus(db, 2, GitHubStatusUpdate{
		Title: "Track issue",
		State: "closed",
	})
	UpdateGitHubEntityStatus(db, 3, GitHubStatusUpdate{
		Title: "Add feature",
		State: "open",
	})
	UpdateGitHubEntityStatus(db, 4, GitHubStatusUpdate{
		Title: "Docs issue",
		State: "open",
	})

	// List all — should get 4.
	all, err := ListGitHubEntities(db, GitHubFilter{})
	if err != nil {
		t.Fatalf("ListGitHubEntities (all): %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 entities, got %d", len(all))
	}

	// Open entities should come first.
	if all[0].State == "closed" {
		t.Error("expected open entities before closed")
	}

	// Filter by kind=pull.
	pulls, err := ListGitHubEntities(db, GitHubFilter{Kind: "pull"})
	if err != nil {
		t.Fatalf("ListGitHubEntities (pulls): %v", err)
	}
	if len(pulls) != 2 {
		t.Fatalf("expected 2 pulls, got %d", len(pulls))
	}
	for _, p := range pulls {
		if p.Kind != "pull" {
			t.Errorf("expected kind 'pull', got %q", p.Kind)
		}
	}

	// Filter by kind=issue.
	issues, err := ListGitHubEntities(db, GitHubFilter{Kind: "issue"})
	if err != nil {
		t.Fatalf("ListGitHubEntities (issues): %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}

	// Filter by state=open.
	open, err := ListGitHubEntities(db, GitHubFilter{State: "open"})
	if err != nil {
		t.Fatalf("ListGitHubEntities (open): %v", err)
	}
	if len(open) != 3 {
		t.Fatalf("expected 3 open entities, got %d", len(open))
	}

	// Filter by repo.
	repoFiltered, err := ListGitHubEntities(db, GitHubFilter{Repo: "mozilla/gecko-dev"})
	if err != nil {
		t.Fatalf("ListGitHubEntities (repo): %v", err)
	}
	if len(repoFiltered) != 2 {
		t.Fatalf("expected 2 entities for mozilla/gecko-dev, got %d", len(repoFiltered))
	}

	// Combined filter.
	combined, err := ListGitHubEntities(db, GitHubFilter{Kind: "issue", State: "open"})
	if err != nil {
		t.Fatalf("ListGitHubEntities (combined): %v", err)
	}
	if len(combined) != 1 {
		t.Fatalf("expected 1 open issue, got %d", len(combined))
	}
	if combined[0].Title != "Docs issue" {
		t.Errorf("expected 'Docs issue', got %q", combined[0].Title)
	}
}

func TestUpdateGitHubEntityStatus(t *testing.T) {
	db := testDB(t)

	// Create an entity.
	id, _, err := UpsertGitHubEntity(db, "mozilla", "gecko-dev", 55, "pull", "tab")
	if err != nil {
		t.Fatalf("UpsertGitHubEntity: %v", err)
	}

	// Verify initial state.
	ent, err := GetGitHubEntity(db, "mozilla", "gecko-dev", 55)
	if err != nil {
		t.Fatalf("GetGitHubEntity: %v", err)
	}
	if ent.Title != "" {
		t.Errorf("expected empty title initially, got %q", ent.Title)
	}
	if ent.State != "" {
		t.Errorf("expected empty state initially, got %q", ent.State)
	}
	if ent.LastRefreshedAt != nil {
		t.Error("expected nil LastRefreshedAt initially")
	}

	// Update with GitHub data.
	now := time.Now().UTC().Truncate(time.Second)
	reviewStatus := "approved"
	checksStatus := "success"
	err = UpdateGitHubEntityStatus(db, id, GitHubStatusUpdate{
		Title:        "Fix critical bug",
		State:        "open",
		Author:       "octocat",
		Assignees:    "alice,bob",
		ReviewStatus: &reviewStatus,
		ChecksStatus: &checksStatus,
		GHUpdatedAt:  &now,
	})
	if err != nil {
		t.Fatalf("UpdateGitHubEntityStatus: %v", err)
	}

	// Verify the update.
	ent, err = GetGitHubEntity(db, "mozilla", "gecko-dev", 55)
	if err != nil {
		t.Fatalf("GetGitHubEntity after update: %v", err)
	}
	if ent.Title != "Fix critical bug" {
		t.Errorf("expected title 'Fix critical bug', got %q", ent.Title)
	}
	if ent.State != "open" {
		t.Errorf("expected state 'open', got %q", ent.State)
	}
	if ent.Author != "octocat" {
		t.Errorf("expected author 'octocat', got %q", ent.Author)
	}
	if ent.Assignees != "alice,bob" {
		t.Errorf("expected assignees 'alice,bob', got %q", ent.Assignees)
	}
	if ent.ReviewStatus == nil || *ent.ReviewStatus != "approved" {
		t.Errorf("expected review_status 'approved', got %v", ent.ReviewStatus)
	}
	if ent.ChecksStatus == nil || *ent.ChecksStatus != "success" {
		t.Errorf("expected checks_status 'success', got %v", ent.ChecksStatus)
	}
	if ent.LastRefreshedAt == nil {
		t.Fatal("expected non-nil LastRefreshedAt after update")
	}
	if ent.GHUpdatedAt == nil {
		t.Fatal("expected non-nil GHUpdatedAt after update")
	}

	// Update again to merged state, nil review/checks.
	err = UpdateGitHubEntityStatus(db, id, GitHubStatusUpdate{
		Title:     "Fix critical bug",
		State:     "merged",
		Author:    "octocat",
		Assignees: "alice,bob",
	})
	if err != nil {
		t.Fatalf("second UpdateGitHubEntityStatus: %v", err)
	}

	ent, err = GetGitHubEntity(db, "mozilla", "gecko-dev", 55)
	if err != nil {
		t.Fatalf("GetGitHubEntity after second update: %v", err)
	}
	if ent.State != "merged" {
		t.Errorf("expected state 'merged', got %q", ent.State)
	}
	if ent.ReviewStatus != nil {
		t.Errorf("expected nil review_status, got %q", *ent.ReviewStatus)
	}
	if ent.ChecksStatus != nil {
		t.Errorf("expected nil checks_status, got %q", *ent.ChecksStatus)
	}
}

func TestOpenGitHubEntityCount(t *testing.T) {
	db := testDB(t)

	// No entities — count should be 0.
	count, err := OpenGitHubEntityCount(db)
	if err != nil {
		t.Fatalf("OpenGitHubEntityCount: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	// Create some entities — they start with state='', which counts as open.
	UpsertGitHubEntity(db, "o", "r", 1, "pull", "tab")
	UpsertGitHubEntity(db, "o", "r", 2, "issue", "tab")
	UpsertGitHubEntity(db, "o", "r", 3, "pull", "tab")

	count, err = OpenGitHubEntityCount(db)
	if err != nil {
		t.Fatalf("OpenGitHubEntityCount: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 (empty state counts as open), got %d", count)
	}

	// Set one to 'open' and one to 'closed'.
	UpdateGitHubEntityStatus(db, 1, GitHubStatusUpdate{State: "open"})
	UpdateGitHubEntityStatus(db, 2, GitHubStatusUpdate{State: "closed"})

	count, err = OpenGitHubEntityCount(db)
	if err != nil {
		t.Fatalf("OpenGitHubEntityCount: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 (1 open + 1 empty), got %d", count)
	}
}

func TestExtractGitHubFromSnapshot(t *testing.T) {
	db := testDB(t)

	// Create a snapshot with GitHub tabs
	_, err := CreateSnapshot(db, "default", nil, []SnapshotTab{
		{URL: "https://github.com/mozilla/gecko-dev/pull/123", Title: "Fix bug"},
		{URL: "https://mail.google.com/inbox", Title: "Gmail"},
		{URL: "https://github.com/org/repo/issues/42", Title: "Feature request"},
	}, "")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	// Get snapshot ID
	var snapID int64
	db.QueryRow("SELECT id FROM snapshots WHERE profile = 'default' AND rev = 1").Scan(&snapID)

	count, err := ExtractGitHubFromSnapshot(db, snapID)
	if err != nil {
		t.Fatalf("ExtractGitHubFromSnapshot: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 entities extracted, got %d", count)
	}

	entities, _ := ListGitHubEntities(db, GitHubFilter{})
	if len(entities) != 2 {
		t.Fatalf("expected 2 entities, got %d", len(entities))
	}
}

func TestExtractGitHubFromSignals(t *testing.T) {
	db := testDB(t)

	now := time.Now()
	// Insert a signal referencing a GitHub PR via subject pattern
	InsertSignal(db, SignalRecord{
		Source:     "gmail",
		Title:      "dependabot",
		Preview:    "[mozilla/gecko-dev] Bump lodash (#1234)",
		SourceTS:   "2:30 PM",
		CapturedAt: now,
	})
	// Insert a signal with a GitHub URL in snippet
	InsertSignal(db, SignalRecord{
		Source:     "gmail",
		Title:      "alice",
		Preview:    "Review requested",
		Snippet:    "Please review https://github.com/org/repo/pull/42",
		SourceTS:   "3:00 PM",
		CapturedAt: now,
	})
	// Insert a signal with no GitHub reference
	InsertSignal(db, SignalRecord{
		Source:     "slack",
		Title:      "#general",
		Preview:    "Hello world",
		SourceTS:   "4:00 PM",
		CapturedAt: now,
	})

	signals, _ := ListSignals(db, "", false)
	count, err := ExtractGitHubFromSignals(db, signals)
	if err != nil {
		t.Fatalf("ExtractGitHubFromSignals: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 entities extracted, got %d", count)
	}

	entities, _ := ListGitHubEntities(db, GitHubFilter{})
	if len(entities) != 2 {
		t.Fatalf("expected 2 entities, got %d", len(entities))
	}
}

func TestGetGitHubEntity_NotFound(t *testing.T) {
	db := testDB(t)

	ent, err := GetGitHubEntity(db, "nonexistent", "repo", 1)
	if err != nil {
		t.Fatalf("GetGitHubEntity: %v", err)
	}
	if ent != nil {
		t.Error("expected nil for non-existent entity")
	}
}

func TestBackfillGitHubEntities(t *testing.T) {
	db := testDB(t)

	// Create snapshots with GitHub tabs
	CreateSnapshot(db, "default", nil, []SnapshotTab{
		{URL: "https://github.com/mozilla/gecko-dev/pull/1", Title: "PR 1"},
		{URL: "https://example.com", Title: "Example"},
	}, "")
	CreateSnapshot(db, "default", nil, []SnapshotTab{
		{URL: "https://github.com/mozilla/gecko-dev/pull/1", Title: "PR 1"},
		{URL: "https://github.com/mozilla/gecko-dev/issues/2", Title: "Issue 2"},
	}, "")

	// Insert a signal referencing GitHub
	InsertSignal(db, SignalRecord{
		Source: "gmail", Title: "bot", Preview: "[org/repo] Fix (#99)",
		SourceTS: "1:00 PM", CapturedAt: time.Now(),
	})

	// Clear any entities created by the snapshot hook (Task 5)
	db.Exec("DELETE FROM github_entity_events")
	db.Exec("DELETE FROM github_entities")

	count, err := BackfillGitHubEntities(db)
	if err != nil {
		t.Fatalf("BackfillGitHubEntities: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected 3 entities backfilled, got %d", count)
	}

	entities, _ := ListGitHubEntities(db, GitHubFilter{})
	if len(entities) != 3 {
		t.Fatalf("expected 3 entities in db, got %d", len(entities))
	}

	// Running again should not duplicate
	count2, _ := BackfillGitHubEntities(db)
	if count2 != 3 {
		t.Fatalf("expected 3 entities on second run, got %d", count2)
	}
}
