package storage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestExtractBugTitleFromText(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"bracket format", "[Bug 1971046] Intermittent crash in widget", "Intermittent crash in widget"},
		{"with daemon prefix", "bugzilla-daemon — [Bug 1971046] Intermittent crash in widget", "Intermittent crash in widget"},
		{"case insensitive", "[bug 12345] Fix rendering", "Fix rendering"},
		{"no match plain text", "Bug 12345 from dev@example.com", ""},
		{"no match empty", "", ""},
		{"no match no brackets", "just some text", ""},
		{"title with extra spaces", "[Bug 99999]   padded title  ", "padded title"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractBugTitleFromText(tc.input)
			if got != tc.want {
				t.Errorf("extractBugTitleFromText(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestExtractBugzillaRefFromText(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  *bugzillaRef
	}{
		{"bracket format", "[Bug 1971046] Intermittent crash", &bugzillaRef{"bugzilla.mozilla.org", 1971046}},
		{"plain format", "Bug 1971046 from dev@example.com", &bugzillaRef{"bugzilla.mozilla.org", 1971046}},
		{"lowercase", "bug 12345 is fixed", &bugzillaRef{"bugzilla.mozilla.org", 12345}},
		{"mixed case", "BUG 99999 updated", &bugzillaRef{"bugzilla.mozilla.org", 99999}},
		{"comment format", "Comment # 33 on Bug 1971046 from...", &bugzillaRef{"bugzilla.mozilla.org", 1971046}},
		{"no match", "no bugs here", nil},
		{"empty", "", nil},
		{"just number", "12345", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractBugzillaRefFromText(tc.input)
			if tc.want == nil {
				if got != nil {
					t.Errorf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected %+v, got nil", tc.want)
			}
			if got.host != tc.want.host || got.bugID != tc.want.bugID {
				t.Errorf("got {%s, %d}, want {%s, %d}", got.host, got.bugID, tc.want.host, tc.want.bugID)
			}
		})
	}
}

func TestExtractBugzillaFromSignalRecord_EmailNotification(t *testing.T) {
	// Simulates a bugzilla-daemon email notification with no URL.
	sig := SignalRecord{
		Source:  "gmail",
		Title:   "bugzilla-daemon — [Bug 1971046] Intermittent crash in widget",
		Snippet: "Comment # 33 on Bug 1971046 from dev@example.com",
	}
	ref := extractBugzillaFromSignalRecord(sig)
	if ref == nil {
		t.Fatal("expected bugzilla ref, got nil")
	}
	if ref.host != "bugzilla.mozilla.org" || ref.bugID != 1971046 {
		t.Errorf("got {%s, %d}, want {bugzilla.mozilla.org, 1971046}", ref.host, ref.bugID)
	}
}

func TestExtractBugzillaFromSnapshot_EmailTab(t *testing.T) {
	db := testDB(t)

	// Gmail tab with bug ID in title but non-Bugzilla URL.
	_, err := CreateSnapshot(db, "default", nil, []SnapshotTab{
		{URL: "https://mail.google.com/mail/u/0/#inbox/abc123",
			Title: "[Bug 1971046] Intermittent crash in widget"},
	}, "")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	var snapID int64
	db.QueryRow("SELECT id FROM snapshots WHERE profile = 'default' AND rev = 1").Scan(&snapID)

	count, err := ExtractBugzillaFromSnapshot(db, snapID)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 entity, got %d", count)
	}

	entities, _ := ListBugzillaEntities(db)
	if len(entities) != 1 {
		t.Fatalf("want 1 entity, got %d", len(entities))
	}
	if entities[0].BugID != 1971046 {
		t.Errorf("BugID = %d, want 1971046", entities[0].BugID)
	}
	if entities[0].Host != "bugzilla.mozilla.org" {
		t.Errorf("Host = %q, want bugzilla.mozilla.org", entities[0].Host)
	}
	if entities[0].Title != "Intermittent crash in widget" {
		t.Errorf("Title = %q, want %q", entities[0].Title, "Intermittent crash in widget")
	}
}

func TestExtractBugzillaFromSignals_EmailNotification(t *testing.T) {
	db := testDB(t)

	now := time.Now()
	// Signal with no Bugzilla URL, only text reference.
	InsertSignal(db, SignalRecord{
		Source:     "gmail",
		Title:      "bugzilla-daemon — [Bug 1971046] Intermittent crash",
		Snippet:    "Comment # 33 on Bug 1971046 from dev@example.com",
		SourceTS:   "1:00 PM",
		CapturedAt: now,
	})

	signals, _ := ListSignals(db, "", false)
	count, err := ExtractBugzillaFromSignals(db, signals)
	if err != nil {
		t.Fatalf("ExtractBugzillaFromSignals: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1, got %d", count)
	}

	entities, _ := ListBugzillaEntities(db)
	if len(entities) != 1 || entities[0].BugID != 1971046 {
		t.Fatalf("unexpected entities: %+v", entities)
	}
	if entities[0].Title != "Intermittent crash" {
		t.Errorf("Title = %q, want %q", entities[0].Title, "Intermittent crash")
	}
}

