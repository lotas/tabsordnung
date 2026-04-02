package storage

import (
	"strings"
	"testing"
	"time"
)

func TestInsertSignal(t *testing.T) {
	db := testDB(t)

	now := time.Now()
	err := InsertSignal(db, SignalRecord{
		Source:     "gmail",
		Title:      "Alice",
		Preview:    "Production DB alert",
		SourceTS:   "2:30 PM",
		CapturedAt: now,
	})
	if err != nil {
		t.Fatalf("InsertSignal: %v", err)
	}

	// Duplicate insert should be silently ignored.
	err = InsertSignal(db, SignalRecord{
		Source:     "gmail",
		Title:      "Alice",
		Preview:    "Different preview",
		SourceTS:   "2:30 PM",
		CapturedAt: now,
	})
	if err != nil {
		t.Fatalf("duplicate InsertSignal: %v", err)
	}

	// Different source_ts should succeed.
	err = InsertSignal(db, SignalRecord{
		Source:     "gmail",
		Title:      "Alice",
		Preview:    "Another email",
		SourceTS:   "3:00 PM",
		CapturedAt: now,
	})
	if err != nil {
		t.Fatalf("InsertSignal different ts: %v", err)
	}
}

func TestListSignals(t *testing.T) {
	db := testDB(t)

	now := time.Now()
	InsertSignal(db, SignalRecord{Source: "gmail", Title: "Alice", Preview: "alert", SourceTS: "2:30 PM", CapturedAt: now})
	InsertSignal(db, SignalRecord{Source: "gmail", Title: "Bob", Preview: "sync", SourceTS: "3:00 PM", CapturedAt: now})
	InsertSignal(db, SignalRecord{Source: "slack", Title: "#ops", Preview: "unread", SourceTS: "", CapturedAt: now})

	sigs, err := ListSignals(db, "gmail", "", false)
	if err != nil {
		t.Fatalf("ListSignals: %v", err)
	}
	if len(sigs) != 2 {
		t.Fatalf("expected 2 gmail signals, got %d", len(sigs))
	}

	all, err := ListSignals(db, "", "", false)
	if err != nil {
		t.Fatalf("ListSignals all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 total signals, got %d", len(all))
	}

	if sigs[0].ID == 0 {
		t.Error("expected non-zero ID")
	}
}

func TestCompleteAndReopenSignal(t *testing.T) {
	db := testDB(t)

	now := time.Now()
	InsertSignal(db, SignalRecord{Source: "gmail", Title: "Alice", Preview: "alert", SourceTS: "2:30 PM", CapturedAt: now})

	sigs, _ := ListSignals(db, "gmail", "", false)
	if len(sigs) != 1 {
		t.Fatalf("expected 1, got %d", len(sigs))
	}
	id := sigs[0].ID

	err := CompleteSignal(db, id)
	if err != nil {
		t.Fatalf("CompleteSignal: %v", err)
	}

	active, _ := ListSignals(db, "gmail", "", false)
	if len(active) != 0 {
		t.Fatalf("expected 0 active after complete, got %d", len(active))
	}

	all, _ := ListSignals(db, "gmail", "", true)
	if len(all) != 1 {
		t.Fatalf("expected 1 total, got %d", len(all))
	}
	if all[0].CompletedAt == nil {
		t.Fatal("expected completed_at to be set")
	}

	err = ReopenSignal(db, id)
	if err != nil {
		t.Fatalf("ReopenSignal: %v", err)
	}

	active, _ = ListSignals(db, "gmail", "", false)
	if len(active) != 1 {
		t.Fatalf("expected 1 active after reopen, got %d", len(active))
	}
	if !active[0].Pinned {
		t.Error("expected pinned=true after reopen")
	}

	err = CompleteSignal(db, 9999)
	if err == nil {
		t.Fatal("expected error for non-existent ID")
	}
}

