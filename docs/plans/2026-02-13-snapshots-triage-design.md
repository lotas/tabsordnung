# Snapshots & GitHub Triage — Design Document

## Overview

Two new feature clusters for tabsordnung, exposed as CLI subcommands:

1. **Snapshots** — save/restore/diff tab sessions. Lets you confidently close tabs knowing you can resurrect them later.
2. **GitHub Triage** — classify GitHub tabs into predefined groups (Needs Attention, Open PRs, Open Issues, Closed/Merged) and move them via live mode.

Both features use the CLI subcommand model — no daemon, no built-in scheduler. Users wire up cron/launchd if they want automation.

## Feature 1: Snapshots

### CLI Subcommands

```
tabsordnung snapshot create <name>   # save current tabs
tabsordnung snapshot list            # list saved snapshots
tabsordnung snapshot restore <name>  # reopen tabs via live mode
tabsordnung snapshot diff <name>     # compare snapshot vs current state
tabsordnung snapshot delete <name>   # remove a snapshot
```

### Storage

SQLite database at `~/.local/share/tabsordnung/tabsordnung.db`. Created automatically on first use. Single file holds all snapshots.

Go dependency: `modernc.org/sqlite` (pure Go, no CGO needed).

### Schema

```sql
CREATE TABLE snapshots (
    id          INTEGER PRIMARY KEY,
    name        TEXT UNIQUE NOT NULL,
    profile     TEXT NOT NULL,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    tab_count   INTEGER NOT NULL
);

CREATE TABLE snapshot_groups (
    id          INTEGER PRIMARY KEY,
    snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    firefox_id  INTEGER NOT NULL,
    name        TEXT NOT NULL,
    color       TEXT
);

CREATE TABLE snapshot_tabs (
    id          INTEGER PRIMARY KEY,
    snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    group_id    INTEGER REFERENCES snapshot_groups(id),
    url         TEXT NOT NULL,
    title       TEXT NOT NULL,
    pinned      BOOLEAN DEFAULT FALSE
);
```

### Operations

**`snapshot create <name>`**
1. Read session from `recovery.jsonlz4` / `previous.jsonlz4` (reuses `firefox.ReadSession()`)
2. Insert into `snapshots` table, batch-insert groups and tabs
3. Print summary: `Snapshot "incident-auth" created: 42 tabs in 5 groups`
4. Error if name already exists (no silent overwrite — delete first to redo)

**`snapshot list`**
```
NAME                              TABS  GROUPS  CREATED
incident-auth-2026-02-13            42       5  2026-02-13 17:30
friday-cleanup                      89      12  2026-02-07 17:00
```

**`snapshot restore <name>`**
1. Requires live mode — connects to WebSocket bridge (extension must be running)
2. Reads tabs + groups from DB
3. Creates tab groups in Firefox first, then opens tabs into their groups
4. Respects pinned state
5. Prints progress: `Restored 42 tabs in 5 groups`

**`snapshot diff <name>`**

Compares snapshot against the current session state (by URL):
```
+ 12 tabs added since "incident-auth"
- 8 tabs closed since "incident-auth"
~ 3 tabs moved to different groups

Added:
  https://github.com/org/repo/pull/456  (Ungrouped)
  ...

Removed:
  https://grafana.example.com/explore?...  (Monitoring)
  ...
```
Optional `--name <other>` to diff two snapshots against each other.

**`snapshot delete <name>`** — cascade delete, confirm with `--yes` or prompt interactively.

## Feature 2: GitHub Triage

### CLI

```
tabsordnung triage          # dry-run (show proposed moves)
tabsordnung triage --apply  # apply without prompt (for cron)
```

Dry-run is the default. Interactive confirm before applying. Execution goes through live mode WebSocket.

### Categories

Tabs are classified in priority order (first match wins):

| Group | Criteria |
|-------|----------|
| **Needs Attention** | Review requested from you, OR assigned to you, OR `updatedAt` > tab's `lastAccessed` |
| **Open PRs** | Open pull requests not matching "Needs Attention" |
| **Open Issues** | Open issues not matching "Needs Attention" |
| **Closed / Merged** | Closed issues, merged or closed PRs |

Only GitHub tabs are touched. Non-GitHub tabs are left unchanged.

### Enhanced GraphQL Query

The existing GitHub analyzer (`internal/analyzer/github.go`) fetches `state` (open/closed/merged). Extend the query to also return:
- `reviewRequests` — is the authenticated user a requested reviewer?
- `assignees` — is the authenticated user an assignee?
- `updatedAt` — compare against tab's `lastAccessed` timestamp

The authenticated user is determined from `gh api user` (or cached after first call).

### Dry-Run Output

```
Proposed moves (23 GitHub tabs):

  → Needs Attention (3)
    github.com/org/repo/pull/789    "Fix worker timeout" (review requested)
    github.com/org/repo/issues/456  "OOM in staging" (assigned, new activity)
    github.com/org/repo/pull/321    "Update deps" (new comments)

  → Open PRs (8)
    ...

  → Closed / Merged (7)
    ...

  → Open Issues (5)
    ...

  15 non-GitHub tabs unchanged.

Apply? [y/N]
```

## Architecture

### New Packages

| Package | Responsibility |
|---------|---------------|
| `internal/storage/` | SQLite DB init, migrations, snapshot CRUD |
| `internal/snapshot/` | Snapshot create/restore/diff orchestration (uses storage + firefox + server) |
| `internal/triage/` | GitHub classification rules, builds proposed moves, applies via live mode |

### Data Flow

```
snapshot create:  firefox.ReadSession() → storage.CreateSnapshot()
snapshot restore: storage.GetSnapshot() → server.OpenTabs()
snapshot diff:    storage.GetSnapshot() + firefox.ReadSession() → diff logic
triage:           firefox.ReadSession() → analyzer.CheckGitHub() (enhanced) → triage.Classify() → dry-run output → server.MoveTabs()
```

### Changes to Existing Code

- **`internal/analyzer/github.go`** — extend GraphQL query to return `assignees`, `reviewRequests`, `updatedAt`; add authenticated user resolution
- **`internal/server/`** — add `MoveTabs()` and `CreateGroup()` WebSocket message types
- **`extension/background.js`** — handle `move-tab` and `create-group` message types
- **`main.go`** — subcommand routing for `snapshot` and `triage`
- **`README.md`** — document new subcommands and features

### Unchanged

- `internal/tui/` — no TUI changes
- `internal/types/` — no type changes (triage uses existing `Tab` fields + new GitHub API data stored transiently)
- `internal/firefox/` — read path unchanged
- `internal/export/` — unchanged

## Implementation Notes

- No git operations during implementation
- All `go` commands must use `GOMAXPROCS=1` and `-p 1`
- `modernc.org/sqlite` for pure-Go SQLite (no CGO)