func TestCleanBugzillaTabTitle(t *testing.T) {
	cases := []struct{ input, want string }{
		{"Bug 1900001 \u2013 Crash on startup \u2013 Bugzilla", "Crash on startup"},
		{"Bug 12345 - Fix rendering issue - Bugzilla", "Fix rendering issue"},
		{"Bug 1 \u2013 Short \u2013 Bugzilla", "Short"},
		{"[Bug 1971046] Intermittent crash in widget", "Intermittent crash in widget"},
		{"Some random page title", "Some random page title"},
		{"", ""},
	}
	for _, tc := range cases {
		got := CleanBugzillaTabTitle(tc.input)
		if got != tc.want {
			t.Errorf("CleanBugzillaTabTitle(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestUpdateBugzillaEntityStatus(t *testing.T) {
	db := testDB(t)
	id, _, err := UpsertBugzillaEntity(db, "bugzilla.mozilla.org", 9000002, "tab")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	update := BugzillaStatusUpdate{
		Title: "Fix memory leak", Status: "RESOLVED",
		Resolution: "FIXED", Assignee: "dev@example.com",
	}
	if err := UpdateBugzillaEntityStatus(db, id, update); err != nil {
		t.Fatalf("update: %v", err)
	}
	entities, _ := ListBugzillaEntities(db)
	if len(entities) != 1 {
		t.Fatalf("want 1, got %d", len(entities))
	}
	e := entities[0]
	if e.Title != "Fix memory leak" || e.Status != "RESOLVED" || e.Resolution != "FIXED" || e.Assignee != "dev@example.com" {
		t.Errorf("unexpected: %+v", e)
	}
	if e.LastRefreshedAt == nil {
		t.Error("LastRefreshedAt should be set")
	}
}

func TestExtractBugzillaFromSnapshot_WithTitle(t *testing.T) {
	db := testDB(t)
	_, err := CreateSnapshot(db, "default", nil, []SnapshotTab{
		{URL: "https://bugzilla.mozilla.org/show_bug.cgi?id=9000003",
			Title: "Bug 9000003 \u2013 Test crash \u2013 Bugzilla"},
	}, "")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	var snapID int64
	db.QueryRow("SELECT id FROM snapshots WHERE profile = 'default' AND rev = 1").Scan(&snapID)
	if _, err := ExtractBugzillaFromSnapshot(db, snapID); err != nil {
		t.Fatalf("extract: %v", err)
	}
	entities, _ := ListBugzillaEntities(db)
	if len(entities) != 1 {
		t.Fatalf("want 1 entity")
	}
	if entities[0].Title != "Test crash" {
		t.Errorf("Title = %q, want 'Test crash'", entities[0].Title)
	}
}

func TestExtractBugzillaFromSnapshot(t *testing.T) {
	db := testDB(t)

	_, err := CreateSnapshot(db, "default", nil, []SnapshotTab{
		{URL: "https://bugzilla.mozilla.org/show_bug.cgi?id=1900001", Title: "Bugzilla bug"},
		{URL: "https://example.com", Title: "Example"},
		{URL: "https://landfill.bugzilla.org/rest/bug/12345", Title: "Bugzilla REST"},
	}, "")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	var snapID int64
	db.QueryRow("SELECT id FROM snapshots WHERE profile = 'default' AND rev = 1").Scan(&snapID)

	count, err := ExtractBugzillaFromSnapshot(db, snapID)
	if err != nil {
		t.Fatalf("ExtractBugzillaFromSnapshot: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 entities extracted, got %d", count)
	}

	entities, err := ListBugzillaEntities(db)
	if err != nil {
		t.Fatalf("ListBugzillaEntities: %v", err)
	}
	if len(entities) != 2 {
		t.Fatalf("expected 2 entities, got %d", len(entities))
	}
}

func TestExtractBugzillaFromSignals(t *testing.T) {
	db := testDB(t)

	now := time.Now()
	InsertSignal(db, SignalRecord{
		Source: "gmail", Title: "bugzilla-daemon", Preview: "Bug updated",
		Snippet:  "See https://bugzilla.mozilla.org/show_bug.cgi?id=1900001",
		SourceTS: "1:00 PM", CapturedAt: now,
	})
	InsertSignal(db, SignalRecord{
		Source: "gmail", Title: "another sender", Preview: "rest endpoint",
		Snippet:  "https://landfill.bugzilla.org/rest/bug/12345 changed",
		SourceTS: "2:00 PM", CapturedAt: now,
	})
	InsertSignal(db, SignalRecord{
		Source: "slack", Title: "#general", Preview: "no bug", SourceTS: "3:00 PM", CapturedAt: now,
	})

	signals, _ := ListSignals(db, "", false)
	count, err := ExtractBugzillaFromSignals(db, signals)
	if err != nil {
		t.Fatalf("ExtractBugzillaFromSignals: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 entities extracted, got %d", count)
	}

	entities, _ := ListBugzillaEntities(db)
	if len(entities) != 2 {
		t.Fatalf("expected 2 entities, got %d", len(entities))
	}
}

func TestBackfillBugzillaEntities(t *testing.T) {
	db := testDB(t)

	CreateSnapshot(db, "default", nil, []SnapshotTab{
		{URL: "https://bugzilla.mozilla.org/show_bug.cgi?id=1900001", Title: "Bug 1"},
		{URL: "https://example.com", Title: "Example"},
	}, "")
	CreateSnapshot(db, "default", nil, []SnapshotTab{
		{URL: "https://bugzilla.mozilla.org/show_bug.cgi?id=1900001", Title: "Bug 1"},
		{URL: "https://landfill.bugzilla.org/rest/bug/12345", Title: "Bug 2"},
	}, "")

	InsertSignal(db, SignalRecord{
		Source: "gmail", Title: "bugzilla-daemon", Preview: "update",
		Snippet:  "Bug link https://bugzilla.mozilla.org/show_bug.cgi?id=1900002",
		SourceTS: "4:00 PM", CapturedAt: time.Now(),
	})

	db.Exec("DELETE FROM bugzilla_entity_events")
	db.Exec("DELETE FROM bugzilla_entities")

	count, err := BackfillBugzillaEntities(db)
	if err != nil {
		t.Fatalf("BackfillBugzillaEntities: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected 3 entities backfilled, got %d", count)
	}

	entities, _ := ListBugzillaEntities(db)
	if len(entities) != 3 {
		t.Fatalf("expected 3 entities in db, got %d", len(entities))
	}

	count2, _ := BackfillBugzillaEntities(db)
	if count2 != 3 {
		t.Fatalf("expected 3 entities on second run, got %d", count2)
	}
}

func TestFormatBugzillaMarkdown(t *testing.T) {
	now := time.Date(2026, 2, 26, 12, 0, 0, 0, time.UTC)
	entities := []BugzillaEntity{
		{
			ID:              1,
			Host:            "bugzilla.mozilla.org",
			BugID:           1900001,
			FirstSeenAt:     now.Add(-48 * time.Hour),
			FirstSeenSource: "tab",
		},
		{
			ID:          2,
			Host:        "landfill.bugzilla.org",
			BugID:       12345,
			FirstSeenAt: now.Add(-24 * time.Hour),
		},
	}
	events := map[int64][]BugzillaEntityEvent{
		2: {
			{EventType: "signal_seen"},
		},
	}

	out := FormatBugzillaMarkdown(entities, events)
	if !strings.Contains(out, "## bugzilla.mozilla.org (1)") {
		t.Fatalf("expected bugzilla.mozilla.org group header, got:\n%s", out)
	}
	if !strings.Contains(out, "- bugzilla.mozilla.org#1900001") {
		t.Fatalf("expected issue line, got:\n%s", out)
	}
	if !strings.Contains(out, "First seen: "+entities[1].FirstSeenAt.Format("2006-01-02")+" (signal)") {
		t.Fatalf("expected fallback first-seen source from events, got:\n%s", out)
	}

	empty := FormatBugzillaMarkdown(nil, nil)
	if empty != "No Bugzilla issues found.\n" {
		t.Fatalf("unexpected empty output: %q", empty)
	}
}

func TestFormatBugzillaJSON(t *testing.T) {
	entities := []BugzillaEntity{
		{
			Host:            "bugzilla.mozilla.org",
			BugID:           1900001,
			FirstSeenAt:     time.Date(2026, 2, 20, 10, 0, 0, 0, time.UTC),
			FirstSeenSource: "signal",
		},
	}

	out, err := FormatBugzillaJSON(entities)
	if err != nil {
		t.Fatalf("FormatBugzillaJSON: %v", err)
	}

	var got []BugzillaJSONOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json.Unmarshal: %v\noutput:\n%s", err, out)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(got))
	}
	row := got[0]
	if row.Host != "bugzilla.mozilla.org" || row.BugID != 1900001 {
		t.Fatalf("unexpected identity fields: %+v", row)
	}
	if row.URL != "https://bugzilla.mozilla.org/show_bug.cgi?id=1900001" {
		t.Fatalf("unexpected url: %q", row.URL)
	}
	if row.FirstSeenAt != "2026-02-20T10:00:00Z" || row.FirstSeenSource != "signal" {
		t.Fatalf("unexpected first seen fields: %+v", row)
	}
}
