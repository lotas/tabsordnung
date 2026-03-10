package storage

import (
	"testing"
	"time"
)

func TestInsertAndQueryTabVisits(t *testing.T) {
	db := testDB(t)

	now := time.Now().Truncate(time.Second)
	base := now.UnixMilli()

	visits := []TabVisit{
		{URL: "https://github.com/foo/bar/pull/1", Title: "PR #1", TabID: 10, StartedAt: base, EndedAt: base + 30000, DurationMs: 30000},
		{URL: "https://github.com/foo/bar/pull/1", Title: "PR #1", TabID: 10, StartedAt: base + 60000, EndedAt: base + 90000, DurationMs: 30000},
		{URL: "https://example.com", Title: "Example", TabID: 11, StartedAt: base + 5000, EndedAt: base + 15000, DurationMs: 10000},
	}

	if err := InsertTabVisits(db, visits); err != nil {
		t.Fatalf("InsertTabVisits: %v", err)
	}

	from := time.UnixMilli(base - 1000)
	to := time.UnixMilli(base + 200000)
	summary, err := QueryTabVisitSummary(db, from, to)
	if err != nil {
		t.Fatalf("QueryTabVisitSummary: %v", err)
	}

	if len(summary) != 2 {
		t.Fatalf("got %d rows, want 2", len(summary))
	}
	// First row should be github PR (2 visits)
	if summary[0].Visits != 2 {
		t.Errorf("first row visits = %d, want 2", summary[0].Visits)
	}
	if summary[0].TotalMs != 60000 {
		t.Errorf("first row TotalMs = %d, want 60000", summary[0].TotalMs)
	}
	// Second row: example (1 visit)
	if summary[1].Visits != 1 {
		t.Errorf("second row visits = %d, want 1", summary[1].Visits)
	}
}

func TestQueryTabVisitsOutOfRange(t *testing.T) {
	db := testDB(t)

	base := time.Now().UnixMilli()
	visits := []TabVisit{
		{URL: "https://example.com", Title: "Example", TabID: 1, StartedAt: base, EndedAt: base + 10000, DurationMs: 10000},
	}
	if err := InsertTabVisits(db, visits); err != nil {
		t.Fatalf("InsertTabVisits: %v", err)
	}

	// Query a range that doesn't include the visit
	from := time.UnixMilli(base + 100000)
	to := time.UnixMilli(base + 200000)
	summary, err := QueryTabVisitSummary(db, from, to)
	if err != nil {
		t.Fatalf("QueryTabVisitSummary: %v", err)
	}
	if len(summary) != 0 {
		t.Errorf("expected 0 rows, got %d", len(summary))
	}
}

func TestInsertTabVisitsEmpty(t *testing.T) {
	db := testDB(t)
	// Should not error on empty slice
	if err := InsertTabVisits(db, nil); err != nil {
		t.Fatalf("InsertTabVisits(nil): %v", err)
	}
}

func TestInsertTabVisitsIgnoresDuplicateRetries(t *testing.T) {
	db := testDB(t)

	base := time.Now().UnixMilli()
	visit := TabVisit{
		URL:        "https://example.com",
		Title:      "Example",
		TabID:      7,
		StartedAt:  base,
		EndedAt:    base + 10000,
		DurationMs: 10000,
	}

	if err := InsertTabVisits(db, []TabVisit{visit}); err != nil {
		t.Fatalf("first InsertTabVisits: %v", err)
	}
	if err := InsertTabVisits(db, []TabVisit{visit}); err != nil {
		t.Fatalf("retry InsertTabVisits: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tab_visits`).Scan(&count); err != nil {
		t.Fatalf("count tab_visits: %v", err)
	}
	if count != 1 {
		t.Fatalf("tab_visits count = %d, want 1", count)
	}
}