func TestReconcileSignals(t *testing.T) {
	db := testDB(t)

	now := time.Now()

	items1 := []SignalRecord{
		{Title: "Alice", Preview: "alert1", SourceTS: "2:30 PM"},
		{Title: "Bob", Preview: "sync", SourceTS: "3:00 PM"},
		{Title: "CI Bot", Preview: "build failed", SourceTS: "3:15 PM"},
	}
	err := ReconcileSignals(db, "gmail", "", items1, now)
	if err != nil {
		t.Fatalf("Reconcile 1: %v", err)
	}

	active, _ := ListSignals(db, "gmail", "", false)
	if len(active) != 3 {
		t.Fatalf("expected 3 active after scrape 1, got %d", len(active))
	}

	// Scrape 2: Alice disappears, Dave appears.
	items2 := []SignalRecord{
		{Title: "Bob", Preview: "sync", SourceTS: "3:00 PM"},
		{Title: "CI Bot", Preview: "build failed", SourceTS: "3:15 PM"},
		{Title: "Dave", Preview: "deploy", SourceTS: "4:00 PM"},
	}
	err = ReconcileSignals(db, "gmail", "", items2, now)
	if err != nil {
		t.Fatalf("Reconcile 2: %v", err)
	}

	active, _ = ListSignals(db, "gmail", "", false)
	if len(active) != 3 {
		t.Fatalf("expected 3 active after scrape 2, got %d", len(active))
	}

	all, _ := ListSignals(db, "gmail", "", true)
	if len(all) != 4 {
		t.Fatalf("expected 4 total, got %d", len(all))
	}
	var aliceSig *SignalRecord
	for i := range all {
		if all[i].Title == "Alice" {
			aliceSig = &all[i]
		}
	}
	if aliceSig == nil {
		t.Fatal("Alice signal not found")
	}
	if aliceSig.CompletedAt == nil {
		t.Fatal("expected Alice to be auto-completed")
	}
	if !aliceSig.AutoCompleted {
		t.Fatal("expected auto_completed=true for Alice")
	}
}

func TestReconcileSignals_EpisodeBased(t *testing.T) {
	db := testDB(t)

	// Scrape 1: #random is unread.
	t1 := time.Date(2026, 2, 17, 13, 0, 0, 0, time.UTC)
	items1 := []SignalRecord{
		{Title: "#random", Preview: "unread"},
	}
	if err := ReconcileSignals(db, "slack", "", items1, t1); err != nil {
		t.Fatalf("Reconcile 1: %v", err)
	}

	active, _ := ListSignals(db, "slack", "", false)
	if len(active) != 1 {
		t.Fatalf("expected 1 active, got %d", len(active))
	}
	episode1ID := active[0].ID

	// Scrape 2: still unread — same episode, no new signal.
	t2 := time.Date(2026, 2, 17, 14, 0, 0, 0, time.UTC)
	if err := ReconcileSignals(db, "slack", "", items1, t2); err != nil {
		t.Fatalf("Reconcile 2: %v", err)
	}

	active, _ = ListSignals(db, "slack", "", false)
	if len(active) != 1 {
		t.Fatalf("expected 1 active (same episode), got %d", len(active))
	}
	if active[0].ID != episode1ID {
		t.Fatal("expected same signal ID for continuing episode")
	}

	// Scrape 3: user read #random, channel gone from scrape.
	t3 := time.Date(2026, 2, 17, 14, 30, 0, 0, time.UTC)
	if err := ReconcileSignals(db, "slack", "", []SignalRecord{}, t3); err != nil {
		t.Fatalf("Reconcile 3: %v", err)
	}

	active, _ = ListSignals(db, "slack", "", false)
	if len(active) != 0 {
		t.Fatalf("expected 0 active after read, got %d", len(active))
	}

	// Scrape 4: new unreads in #random — should create NEW episode, not reactivate.
	t4 := time.Date(2026, 2, 17, 15, 0, 0, 0, time.UTC)
	if err := ReconcileSignals(db, "slack", "", items1, t4); err != nil {
		t.Fatalf("Reconcile 4: %v", err)
	}

	active, _ = ListSignals(db, "slack", "", false)
	if len(active) != 1 {
		t.Fatalf("expected 1 active (new episode), got %d", len(active))
	}
	if active[0].ID == episode1ID {
		t.Fatal("expected new signal ID for new episode, got same as episode 1")
	}

	// Scrape 5: still unread — same new episode.
	t5 := time.Date(2026, 2, 17, 16, 0, 0, 0, time.UTC)
	if err := ReconcileSignals(db, "slack", "", items1, t5); err != nil {
		t.Fatalf("Reconcile 5: %v", err)
	}

	active, _ = ListSignals(db, "slack", "", false)
	if len(active) != 1 {
		t.Fatalf("expected 1 active (same new episode), got %d", len(active))
	}

	// Total: 2 signals (1 completed episode + 1 active episode).
	all, _ := ListSignals(db, "slack", "", true)
	if len(all) != 2 {
		t.Fatalf("expected 2 total signals (2 episodes), got %d", len(all))
	}
}

