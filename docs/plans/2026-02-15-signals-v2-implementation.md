# Signals v2 Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace file-based signal storage with SQLite-backed persistent signals, add CLI subcommands, and make TUI signal list navigable with complete/reopen actions.

**Architecture:** Add a `signals` table to the existing `internal/storage/` package (same DB as snapshots). Add `Timestamp` field to `SignalItem`. CLI commands query the DB directly. TUI replaces markdown reads with DB queries and adds a navigable signal list with cursor.

**Tech Stack:** Go, SQLite (modernc.org/sqlite), Bubble Tea, existing WebSocket extension bridge.

---

### Task 1: Add signals table migration

**Files:**
- Modify: `internal/storage/storage.go` (add migration 3)
- Modify: `internal/storage/storage_test.go` (add test)

**Step 1: Write the failing test**

Add to `internal/storage/storage_test.go`:

```go
func TestSignalsTableExists(t *testing.T) {
	db := testDB(t)

	// Insert a signal row to verify the table exists and has the right schema.
	_, err := db.Exec(`INSERT INTO signals (source, title, preview, source_ts, captured_at)
		VALUES ('gmail', 'Alice', 'hello', '2:30 PM', CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatalf("insert into signals: %v", err)
	}

	// Verify unique constraint.
	_, err = db.Exec(`INSERT INTO signals (source, title, preview, source_ts, captured_at)
		VALUES ('gmail', 'Alice', 'different preview', '2:30 PM', CURRENT_TIMESTAMP)`)
	if err == nil {
		t.Fatal("expected unique constraint violation")
	}

	// Verify different source_ts is allowed.
	_, err = db.Exec(`INSERT INTO signals (source, title, preview, source_ts, captured_at)
		VALUES ('gmail', 'Alice', 'hello', '3:00 PM', CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatalf("insert with different source_ts: %v", err)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `GOMAXPROCS=1 go test -p 1 -run TestSignalsTableExists ./internal/storage/ -v`
Expected: FAIL — table "signals" does not exist

**Step 3: Write minimal implementation**

Add migration 3 to the `migrations` slice in `internal/storage/storage.go`:

```go
{
	Version:     3,
	Description: "create signals table",
	SQL: `
CREATE TABLE signals (
    id              INTEGER PRIMARY KEY,
    source          TEXT NOT NULL,
    title           TEXT NOT NULL,
    preview         TEXT DEFAULT '',
    source_ts       TEXT NOT NULL DEFAULT '',
    captured_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at    DATETIME,
    auto_completed  BOOLEAN DEFAULT 0,
    pinned          BOOLEAN DEFAULT 0,
    UNIQUE(source, title, source_ts)
);`,
},
```

**Step 4: Run test to verify it passes**

Run: `GOMAXPROCS=1 go test -p 1 -run TestSignalsTableExists ./internal/storage/ -v`
Expected: PASS

Also run: `GOMAXPROCS=1 go test -p 1 ./internal/storage/ -v`
Expected: All existing tests still pass.

**Step 5: Commit**

```bash
git add internal/storage/storage.go internal/storage/storage_test.go
git commit -m "Add signals table migration (v3)"
```

---

### Task 2: Signal types and InsertSignal

**Files:**
- Create: `internal/storage/signals.go`
- Create: `internal/storage/signals_test.go`

**Step 1: Write the failing test**

Create `internal/storage/signals_test.go`:

```go
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
```

**Step 2: Run test to verify it fails**

Run: `GOMAXPROCS=1 go test -p 1 -run TestInsertSignal ./internal/storage/ -v`
Expected: FAIL — undefined: SignalRecord, InsertSignal

**Step 3: Write minimal implementation**

Create `internal/storage/signals.go`:

```go
package storage

import (
	"database/sql"
	"time"
)

// SignalRecord represents a single signal item stored in the database.
type SignalRecord struct {
	ID            int64
	Source        string
	Title         string
	Preview       string
	SourceTS      string
	CapturedAt    time.Time
	CompletedAt   *time.Time
	AutoCompleted bool
	Pinned        bool
}

// InsertSignal inserts a signal, silently ignoring duplicates (same source+title+source_ts).
func InsertSignal(db *sql.DB, sig SignalRecord) error {
	_, err := db.Exec(
		`INSERT OR IGNORE INTO signals (source, title, preview, source_ts, captured_at)
		 VALUES (?, ?, ?, ?, ?)`,
		sig.Source, sig.Title, sig.Preview, sig.SourceTS, sig.CapturedAt,
	)
	return err
}
```

**Step 4: Run test to verify it passes**

Run: `GOMAXPROCS=1 go test -p 1 -run TestInsertSignal ./internal/storage/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/storage/signals.go internal/storage/signals_test.go
git commit -m "Add SignalRecord type and InsertSignal"
```

---

### Task 3: ListSignals — query active/all signals by source

**Files:**
- Modify: `internal/storage/signals.go`
- Modify: `internal/storage/signals_test.go`

**Step 1: Write the failing test**

Add to `internal/storage/signals_test.go`:

```go
func TestListSignals(t *testing.T) {
	db := testDB(t)

	now := time.Now()
	InsertSignal(db, SignalRecord{Source: "gmail", Title: "Alice", Preview: "alert", SourceTS: "2:30 PM", CapturedAt: now})
	InsertSignal(db, SignalRecord{Source: "gmail", Title: "Bob", Preview: "sync", SourceTS: "3:00 PM", CapturedAt: now})
	InsertSignal(db, SignalRecord{Source: "slack", Title: "#ops", Preview: "unread", SourceTS: "", CapturedAt: now})

	// List active gmail signals.
	sigs, err := ListSignals(db, "gmail", false)
	if err != nil {
		t.Fatalf("ListSignals: %v", err)
	}
	if len(sigs) != 2 {
		t.Fatalf("expected 2 gmail signals, got %d", len(sigs))
	}

	// List all signals (no source filter).
	all, err := ListSignals(db, "", false)
	if err != nil {
		t.Fatalf("ListSignals all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 total signals, got %d", len(all))
	}

	// Verify IDs are assigned.
	if sigs[0].ID == 0 {
		t.Error("expected non-zero ID")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `GOMAXPROCS=1 go test -p 1 -run TestListSignals ./internal/storage/ -v`
Expected: FAIL — undefined: ListSignals

**Step 3: Write minimal implementation**

Add to `internal/storage/signals.go`:

```go
// ListSignals returns signals. If source is non-empty, filters by source.
// If includeCompleted is false, only returns active signals (completed_at IS NULL).
// Results are ordered: active first (newest captured_at first), then completed (newest completed_at first).
func ListSignals(db *sql.DB, source string, includeCompleted bool) ([]SignalRecord, error) {
	query := `SELECT id, source, title, preview, source_ts, captured_at, completed_at, auto_completed, pinned
		FROM signals WHERE 1=1`
	var args []interface{}

	if source != "" {
		query += " AND source = ?"
		args = append(args, source)
	}
	if !includeCompleted {
		query += " AND completed_at IS NULL"
	}

	query += ` ORDER BY
		CASE WHEN completed_at IS NULL THEN 0 ELSE 1 END,
		captured_at DESC`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []SignalRecord
	for rows.Next() {
		var s SignalRecord
		var completedAt sql.NullTime
		if err := rows.Scan(&s.ID, &s.Source, &s.Title, &s.Preview, &s.SourceTS,
			&s.CapturedAt, &completedAt, &s.AutoCompleted, &s.Pinned); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			s.CompletedAt = &completedAt.Time
		}
		result = append(result, s)
	}
	return result, rows.Err()
}
```

**Step 4: Run test to verify it passes**

Run: `GOMAXPROCS=1 go test -p 1 -run TestListSignals ./internal/storage/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/storage/signals.go internal/storage/signals_test.go
git commit -m "Add ListSignals with source filter and completed flag"
```

---

### Task 4: CompleteSignal and ReopenSignal

**Files:**
- Modify: `internal/storage/signals.go`
- Modify: `internal/storage/signals_test.go`

**Step 1: Write the failing test**

Add to `internal/storage/signals_test.go`:

```go
func TestCompleteAndReopenSignal(t *testing.T) {
	db := testDB(t)

	now := time.Now()
	InsertSignal(db, SignalRecord{Source: "gmail", Title: "Alice", Preview: "alert", SourceTS: "2:30 PM", CapturedAt: now})

	// Get the signal ID.
	sigs, _ := ListSignals(db, "gmail", false)
	if len(sigs) != 1 {
		t.Fatalf("expected 1, got %d", len(sigs))
	}
	id := sigs[0].ID

	// Complete it.
	err := CompleteSignal(db, id)
	if err != nil {
		t.Fatalf("CompleteSignal: %v", err)
	}

	// Should no longer appear in active list.
	active, _ := ListSignals(db, "gmail", false)
	if len(active) != 0 {
		t.Fatalf("expected 0 active after complete, got %d", len(active))
	}

	// Should appear in all list.
	all, _ := ListSignals(db, "gmail", true)
	if len(all) != 1 {
		t.Fatalf("expected 1 total, got %d", len(all))
	}
	if all[0].CompletedAt == nil {
		t.Fatal("expected completed_at to be set")
	}

	// Reopen it.
	err = ReopenSignal(db, id)
	if err != nil {
		t.Fatalf("ReopenSignal: %v", err)
	}

	// Should be active again, with pinned=true.
	active, _ = ListSignals(db, "gmail", false)
	if len(active) != 1 {
		t.Fatalf("expected 1 active after reopen, got %d", len(active))
	}
	if !active[0].Pinned {
		t.Error("expected pinned=true after reopen")
	}

	// Complete non-existent should error.
	err = CompleteSignal(db, 9999)
	if err == nil {
		t.Fatal("expected error for non-existent ID")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `GOMAXPROCS=1 go test -p 1 -run TestCompleteAndReopenSignal ./internal/storage/ -v`
Expected: FAIL — undefined: CompleteSignal, ReopenSignal

**Step 3: Write minimal implementation**

Add to `internal/storage/signals.go`:

```go
import "fmt"

// CompleteSignal marks a signal as manually completed. Clears pinned flag.
func CompleteSignal(db *sql.DB, id int64) error {
	res, err := db.Exec(
		`UPDATE signals SET completed_at = CURRENT_TIMESTAMP, auto_completed = 0, pinned = 0
		 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("signal %d not found", id)
	}
	return nil
}

// ReopenSignal reactivates a completed signal. Sets pinned=true to prevent auto-complete.
func ReopenSignal(db *sql.DB, id int64) error {
	res, err := db.Exec(
		`UPDATE signals SET completed_at = NULL, auto_completed = 0, pinned = 1
		 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("signal %d not found", id)
	}
	return nil
}
```

Note: merge the `fmt` import with existing imports in signals.go.

**Step 4: Run test to verify it passes**

Run: `GOMAXPROCS=1 go test -p 1 -run TestCompleteAndReopenSignal ./internal/storage/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/storage/signals.go internal/storage/signals_test.go
git commit -m "Add CompleteSignal and ReopenSignal with pinned behavior"
```

---

### Task 5: ReconcileSignals — transactional scrape reconciliation

**Files:**
- Modify: `internal/storage/signals.go`
- Modify: `internal/storage/signals_test.go`

**Step 1: Write the failing test**

Add to `internal/storage/signals_test.go`:

```go
func TestReconcileSignals(t *testing.T) {
	db := testDB(t)

	now := time.Now()

	// --- Scrape 1: insert A, B, C ---
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

	// --- Scrape 2: B and C still present, A gone, D new ---
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

	// A should be auto-completed.
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

	// --- Scrape 3: A returns ---
	items3 := []SignalRecord{
		{Title: "Alice", Preview: "alert1", SourceTS: "2:30 PM"},
		{Title: "Bob", Preview: "sync", SourceTS: "3:00 PM"},
	}
	err = ReconcileSignals(db, "gmail", items3, now)
	if err != nil {
		t.Fatalf("Reconcile 3: %v", err)
	}

	// A should be reactivated.
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

	// Insert a signal.
	items1 := []SignalRecord{
		{Title: "Alice", Preview: "alert", SourceTS: "2:30 PM"},
	}
	ReconcileSignals(db, "gmail", items1, now)

	// Complete it, then reopen (makes it pinned).
	sigs, _ := ListSignals(db, "gmail", false)
	CompleteSignal(db, sigs[0].ID)
	ReopenSignal(db, sigs[0].ID)

	// Reconcile without Alice — she should NOT be auto-completed (pinned).
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

	// Manually complete Alice.
	sigs, _ := ListSignals(db, "gmail", false)
	CompleteSignal(db, sigs[0].ID)

	// Reconcile with Alice still present — she should stay completed.
	ReconcileSignals(db, "gmail", items1, now)

	active, _ := ListSignals(db, "gmail", false)
	for _, s := range active {
		if s.Title == "Alice" {
			t.Fatal("manually completed signal should not be reactivated")
		}
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `GOMAXPROCS=1 go test -p 1 -run TestReconcileSignals ./internal/storage/ -v`
Expected: FAIL — undefined: ReconcileSignals

**Step 3: Write minimal implementation**

Add to `internal/storage/signals.go`:

```go
// ReconcileSignals processes a scrape result for a source in a single transaction:
// 1. Insert new items (dedup via INSERT OR IGNORE)
// 2. Auto-complete signals missing from scrape (unless pinned)
// 3. Reactivate auto-completed signals that reappear
// Manually completed signals (auto_completed=0, completed_at IS NOT NULL) are never reactivated.
func ReconcileSignals(db *sql.DB, source string, items []SignalRecord, capturedAt time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Insert new items.
	insertStmt, err := tx.Prepare(
		`INSERT OR IGNORE INTO signals (source, title, preview, source_ts, captured_at)
		 VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insertStmt.Close()

	for _, item := range items {
		if _, err := insertStmt.Exec(source, item.Title, item.Preview, item.SourceTS, capturedAt); err != nil {
			return err
		}
	}

	// Build a set of current item keys for the WHERE NOT IN clause.
	// We use a temp table for efficient matching.
	_, err = tx.Exec(`CREATE TEMP TABLE _current_items (title TEXT, source_ts TEXT)`)
	if err != nil {
		return err
	}
	tempStmt, err := tx.Prepare(`INSERT INTO _current_items (title, source_ts) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer tempStmt.Close()
	for _, item := range items {
		if _, err := tempStmt.Exec(item.Title, item.SourceTS); err != nil {
			return err
		}
	}

	// 2. Auto-complete active signals not in current scrape (unless pinned).
	_, err = tx.Exec(`
		UPDATE signals
		SET completed_at = CURRENT_TIMESTAMP, auto_completed = 1
		WHERE source = ? AND completed_at IS NULL AND pinned = 0
		  AND (title, source_ts) NOT IN (SELECT title, source_ts FROM _current_items)`,
		source)
	if err != nil {
		return err
	}

	// 3. Reactivate auto-completed signals that reappear.
	_, err = tx.Exec(`
		UPDATE signals
		SET completed_at = NULL, auto_completed = 0
		WHERE source = ? AND auto_completed = 1 AND completed_at IS NOT NULL
		  AND (title, source_ts) IN (SELECT title, source_ts FROM _current_items)`,
		source)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`DROP TABLE _current_items`)
	if err != nil {
		return err
	}

	return tx.Commit()
}
```

**Step 4: Run tests to verify they pass**

Run: `GOMAXPROCS=1 go test -p 1 -run TestReconcileSignals ./internal/storage/ -v`
Expected: PASS

Run all storage tests: `GOMAXPROCS=1 go test -p 1 ./internal/storage/ -v`
Expected: All PASS

**Step 5: Commit**

```bash
git add internal/storage/signals.go internal/storage/signals_test.go
git commit -m "Add ReconcileSignals with auto-complete, reactivation, and pinned protection"
```

---

### Task 6: Add Timestamp field to SignalItem and update parsing

**Files:**
- Modify: `internal/signal/signal.go`
- Modify: `internal/signal/signal_test.go`

**Step 1: Write the failing test**

Add to `internal/signal/signal_test.go`:

```go
func TestParseItemsJSONWithTimestamp(t *testing.T) {
	raw := `[{"title":"Alice","preview":"hello","timestamp":"2:30 PM"},{"title":"Bob","preview":"world","timestamp":""}]`
	items, err := ParseItemsJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].Timestamp != "2:30 PM" {
		t.Errorf("items[0].Timestamp = %q, want '2:30 PM'", items[0].Timestamp)
	}
	if items[1].Timestamp != "" {
		t.Errorf("items[1].Timestamp = %q, want empty", items[1].Timestamp)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `GOMAXPROCS=1 go test -p 1 -run TestParseItemsJSONWithTimestamp ./internal/signal/ -v`
Expected: FAIL — SignalItem has no Timestamp field

**Step 3: Write minimal implementation**

In `internal/signal/signal.go`, add `Timestamp` field to `SignalItem`:

```go
type SignalItem struct {
	Title     string `json:"title"`
	Preview   string `json:"preview"`
	Timestamp string `json:"timestamp"`
}
```

Also update `deduplicateItems` to include Timestamp in the key:

```go
func deduplicateItems(items []SignalItem) []SignalItem {
	seen := make(map[string]bool)
	result := make([]SignalItem, 0, len(items))
	for _, item := range items {
		key := item.Title + "\x00" + item.Preview + "\x00" + item.Timestamp
		if !seen[key] {
			seen[key] = true
			result = append(result, item)
		}
	}
	return result
}
```

**Step 4: Run tests to verify they pass**

Run: `GOMAXPROCS=1 go test -p 1 ./internal/signal/ -v`
Expected: All PASS

**Step 5: Commit**

```bash
git add internal/signal/signal.go internal/signal/signal_test.go
git commit -m "Add Timestamp field to SignalItem"
```

---

### Task 7: Update extension scrapers for timestamps

**Files:**
- Modify: `extension/background.js`

**Step 1: Update Gmail scraper to extract timestamps**

In `extension/background.js`, update the `gmail` scraper inside the `scrape-activity` handler (around line 169-175):

```javascript
gmail: () => {
  const rows = document.querySelectorAll("tr.zE");
  return Array.from(rows).slice(0, 20).map(row => {
    const sender = row.querySelector(".yX.yW span")?.getAttribute("name") || row.querySelector(".yX.yW")?.textContent?.trim() || "";
    const subject = row.querySelector(".bog span")?.textContent?.trim() || row.querySelector(".y6 span")?.textContent?.trim() || "";
    const timestamp = row.querySelector("td.xW span")?.getAttribute("title") || row.querySelector("td.xW span")?.textContent?.trim() || "";
    return { title: sender, preview: subject, timestamp };
  });
},
```

**Step 2: Update Slack/Matrix scrapers to send empty timestamp**

Slack scraper — add `timestamp: ""` to each item:

```javascript
slack: () => {
  const unreads = document.querySelectorAll(".p-channel_sidebar__link--unread .p-channel_sidebar__name");
  if (unreads.length > 0) {
    return Array.from(unreads).slice(0, 20).map(el => ({
      title: el.textContent?.trim() || "",
      preview: "unread channel",
      timestamp: "",
    }));
  }
  const msgs = document.querySelectorAll("[data-qa='virtual-list-item'] .c-message_kit__text");
  return Array.from(msgs).slice(-20).map(el => ({
    title: "",
    preview: el.textContent?.trim() || "",
    timestamp: "",
  }));
},
```

Matrix scraper — add `timestamp: ""` to each item:

```javascript
matrix: () => {
  const rooms = document.querySelectorAll(".mx_RoomTile");
  const items = [];
  rooms.forEach(room => {
    const badge = room.querySelector(".mx_RoomTile_badge, .mx_NotificationBadge");
    if (badge && badge.textContent?.trim() !== "0") {
      const name = room.querySelector(".mx_RoomTile_title")?.textContent?.trim() || "";
      items.push({ title: name, preview: badge.textContent?.trim() + " unread", timestamp: "" });
    }
  });
  return items.length > 0 ? items : [];
},
```

**Step 3: Commit**

```bash
git add extension/background.js
git commit -m "Add timestamp extraction to extension scrapers"
```

---

### Task 8: CLI — signals list subcommand

**Files:**
- Modify: `main.go`

**Step 1: Add signal list formatting functions**

Add to `internal/storage/signals.go`:

```go
import (
	"encoding/json"
	"strings"
	"time"
)

// FormatSignalsMarkdown formats signals grouped by source as markdown.
func FormatSignalsMarkdown(signals []SignalRecord) string {
	if len(signals) == 0 {
		return "No signals found.\n"
	}

	grouped := make(map[string][]SignalRecord)
	var sourceOrder []string
	for _, s := range signals {
		if _, exists := grouped[s.Source]; !exists {
			sourceOrder = append(sourceOrder, s.Source)
		}
		grouped[s.Source] = append(grouped[s.Source], s)
	}

	var b strings.Builder
	for _, source := range sourceOrder {
		sigs := grouped[source]
		activeCount := 0
		for _, s := range sigs {
			if s.CompletedAt == nil {
				activeCount++
			}
		}
		fmt.Fprintf(&b, "## %s (%d active)\n\n", capitalize(source), activeCount)
		for _, s := range sigs {
			age := formatAge(s.CapturedAt)
			prefix := fmt.Sprintf("- [%d]", s.ID)
			if s.CompletedAt != nil {
				prefix += " ✓"
			}
			if s.Preview != "" {
				fmt.Fprintf(&b, "%s %s — %s (%s)\n", prefix, s.Title, s.Preview, age)
			} else {
				fmt.Fprintf(&b, "%s %s (%s)\n", prefix, s.Title, age)
			}
		}
		b.WriteString("\n")
	}

	return b.String()
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func formatAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// SignalJSONOutput is the structure for --json output.
type SignalJSONOutput struct {
	ID        int64   `json:"id"`
	Title     string  `json:"title"`
	Preview   string  `json:"preview"`
	SourceTS  string  `json:"source_ts,omitempty"`
	CapturedAt string `json:"captured_at"`
	Active    bool    `json:"active"`
}

// FormatSignalsJSON formats signals grouped by source as JSON.
func FormatSignalsJSON(signals []SignalRecord) (string, error) {
	grouped := make(map[string][]SignalJSONOutput)
	for _, s := range signals {
		grouped[s.Source] = append(grouped[s.Source], SignalJSONOutput{
			ID:         s.ID,
			Title:      s.Title,
			Preview:    s.Preview,
			SourceTS:   s.SourceTS,
			CapturedAt: s.CapturedAt.Format(time.RFC3339),
			Active:     s.CompletedAt == nil,
		})
	}
	data, err := json.MarshalIndent(grouped, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}
```

**Step 2: Add CLI handler in main.go**

Add `"signals"` case to the `switch os.Args[1]` block in `main()` (after the `"summarize"` case, around line 41):

```go
case "signals":
	runSignals(os.Args[2:])
	return
```

Add the handler function:

```go
func runSignals(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		runSignalsList(args)
		return
	}

	subcmd := args[0]
	subArgs := args[1:]

	switch subcmd {
	case "list":
		runSignalsList(subArgs)
	case "complete":
		runSignalsComplete(subArgs)
	case "reopen":
		runSignalsReopen(subArgs)
	default:
		fmt.Fprintf(os.Stderr, "Unknown signals command %q. Use list, complete, or reopen.\n", subcmd)
		os.Exit(1)
	}
}

func runSignalsList(args []string) {
	fs := flag.NewFlagSet("signals list", flag.ExitOnError)
	showAll := fs.Bool("all", false, "Include completed signals")
	jsonFlag := fs.Bool("json", false, "Output as JSON")
	source := fs.String("source", "", "Filter by source (gmail, slack, matrix)")
	fs.Parse(args)

	db, err := openDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	sigs, err := storage.ListSignals(db, *source, *showAll)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing signals: %v\n", err)
		os.Exit(1)
	}

	if *jsonFlag {
		out, err := storage.FormatSignalsJSON(sigs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error formatting JSON: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(out)
	} else {
		fmt.Print(storage.FormatSignalsMarkdown(sigs))
	}
}
```

**Step 3: Update help text**

In `printHelp()`, add the signals section after the snapshot section:

```go
  tabsordnung signals                                    List active signals
  tabsordnung signals list [--all] [--json] [--source X] List signals
  tabsordnung signals complete <id>                      Mark signal as completed
  tabsordnung signals reopen <id>                        Reopen a completed signal
```

**Step 4: Verify it compiles and runs**

Run: `GOMAXPROCS=1 go build -p 1 -o tabsordnung . && ./tabsordnung signals list`
Expected: "No signals found." (empty DB)

**Step 5: Commit**

```bash
git add main.go internal/storage/signals.go
git commit -m "Add signals list CLI subcommand with markdown and JSON output"
```

---

### Task 9: CLI — signals complete and reopen subcommands

**Files:**
- Modify: `main.go`

**Step 1: Add complete and reopen handlers**

Add to `main.go`:

```go
func runSignalsComplete(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: tabsordnung signals complete <id>")
		os.Exit(1)
	}

	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid signal ID: %s\n", args[0])
		os.Exit(1)
	}

	db, err := openDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := storage.CompleteSignal(db, id); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Signal %d marked as completed.\n", id)
}

func runSignalsReopen(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: tabsordnung signals reopen <id>")
		os.Exit(1)
	}

	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid signal ID: %s\n", args[0])
		os.Exit(1)
	}

	db, err := openDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := storage.ReopenSignal(db, id); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Signal %d reopened.\n", id)
}
```

**Step 2: Verify it compiles**

Run: `GOMAXPROCS=1 go build -p 1 -o tabsordnung .`
Expected: Compiles successfully.

**Step 3: Commit**

```bash
git add main.go
git commit -m "Add signals complete and reopen CLI subcommands"
```

---

### Task 10: TUI — pass DB and replace signal write flow

This task changes the TUI to use `ReconcileSignals` instead of file-based `WriteSignal`.

**Files:**
- Modify: `internal/tui/app.go`
- Modify: `main.go`

**Step 1: Add `*sql.DB` to Model, remove `signalDir`**

In `internal/tui/app.go`:

1. Add import: `"database/sql"` and `"github.com/lotas/tabsordnung/internal/storage"`
2. In `Model` struct, replace `signalDir string` with `db *sql.DB`
3. Update `NewModel` signature: replace `signalDir string` with `db *sql.DB`
4. In `NewModel` body: replace `signalDir: signalDir,` with `db: db,`

**Step 2: Replace `runWriteSignal` with `runReconcileSignals`**

Replace the `runWriteSignal` function:

```go
func runReconcileSignals(db *sql.DB, source string, items []signal.SignalItem, capturedAt time.Time) tea.Cmd {
	return func() tea.Msg {
		records := make([]storage.SignalRecord, len(items))
		for i, item := range items {
			records[i] = storage.SignalRecord{
				Title:    item.Title,
				Preview:  item.Preview,
				SourceTS: item.Timestamp,
			}
		}
		err := storage.ReconcileSignals(db, source, records, capturedAt)
		if err != nil {
			return signalCompleteMsg{source: source, err: err}
		}
		return signalCompleteMsg{source: source}
	}
}
```

**Step 3: Update the wsCmdResponseMsg handler**

In the `wsCmdResponseMsg` case (around line 804-812), replace:

```go
return m, tea.Batch(
	listenWebSocket(m.server),
	runWriteSignal(m.signalDir, sig),
)
```

with:

```go
return m, tea.Batch(
	listenWebSocket(m.server),
	runReconcileSignals(m.db, source, items, time.Now()),
)
```

Remove the `sig := signal.Signal{...}` construction above it since we no longer need it.

**Step 4: Update main.go**

In `main.go`, open DB before creating the model:

Replace the signalDir resolution block (lines 108-112) and model creation (line 114) with:

```go
db, err := openDB()
if err != nil {
	fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
	os.Exit(1)
}
defer db.Close()

model := tui.NewModel(profiles, *staleDays, *liveMode, srv, summaryDir, resolvedModel, ollamaHost, db)
```

**Step 5: Verify it compiles**

Run: `GOMAXPROCS=1 go build -p 1 -o tabsordnung .`
Expected: Compiles. (There will be unused import warnings for signal package functions — we'll clean those up in the cleanup task.)

**Step 6: Commit**

```bash
git add internal/tui/app.go main.go
git commit -m "TUI: replace file-based signal writes with ReconcileSignals"
```

---

### Task 11: TUI — replace signal reads with DB queries in detail pane

This task changes the detail pane to read signals from the database instead of markdown files.

**Files:**
- Modify: `internal/tui/app.go` (View function)
- Modify: `internal/tui/detail.go`

**Step 1: Update ViewTabWithSignal to accept signal records**

In `internal/tui/detail.go`, replace `ViewTabWithSignal` signature and body:

```go
// ViewTabWithSignal renders tab info with signal list from database.
func (m *DetailModel) ViewTabWithSignal(tab *types.Tab, signals []storage.SignalRecord, signalCursor int, capturing bool, signalErr string) string {
	base := m.ViewTab(tab)

	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	activeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	completedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	cursorStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("62"))

	if capturing {
		base += "\n" + activeStyle.Render("Capturing signal...")
	}

	if len(signals) > 0 {
		// Count active/completed.
		var activeCount, completedCount int
		for _, s := range signals {
			if s.CompletedAt == nil {
				activeCount++
			} else {
				completedCount++
			}
		}

		base += "\n" + labelStyle.Render(fmt.Sprintf("Signals — %d active, %d completed", activeCount, completedCount)) + "\n\n"

		for i, s := range signals {
			prefix := "  "
			if i == signalCursor {
				prefix = "> "
			}

			age := formatSignalAge(s.CapturedAt)
			line := fmt.Sprintf("[%d] %s", s.ID, s.Title)
			if s.Preview != "" {
				line += " — " + s.Preview
			}
			line = prefix + line + "  " + age

			if i == signalCursor {
				base += cursorStyle.Render(line) + "\n"
			} else if s.CompletedAt != nil {
				base += completedStyle.Render(prefix + "✓ " + line[2:]) + "\n"
			} else {
				base += line + "\n"
			}
		}

		base += "\n" + dimStyle.Render("  c capture · x complete · u reopen")
	} else if signalErr != "" {
		base += "\n" + errStyle.Render("Signal failed: "+signalErr)
		base += "\n" + dimStyle.Render("  Press 'c' to retry")
	} else if !capturing {
		base += "\n" + dimStyle.Render("  Press 'c' to capture signal")
	}

	return base
}

func formatSignalAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
```

Add imports to detail.go: `"fmt"`, `"time"`, `"github.com/lotas/tabsordnung/internal/storage"`.

**Step 2: Add signal state to Model**

In `internal/tui/app.go`, add to the `Model` struct:

```go
// Signals
signalQueue   []*SignalJob
signalActive  *SignalJob
signalErrors  map[string]string
signals       []storage.SignalRecord  // signals for currently viewed source
signalCursor  int                      // cursor position in signal list
signalSource  string                   // source of currently loaded signals
```

Remove the old `signalDir` field (if not already removed in Task 10).

**Step 3: Update View to load signals from DB and pass to detail**

In `app.go` View function, replace the signal-tab block (around lines 1039-1066):

```go
if node.Tab != nil {
	source := signal.DetectSource(node.Tab.URL)
	if source != "" {
		// Load signals from DB if source changed.
		if source != m.signalSource {
			m.signals, _ = storage.ListSignals(m.db, source, true)
			m.signalSource = source
			m.signalCursor = 0
		}
		isCapturing := m.signalActive != nil && m.signalActive.Source == source
		if !isCapturing {
			for _, j := range m.signalQueue {
				if j.Source == source {
					isCapturing = true
					break
				}
			}
		}
		sigErr := m.signalErrors[source]
		detailContent = m.detail.ViewTabWithSignal(node.Tab, m.signals, m.signalCursor, isCapturing, sigErr)
	} else {
		// ... existing summary code unchanged ...
	}
}
```

**Step 4: Reload signals after reconcile completes**

In the `signalCompleteMsg` handler, reload signals:

```go
case signalCompleteMsg:
	if msg.err != nil {
		m.signalErrors[msg.source] = msg.err.Error()
	} else {
		delete(m.signalErrors, msg.source)
	}
	// Reload signals for current source.
	if m.signalSource != "" {
		m.signals, _ = storage.ListSignals(m.db, m.signalSource, true)
	}
	return m, m.processNextSignal()
```

**Step 5: Verify it compiles**

Run: `GOMAXPROCS=1 go build -p 1 -o tabsordnung .`
Expected: Compiles.

**Step 6: Commit**

```bash
git add internal/tui/app.go internal/tui/detail.go
git commit -m "TUI: display signals from DB with cursor in detail pane"
```

---

### Task 12: TUI — x/u keybindings for complete/reopen signals

**Files:**
- Modify: `internal/tui/app.go`

**Step 1: Add signal navigation and actions in detail focus mode**

In the detail focus mode handler (`if m.focusDetail { ... }`, around line 408), add signal-specific keybindings:

```go
if m.focusDetail {
	// Signal navigation when viewing a signal-source tab.
	if m.signalSource != "" && len(m.signals) > 0 {
		switch msg.String() {
		case "j", "down":
			if m.signalCursor < len(m.signals)-1 {
				m.signalCursor++
			}
			return m, nil
		case "k", "up":
			if m.signalCursor > 0 {
				m.signalCursor--
			}
			return m, nil
		case "x":
			sig := m.signals[m.signalCursor]
			if sig.CompletedAt == nil {
				return m, completeSignalCmd(m.db, sig.ID, m.signalSource)
			}
			return m, nil
		case "u":
			sig := m.signals[m.signalCursor]
			if sig.CompletedAt != nil {
				return m, reopenSignalCmd(m.db, sig.ID, m.signalSource)
			}
			return m, nil
		case "tab":
			m.focusDetail = false
			m.detail.Scroll = 0
			return m, nil
		case "q", "ctrl+c":
			return m, tea.Quit
		case "c":
			// Fall through to main 'c' handler below
		default:
			return m, nil
		}
	}
	// ... existing detail focus handlers for non-signal tabs ...
}
```

**Step 2: Add message types and command helpers**

```go
type signalActionMsg struct {
	source string
	err    error
}

func completeSignalCmd(db *sql.DB, id int64, source string) tea.Cmd {
	return func() tea.Msg {
		err := storage.CompleteSignal(db, id)
		return signalActionMsg{source: source, err: err}
	}
}

func reopenSignalCmd(db *sql.DB, id int64, source string) tea.Cmd {
	return func() tea.Msg {
		err := storage.ReopenSignal(db, id)
		return signalActionMsg{source: source, err: err}
	}
}
```

**Step 3: Handle signalActionMsg in Update**

Add case in the Update switch:

```go
case signalActionMsg:
	if msg.err != nil {
		m.signalErrors[msg.source] = msg.err.Error()
	}
	// Reload signals.
	if m.signalSource != "" {
		m.signals, _ = storage.ListSignals(m.db, m.signalSource, true)
		if m.signalCursor >= len(m.signals) {
			m.signalCursor = len(m.signals) - 1
		}
		if m.signalCursor < 0 {
			m.signalCursor = 0
		}
	}
	return m, nil
```

**Step 4: Verify it compiles**

Run: `GOMAXPROCS=1 go build -p 1 -o tabsordnung .`
Expected: Compiles.

**Step 5: Commit**

```bash
git add internal/tui/app.go
git commit -m "TUI: add x/u keybindings for complete/reopen signals in detail pane"
```

---

### Task 13: Remove old file-based signal I/O and clean up

**Files:**
- Modify: `internal/signal/signal.go` — remove `WriteSignal`, `ReadSignals`, `AppendSignalLog`, `RenderSignalsMarkdown`, `parseSignalMarkdown`, `capitalize`, `ItemsEqual`, `Signal` struct
- Modify: `internal/signal/signal_test.go` — remove file-based tests
- Modify: `internal/tui/app.go` — remove any remaining references to old signal functions
- Modify: `main.go` — remove `signalDir` env var resolution

**Step 1: Clean signal.go**

Keep only:
- `SignalItem` struct (with Timestamp field)
- `DetectSource` function
- `ParseItemsJSON` function
- `deduplicateItems` function

Remove everything else. Remove unused imports (`fmt`, `os`, `path/filepath`, `sort`, `time`).

**Step 2: Clean signal_test.go**

Keep only:
- `TestDetectSource`
- `TestParseItemsJSON`
- `TestParseItemsJSONWithTimestamp`

Remove: `TestWriteAndReadSignal`, `TestReadSignalsReverseChronological`, `TestAppendSignalLog`, `TestWriteSignalDedup`.

**Step 3: Clean main.go**

Remove the `signalDir` env var block (lines 108-112). Remove unused `signal` import if present.

**Step 4: Verify everything compiles and tests pass**

Run: `GOMAXPROCS=1 go build -p 1 -o tabsordnung .`
Run: `GOMAXPROCS=1 go test -p 1 ./... -v`
Expected: All compile and pass.

**Step 5: Commit**

```bash
git add internal/signal/signal.go internal/signal/signal_test.go internal/tui/app.go main.go
git commit -m "Remove file-based signal storage, keep only DetectSource and ParseItemsJSON"
```

---

### Task 14: Final verification

**Step 1: Run all tests**

Run: `GOMAXPROCS=1 go test -p 1 ./... -v`
Expected: All PASS.

**Step 2: Build and smoke test CLI**

```bash
GOMAXPROCS=1 go build -p 1 -o tabsordnung .
./tabsordnung signals list
./tabsordnung signals list --json
./tabsordnung help
```

**Step 3: Verify TUI starts**

```bash
./tabsordnung --profile <your-profile>
```

Navigate to a Gmail/Slack tab, verify detail pane shows "Press 'c' to capture signal" instead of old markdown content. If in live mode, press 'c' to test a capture, then Tab to focus detail, use j/k to navigate, x to complete, u to reopen.
