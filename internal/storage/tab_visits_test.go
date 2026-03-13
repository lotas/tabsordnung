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

func TestActivityPeriodBounds(t *testing.T) {
	loc := time.FixedZone("UTC-5", -5*60*60)
	ts := time.Date(2026, time.March, 11, 14, 30, 0, 0, loc)

	dayFrom, dayTo, dayLabel := ActivityPeriodBounds(ActivityPeriodDay, ts, loc)
	if dayLabel != "2026-03-11" {
		t.Fatalf("day label = %q, want 2026-03-11", dayLabel)
	}
	if dayTo.Sub(dayFrom) != 24*time.Hour {
		t.Fatalf("day span = %s, want 24h", dayTo.Sub(dayFrom))
	}

	weekFrom, weekTo, weekLabel := ActivityPeriodBounds(ActivityPeriodWeek, ts, loc)
	if weekLabel != "Week of 2026-03-09" {
		t.Fatalf("week label = %q, want Week of 2026-03-09", weekLabel)
	}
	if weekTo.Sub(weekFrom) != 7*24*time.Hour {
		t.Fatalf("week span = %s, want 168h", weekTo.Sub(weekFrom))
	}

	monthFrom, monthTo, monthLabel := ActivityPeriodBounds(ActivityPeriodMonth, ts, loc)
	if monthLabel != "2026-03" {
		t.Fatalf("month label = %q, want 2026-03", monthLabel)
	}
	if monthFrom.Day() != 1 || monthTo.Day() != 1 || monthTo.Month() != time.April {
		t.Fatalf("month bounds = [%s, %s), want March 1 to April 1", monthFrom, monthTo)
	}
}

func TestListActivityPeriods(t *testing.T) {
	db := testDB(t)
	loc := time.UTC

	insert := func(ts time.Time, title string) {
		base := ts.UnixMilli()
		err := InsertTabVisits(db, []TabVisit{{
			URL:        "https://example.com/" + title,
			Title:      title,
			TabID:      1,
			StartedAt:  base,
			EndedAt:    base + 10000,
			DurationMs: 10000,
		}})
		if err != nil {
			t.Fatalf("InsertTabVisits(%s): %v", title, err)
		}
	}

	insert(time.Date(2026, time.March, 12, 12, 0, 0, 0, loc), "today-1")
	insert(time.Date(2026, time.March, 12, 14, 0, 0, 0, loc), "today-2")
	insert(time.Date(2026, time.March, 10, 9, 0, 0, 0, loc), "same-week")
	insert(time.Date(2026, time.March, 1, 9, 0, 0, 0, loc), "same-month")
	insert(time.Date(2026, time.February, 25, 9, 0, 0, 0, loc), "prior-month")

	days, err := ListActivityPeriods(db, ActivityPeriodDay, loc)
	if err != nil {
		t.Fatalf("ListActivityPeriods(day): %v", err)
	}
	if len(days) != 4 {
		t.Fatalf("day periods = %d, want 4", len(days))
	}
	if days[0].Label != "2026-03-12" || days[0].VisitCount != 2 {
		t.Fatalf("first day = %+v, want label 2026-03-12 count 2", days[0])
	}

	weeks, err := ListActivityPeriods(db, ActivityPeriodWeek, loc)
	if err != nil {
		t.Fatalf("ListActivityPeriods(week): %v", err)
	}
	if len(weeks) != 2 {
		t.Fatalf("week periods = %d, want 2", len(weeks))
	}
	if weeks[0].Label != "Week of 2026-03-09" || weeks[0].VisitCount != 3 {
		t.Fatalf("first week = %+v, want week of 2026-03-09 count 3", weeks[0])
	}

	months, err := ListActivityPeriods(db, ActivityPeriodMonth, loc)
	if err != nil {
		t.Fatalf("ListActivityPeriods(month): %v", err)
	}
	if len(months) != 2 {
		t.Fatalf("month periods = %d, want 2", len(months))
	}
	if months[0].Label != "2026-03" || months[0].VisitCount != 4 {
		t.Fatalf("first month = %+v, want 2026-03 count 4", months[0])
	}
}
