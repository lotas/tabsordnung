package storage

import (
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

	sigs, err := ListSignals(db, "gmail", false)
	if err != nil {
		t.Fatalf("ListSignals: %v", err)
	}
	if len(sigs) != 2 {
		t.Fatalf("expected 2 gmail signals, got %d", len(sigs))
	}

	all, err := ListSignals(db, "", false)
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

	sigs, _ := ListSignals(db, "gmail", false)
	if len(sigs) != 1 {
		t.Fatalf("expected 1, got %d", len(sigs))
	}
	id := sigs[0].ID

	err := CompleteSignal(db, id)
	if err != nil {
		t.Fatalf("CompleteSignal: %v", err)
	}

	active, _ := ListSignals(db, "gmail", false)
	if len(active) != 0 {
		t.Fatalf("expected 0 active after complete, got %d", len(active))
	}

	all, _ := ListSignals(db, "gmail", true)
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

	active, _ = ListSignals(db, "gmail", false)
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
	err := ReconcileSignals(db, "gmail", items1, now)
	if err != nil {
		t.Fatalf("Reconcile 1: %v", err)
	}

	active, _ := ListSignals(db, "gmail", false)
	if len(active) != 3 {
		t.Fatalf("expected 3 active after scrape 1, got %d", len(active))
	}

	items2 := []SignalRecord{
		{Title: "Bob", Preview: "sync", SourceTS: "3:00 PM"},
		{Title: "CI Bot", Preview: "build failed", SourceTS: "3:15 PM"},
		{Title: "Dave", Preview: "deploy", SourceTS: "4:00 PM"},
	}
	err = ReconcileSignals(db, "gmail", items2, now)
	if err != nil {
		t.Fatalf("Reconcile 2: %v", err)
	}

	active, _ = ListSignals(db, "gmail", false)
	if len(active) != 3 {
		t.Fatalf("expected 3 active after scrape 2, got %d", len(active))
	}

	all, _ := ListSignals(db, "gmail", true)
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

	items3 := []SignalRecord{
		{Title: "Alice", Preview: "alert1", SourceTS: "2:30 PM"},
		{Title: "Bob", Preview: "sync", SourceTS: "3:00 PM"},
	}
	err = ReconcileSignals(db, "gmail", items3, now)
	if err != nil {
		t.Fatalf("Reconcile 3: %v", err)
	}

	active, _ = ListSignals(db, "gmail", false)
	foundAlice := false
	for _, s := range active {
		if s.Title == "Alice" {
			foundAlice = true
			if s.CompletedAt != nil {
				t.Error("expected Alice to be active again")
			}
		}
	}
	if !foundAlice {
		t.Fatal("expected Alice in active list after reappearing")
	}
}

func TestReconcileSignals_PinnedNotAutoCompleted(t *testing.T) {
	db := testDB(t)

	now := time.Now()

	items1 := []SignalRecord{
		{Title: "Alice", Preview: "alert", SourceTS: "2:30 PM"},
	}
	ReconcileSignals(db, "gmail", items1, now)

	sigs, _ := ListSignals(db, "gmail", false)
	CompleteSignal(db, sigs[0].ID)
	ReopenSignal(db, sigs[0].ID)

	items2 := []SignalRecord{
		{Title: "Bob", Preview: "hello", SourceTS: "3:00 PM"},
	}
	ReconcileSignals(db, "gmail", items2, now)

	active, _ := ListSignals(db, "gmail", false)
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

func TestReconcileSignals_ManuallyCompletedStaysCompleted(t *testing.T) {
	db := testDB(t)

	now := time.Now()

	items1 := []SignalRecord{
		{Title: "Alice", Preview: "alert", SourceTS: "2:30 PM"},
	}
	ReconcileSignals(db, "gmail", items1, now)

	sigs, _ := ListSignals(db, "gmail", false)
	CompleteSignal(db, sigs[0].ID)

	ReconcileSignals(db, "gmail", items1, now)

	active, _ := ListSignals(db, "gmail", false)
	for _, s := range active {
		if s.Title == "Alice" {
			t.Fatal("manually completed signal should not be reactivated")
		}
	}
}