func TestReconcileSignals_PinnedNotAutoCompleted(t *testing.T) {
	db := testDB(t)

	now := time.Now()

	items1 := []SignalRecord{
		{Title: "Alice", Preview: "alert", SourceTS: "2:30 PM"},
	}
	ReconcileSignals(db, "gmail", "", items1, now)

	sigs, _ := ListSignals(db, "gmail", "", false)
	CompleteSignal(db, sigs[0].ID)
	ReopenSignal(db, sigs[0].ID)

	items2 := []SignalRecord{
		{Title: "Bob", Preview: "hello", SourceTS: "3:00 PM"},
	}
	ReconcileSignals(db, "gmail", "", items2, now)

	active, _ := ListSignals(db, "gmail", "", false)
	foundAlice := false
	for _, s := range active {
		if s.Title == "Alice" {
			foundAlice = true
		}
	}
	if !foundAlice {
		t.Fatal("pinned signal should not be auto-completed")
	}
}

func TestSignalSnippet(t *testing.T) {
	db := testDB(t)

	now := time.Now()
	err := InsertSignal(db, SignalRecord{
		Source:     "gmail",
		Title:      "Alice",
		Preview:    "Project update",
		Snippet:    "Hey team, the deploy is done.",
		SourceTS:   "2:30 PM",
		CapturedAt: now,
	})
	if err != nil {
		t.Fatalf("InsertSignal: %v", err)
	}

	sigs, err := ListSignals(db, "gmail", "", false)
	if err != nil {
		t.Fatalf("ListSignals: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("expected 1, got %d", len(sigs))
	}
	if sigs[0].Snippet != "Hey team, the deploy is done." {
		t.Errorf("Snippet = %q, want 'Hey team, the deploy is done.'", sigs[0].Snippet)
	}
}

func TestReconcileSignals_ManualCompleteCreatesNewEpisode(t *testing.T) {
	db := testDB(t)

	t1 := time.Date(2026, 2, 17, 13, 0, 0, 0, time.UTC)
	items := []SignalRecord{
		{Title: "#random", Preview: "unread"},
	}
	ReconcileSignals(db, "slack", "", items, t1)

	sigs, _ := ListSignals(db, "slack", "", false)
	episode1ID := sigs[0].ID
	CompleteSignal(db, episode1ID)

	// Re-scrape with same channel still unread — should create new episode.
	t2 := time.Date(2026, 2, 17, 14, 0, 0, 0, time.UTC)
	ReconcileSignals(db, "slack", "", items, t2)

	active, _ := ListSignals(db, "slack", "", false)
	if len(active) != 1 {
		t.Fatalf("expected 1 active (new episode), got %d", len(active))
	}
	if active[0].ID == episode1ID {
		t.Fatal("expected new episode, not reactivation of manually completed one")
	}

	// Old episode stays completed.
	all, _ := ListSignals(db, "slack", "", true)
	for _, s := range all {
		if s.ID == episode1ID && s.CompletedAt == nil {
			t.Fatal("manually completed episode should stay completed")
		}
	}
}

func TestFormatSignalsMarkdownWithSnippet(t *testing.T) {
	now := time.Now()
	sigs := []SignalRecord{
		{ID: 1, Source: "gmail", Title: "Alice", Preview: "Project update", Snippet: "Hey team, deploy is done.", SourceTS: "2:30 PM", CapturedAt: now},
		{ID: 2, Source: "gmail", Title: "Bob", Preview: "Sync notes", CapturedAt: now},
	}
	out := FormatSignalsMarkdown(sigs)
	if !strings.Contains(out, "> Hey team, deploy is done.") {
		t.Errorf("expected snippet as blockquote, got:\n%s", out)
	}
	// Signal without snippet should not have blockquote
	lines := strings.Split(out, "\n")
	bobLine := ""
	for i, line := range lines {
		if strings.Contains(line, "Bob") {
			if i+1 < len(lines) {
				bobLine = lines[i+1]
			}
			break
		}
	}
	if strings.HasPrefix(strings.TrimSpace(bobLine), ">") {
		t.Errorf("Bob should not have a blockquote line, got: %q", bobLine)
	}
}

func TestFormatSignalsJSONWithSnippet(t *testing.T) {
	now := time.Now()
	sigs := []SignalRecord{
		{ID: 1, Source: "gmail", Title: "Alice", Preview: "Project update", Snippet: "Hey team, deploy is done.", CapturedAt: now},
	}
	out, err := FormatSignalsJSON(sigs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"snippet"`) {
		t.Errorf("expected snippet field in JSON, got:\n%s", out)
	}
	if !strings.Contains(out, "Hey team, deploy is done.") {
		t.Errorf("expected snippet value in JSON, got:\n%s", out)
	}
}

func TestListSignalsIncludesLinkedGitHubEntity(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	if err := InsertSignal(db, SignalRecord{
		Source:     "gmail",
		Title:      "Review requested",
		Preview:    "https://github.com/mozilla/gecko-dev/pull/123",
		CapturedAt: now,
	}); err != nil {
		t.Fatalf("InsertSignal: %v", err)
	}

	sigs, err := ListSignals(db, "gmail", "", false)
	if err != nil {
		t.Fatalf("ListSignals: %v", err)
	}
	if _, err := ExtractGitHubFromSignals(db, sigs); err != nil {
		t.Fatalf("ExtractGitHubFromSignals: %v", err)
	}

	entities, err := ListGitHubEntities(db, GitHubFilter{})
	if err != nil {
		t.Fatalf("ListGitHubEntities: %v", err)
	}
	if len(entities) != 1 {
		t.Fatalf("expected 1 GitHub entity, got %d", len(entities))
	}
	if err := UpdateGitHubEntityStatus(db, entities[0].ID, GitHubStatusUpdate{Title: "Fix tabs", State: "merged"}); err != nil {
		t.Fatalf("UpdateGitHubEntityStatus: %v", err)
	}

	sigs, err = ListSignals(db, "gmail", "", false)
	if err != nil {
		t.Fatalf("ListSignals reload: %v", err)
	}
	if sigs[0].Entity == nil {
		t.Fatal("expected linked entity on signal")
	}
	if sigs[0].Entity.Provider != "github" || sigs[0].Entity.Kind != "pull" {
		t.Fatalf("unexpected entity: %+v", sigs[0].Entity)
	}
	if sigs[0].Entity.URL != "https://github.com/mozilla/gecko-dev/pull/123" {
		t.Fatalf("unexpected URL: %q", sigs[0].Entity.URL)
	}
	if !sigs[0].Entity.Closed || sigs[0].Entity.State != "merged" {
		t.Fatalf("expected merged linked entity, got %+v", sigs[0].Entity)
	}
}

func TestAutoCompleteSignalsForClosedEntities(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	if err := InsertSignal(db, SignalRecord{
		Source:     "gmail",
		Title:      "Bugzilla update",
		Snippet:    "See https://bugzilla.mozilla.org/show_bug.cgi?id=1900001",
		CapturedAt: now,
	}); err != nil {
		t.Fatalf("InsertSignal: %v", err)
	}

	sigs, err := ListSignals(db, "gmail", "", false)
	if err != nil {
		t.Fatalf("ListSignals: %v", err)
	}
	if _, err := ExtractBugzillaFromSignals(db, sigs); err != nil {
		t.Fatalf("ExtractBugzillaFromSignals: %v", err)
	}

	entities, err := ListBugzillaEntities(db)
	if err != nil {
		t.Fatalf("ListBugzillaEntities: %v", err)
	}
	if len(entities) != 1 {
		t.Fatalf("expected 1 Bugzilla entity, got %d", len(entities))
	}
	if err := UpdateBugzillaEntityStatus(db, entities[0].ID, BugzillaStatusUpdate{Title: "Crash", Status: "RESOLVED", Resolution: "FIXED"}); err != nil {
		t.Fatalf("UpdateBugzillaEntityStatus: %v", err)
	}

	updated, err := AutoCompleteSignalsForClosedEntities(db)
	if err != nil {
		t.Fatalf("AutoCompleteSignalsForClosedEntities: %v", err)
	}
	if updated != 1 {
		t.Fatalf("expected 1 auto-completed signal, got %d", updated)
	}

	all, err := ListSignals(db, "gmail", "", true)
	if err != nil {
		t.Fatalf("ListSignals all: %v", err)
	}
	if all[0].CompletedAt == nil || !all[0].AutoCompleted {
		t.Fatalf("expected completed auto-closed signal, got %+v", all[0])
	}
}

func TestFormatSignalsOutputsLinkedEntity(t *testing.T) {
	now := time.Now()
	sigs := []SignalRecord{{
		ID:         1,
		Source:     "gmail",
		Title:      "PR merged",
		Preview:    "done",
		CapturedAt: now,
		Entity: &SignalEntityLink{
			Provider: "github",
			Kind:     "pull",
			Label:    "mozilla/gecko-dev#55",
			URL:      "https://github.com/mozilla/gecko-dev/pull/55",
			State:    "merged",
			Closed:   true,
		},
	}}

	md := FormatSignalsMarkdown(sigs)
	if !strings.Contains(md, "Link: mozilla/gecko-dev#55 [merged]") {
		t.Fatalf("expected markdown linked entity, got:\n%s", md)
	}

	jsonOut, err := FormatSignalsJSON(sigs)
	if err != nil {
		t.Fatalf("FormatSignalsJSON: %v", err)
	}
	if !strings.Contains(jsonOut, `"linked_entity"`) || !strings.Contains(jsonOut, `"closed": true`) {
		t.Fatalf("expected linked entity JSON, got:\n%s", jsonOut)
	}
}
