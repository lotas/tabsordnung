# Signals v2: SQLite Storage & CLI

Replaces the file-based (markdown) signal system with SQLite-backed persistent storage, per-item state tracking, deduplication, and CLI subcommands.

## Data Model

SQLite table in existing `tabsordnung.db`:

```sql
CREATE TABLE signals (
  id              INTEGER PRIMARY KEY,
  source          TEXT NOT NULL,         -- "gmail", "slack", "matrix"
  title           TEXT NOT NULL,         -- sender name, channel name, room name
  preview         TEXT,                  -- subject line, message text
  source_ts       TEXT NOT NULL DEFAULT '',  -- original item timestamp (email sent time); empty string for sources without timestamps (slack, matrix)
  captured_at     DATETIME NOT NULL,     -- when first scraped
  completed_at    DATETIME,             -- NULL = active, non-NULL = completed
  auto_completed  BOOLEAN DEFAULT 0,    -- true if resolved by re-scrape (item disappeared)
  pinned          BOOLEAN DEFAULT 0,    -- true if manually reopened; prevents auto-complete
  UNIQUE(source, title, source_ts)
);
```

- **Active**: `completed_at IS NULL`
- **Completed**: `completed_at IS NOT NULL`
- **Dedup key**: `(source, title, source_ts)` — duplicates across scrapes are silently ignored via `INSERT OR IGNORE`
- **Per-source timestamp strategy**: Gmail extracts email timestamps; Slack/Matrix use empty string `source_ts` (dedup on source + title only)

## Scrape Reconciliation

When a new scrape arrives for a source, run in a single transaction:

1. **Insert new items**: `INSERT OR IGNORE` each item. Duplicates are skipped.
2. **Auto-complete missing items**: Active signals for that source NOT in the current scrape get `completed_at = NOW(), auto_completed = 1` — but only if `pinned = 0`.
3. **Reactivate returning items**: Previously auto-completed signals that reappear get `completed_at = NULL, auto_completed = 0`.

Manually completed signals (`auto_completed = 0, completed_at IS NOT NULL`) stay completed even if the item reappears in a scrape.

### Pin lifecycle

- `reopen` (manual) → `pinned = 1`, `completed_at = NULL`
- `complete` (manual) → `completed_at = NOW()`, `pinned = 0`
- Auto-complete by scrape → only affects `pinned = 0` signals

## Extension Scraper Changes

Add `timestamp` field to scraped items (alongside existing `title` and `preview`).

- **Gmail**: Extract visible timestamp from email row (`<td>` with "2:30 PM", "Feb 14", etc.)
- **Slack**: No per-item timestamp available from sidebar; `timestamp` = empty string
- **Matrix**: No per-item timestamp from badge; `timestamp` = empty string

Updated response format:
```json
{"title": "Alice", "preview": "Production DB alert", "timestamp": "2:30 PM"}
```

## CLI Subcommands

```
./tabsordnung signals list              # active signals, markdown grouped by source
./tabsordnung signals list --all        # include completed signals
./tabsordnung signals list --json       # machine-readable JSON grouped by source
./tabsordnung signals complete <id>     # mark signal as completed
./tabsordnung signals reopen <id>       # undo a manual completion
```

### Markdown output (default)

```
## Gmail (3 active)

- [1] Alice — Production DB latency spike (2h ago)
- [2] Bob — Weekly sync notes (5h ago)
- [3] CI Bot — Build failed on main (1d ago)

## Slack (1 active)

- [4] #incidents — (3h ago)
```

### JSON output (`--json`)

```json
{
  "gmail": [
    {"id": 1, "title": "Alice", "preview": "Production DB latency spike", "source_ts": "2:30 PM", "captured_at": "...", "active": true}
  ],
  "slack": [...]
}
```

## TUI Integration

Replace markdown file reader with database queries in the detail pane.

### Detail pane for signal-capable tabs

```
── Gmail Signals ──────────────────────

  Active (3)
  > [1] Alice — Production DB latency spike    2h ago
    [2] Bob — Weekly sync notes                5h ago
    [3] CI Bot — Build failed on main          1d ago

  Completed (2)
    [4] Dave — Deploy checklist                1d ago
    [5] Alice — Monday standup notes           3d ago

  Press 'c' to capture · 'x' to complete · 'u' to reopen
```

- When detail pane is focused (Tab toggle), arrow keys navigate signal items
- Active signals listed first, then completed
- `c` — trigger new scrape (unchanged)
- `x` — complete highlighted active signal
- `u` — reopen highlighted completed signal

### Flow change

- `signal.ReadSignals()` (markdown) → `db.SignalStore.ListSignals(source)`
- `signal.WriteSignal()` (markdown) → `db.SignalStore.Reconcile(source, items)`

## Package Restructuring

```
internal/db/           -- NEW: shared data layer
  db.go                -- Open(), migrations, shared *sql.DB
  signals.go           -- SignalStore: CRUD, Reconcile()
  snapshots.go         -- SnapshotStore: moved from internal/snapshot/storage.go

internal/signal/       -- domain logic only
  signal.go            -- DetectSource(), SignalItem type (remove file I/O)

internal/snapshot/     -- domain logic only
  snapshot.go          -- types (SnapshotSummary, SnapshotTab, etc.)
  diff.go              -- diff logic (remove storage.go)
```

`internal/db` owns the connection, migrations, and all SQL queries. Domain packages keep types and logic.

## Migration & Cleanup

- Add migration to create `signals` table (using existing migration system)
- Move snapshot SQL from `internal/snapshot/storage.go` to `internal/db/snapshots.go`
- Remove file-based signal functions: `WriteSignal`, `ReadSignals`, `AppendSignalLog`
- Keep `DetectSource()` in `internal/signal/`
- Old `~/.local/share/tabsordnung/signals/` directory becomes obsolete (no migration needed — ephemeral scrape data)
