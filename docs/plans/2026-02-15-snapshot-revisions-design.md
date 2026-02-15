# Snapshot Revisions Design

## Problem

Named snapshots require manual effort — picking a name each time. The goal is to make snapshots automatic and cron-friendly: run periodically, only save when tabs change, reference by sequential revision number.

## CLI Interface

```
tabsordnung snapshot [--profile X] [--label "text"]   Auto-create if changed from latest
tabsordnung snapshot list                              List all snapshots with rev numbers
tabsordnung snapshot diff [--profile X]                Current tabs vs latest snapshot
tabsordnung snapshot diff 3 [--profile X]              Current tabs vs rev #3
tabsordnung snapshot diff 3 5 [--profile X]            Rev #3 vs rev #5
tabsordnung snapshot delete 3 [--yes]                  Delete rev #3
tabsordnung snapshot restore 3 [--port N]              Restore rev #3 via live mode
```

Cron usage:
```
*/30 * * * * tabsordnung snapshot --profile default
```

## Schema

Breaking change — drop and recreate the DB.

```sql
CREATE TABLE IF NOT EXISTS snapshots (
    id          INTEGER PRIMARY KEY,
    rev         INTEGER NOT NULL,
    name        TEXT,
    profile     TEXT NOT NULL,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    tab_count   INTEGER NOT NULL,
    UNIQUE(profile, rev)
);
```

`snapshot_groups` and `snapshot_tabs` tables unchanged.

Rev assignment: `SELECT COALESCE(MAX(rev), 0) + 1 FROM snapshots WHERE profile = ?` inside the insert transaction.

## Storage Layer

**Modified:**
- `CreateSnapshot(db, profile, groups, tabs, label) (int, error)` — label is optional (empty string = no label). Returns new rev number.
- `GetSnapshot(db, profile, rev) (*SnapshotFull, error)` — load by profile + rev
- `DeleteSnapshot(db, profile, rev) error` — delete by profile + rev
- `ListSnapshots(db) ([]SnapshotSummary, error)` — returns results with rev numbers and optional labels

**New:**
- `GetLatestSnapshot(db, profile) (*SnapshotFull, error)` — most recent snapshot for profile, nil if none

## Snapshot Package

**`Create(db, session, label) (rev int, created bool, err error)`:**
1. Load latest snapshot for session's profile
2. Compare URL sets (current vs latest)
3. If identical: return `(latestRev, false, nil)`
4. If different (or no previous): save new snapshot, return `(newRev, true, nil)`

**`Diff` signature:** `Diff(db, profile, revs ...int, current *types.SessionData)` is awkward with variadic + trailing arg. Instead:

- `DiffAgainstCurrent(db, profile, rev int, current *types.SessionData) (*DiffResult, error)` — rev 0 means latest
- `DiffRevisions(db, profile, rev1, rev2 int) (*DiffResult, error)` — compare two stored snapshots

## CLI Output

**Snapshot created (with diff):**
```
Snapshot #4 created: 142 tabs in 8 groups

+ Added (3):
  + https://example.com/new-page [Research]
  + https://github.com/foo/bar/pull/42
  + https://docs.go.dev/ref/spec

- Removed (1):
  - https://old-site.com/page [Archive]
```

**No changes:**
```
No changes since snapshot #3
```

**First snapshot (no previous):**
```
Snapshot #1 created: 142 tabs in 8 groups
```

**Snapshot list:**
```
REV  TABS  PROFILE   LABEL                CREATED
  5   142  default                        2026-02-15 14:30
  4   138  default   before cleanup       2026-02-15 10:00
  3   135  default                        2026-02-14 18:00
```
