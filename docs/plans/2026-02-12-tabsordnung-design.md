# Tabsordnung — Design Document

## Overview

Tabsordnung is a Go CLI/TUI tool that reads Firefox session data, analyzes open tabs, and presents insights in an interactive terminal interface built with Bubble Tea.

The tool helps users who keep many tabs open by identifying stale tabs, dead links, duplicates, and providing group-level summaries — starting as analytics-only, with tab actions planned for later.

## Core Architecture

### Components

- **Session Reader** — Locates Firefox profiles (`profiles.ini`), decompresses `recovery.jsonlz4` (Mozilla's custom lz4 format with a `mozLz40\0` header), and parses the JSON into structured Go types (windows, tab groups, tabs with URL/title/last access time).
- **Analyzers** — A set of independent analyzers that each produce findings:
  - *Stale tabs* — Tabs not visited within a configurable threshold (default 7 days)
  - *Dead links* — Concurrent HTTP HEAD requests to detect 404s/unreachable pages
  - *Duplicates* — Tabs with identical or near-identical URLs (normalized)
  - *Group summary* — Tab count per group, expanded/collapsed state
- **TUI** — Built with Bubble Tea. A navigable tree: groups → tabs, with inline status markers. A detail pane shows info for the selected tab.

### Data Flow

Profile selection → read session file → parse → run analyzers concurrently → render TUI

## Firefox Session Parsing

### Locating Profiles

- Read `~/.mozilla/firefox/profiles.ini` (Linux) or `~/Library/Application Support/Firefox/profiles.ini` (macOS)
- Parse the INI to get all profiles with their paths and names
- On startup, if multiple profiles exist, show the profile picker. If only one, use it directly.

### Reading the Session File

- Path: `<profile_dir>/sessionstore-backups/recovery.jsonlz4`
- Format: 8-byte magic header (`mozLz40\0`) followed by 4-byte little-endian uncompressed size, then lz4-compressed JSON
- Decompress and parse into Go structs

### Extracted Data

- `windows[].tabs[]` — each tab's entries (URL, title), `lastAccessed` timestamp
- `windows[].groups` — tab group info (id, name, collapsed state, color)
- Each tab's `group` field linking it to a group
- Ungrouped tabs go into a virtual "Ungrouped" group

### Edge Cases

- Firefox writes the session file periodically (~every 15 seconds). Retry once on read failure if file is locked.

## Analyzers

Each analyzer is independent, runs concurrently, and produces findings attached to specific tabs.

### Stale Tabs

- Compare `lastAccessed` timestamp against current time
- Default threshold: 7 days (configurable via `--stale-days` flag)
- Report age in human-readable form ("12 days ago")

### Dead Links

- Concurrent HTTP HEAD requests with concurrency limit (10 in-flight)
- Timeout per request: 5 seconds
- Mark as dead on: 404, 410 (Gone), connection refused, DNS failure, timeout
- Skip internal URLs (`about:`, `moz-extension:`, `file:`)
- Results stream in progressively — TUI updates as checks complete

### Duplicates

- Normalize URLs: strip fragments, sort query params, normalize trailing slashes
- Group tabs by normalized URL
- Flag groups with 2+ tabs

### Group Summary

- Count tabs per group, note collapsed/expanded state
- Compute aggregate stats: stale/dead/duplicate counts per group

## TUI Layout

```
┌─ Tabsordnung ──────────────────────────────────┐
│ Profile: default-release                        │
│ 147 tabs · 12 groups · 3 dead · 8 stale · 2 dup│
├─────────────────────────────────┬───────────────┤
│ ▼ Work (23 tabs)                │ Title: PR #42 │
│   ├─ github.com/org/repo/...   │ URL: https://…│
│   ├─ 🔴 github.com/org/... 404 │ Last visited:  │
│   └─ ⏳ jira.com/browse/...    │   12 days ago  │
│ ▶ Monitoring (8 tabs)          │ Status: stale  │
│ ▶ Chat (5 tabs)                │               │
│ ▼ Research (31 tabs)           │               │
│   ├─ stackoverflow.com/...     │               │
│   ├─ 🔁 stackoverflow.com/... │               │
│   └─ ...                       │               │
├─────────────────────────────────┴───────────────┤
│ ↑↓ navigate · enter expand/collapse · q quit    │
│ f filter · s sort · r refresh · p profile       │
└─────────────────────────────────────────────────┘
```

### Left Pane
Collapsible tree of groups and tabs. Inline markers: 🔴 dead, ⏳ stale, 🔁 duplicate. Groups show tab count.

### Right Pane
Detail for the selected tab — full title, full URL, last visited timestamp, any issues detected.

### Top Bar
Profile name and summary stats at a glance.

### Key Bindings

| Key | Action |
|-----|--------|
| `↑`/`↓` or `j`/`k` | Navigate |
| `enter` | Expand/collapse group |
| `f` | Filter by status (stale, dead, duplicate, all) |
| `s` | Cycle sort (by group, by age, by status) |
| `r` | Re-read session file and re-analyze |
| `p` | Open profile picker |
| `q` | Quit |

### Profile Picker
An overlay list of detected profiles. Select one and the view reloads with that profile's data.

## Project Structure

```
tabsordnung/
├── main.go
├── go.mod
├── internal/
│   ├── firefox/
│   │   ├── profiles.go
│   │   └── session.go
│   ├── analyzer/
│   │   ├── stale.go
│   │   ├── deadlinks.go
│   │   ├── duplicates.go
│   │   └── summary.go
│   └── tui/
│       ├── app.go
│       ├── tree.go
│       ├── detail.go
│       └── profile_picker.go
├── pkg/
│   └── types/
│       └── types.go
```

## Dependencies

- `github.com/charmbracelet/bubbletea` — TUI framework
- `github.com/charmbracelet/lipgloss` — Styling
- `github.com/charmbracelet/bubbles` — Reusable components (list, viewport)
- `github.com/pierrec/lz4/v4` — LZ4 decompression
- Standard library for HTTP, INI parsing, JSON

## CLI Flags

- `--profile <name>` — Skip profile picker, use this profile directly
- `--stale-days <n>` — Stale threshold (default 7)

## Future Work

- GitHub API integration for PR/issue status checking
- WebExtension companion for taking actions (close, move, regroup tabs)
- Browser bookmark cross-referencing
