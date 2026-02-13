# Tabsordnung

Terminal UI for analyzing Firefox tabs. Reads session data directly from Firefox profile files and surfaces stale tabs, duplicates, and dead links.

## How it works

Firefox stores open tabs in `recovery.jsonlz4` (active session) or `previous.jsonlz4` (last closed session) inside each profile's `sessionstore-backups/` directory. Tabsordnung decompresses these mozlz4 files, parses the session JSON, and runs analysis:

- **Stale tabs** -- not accessed within a configurable number of days
- **Duplicate tabs** -- multiple tabs with the same URL
- **Dead links** -- URLs that return HTTP errors (checked async in the background)

Tabs are displayed in a collapsible tree grouped by Firefox tab groups.

## Install

```
go install github.com/nickel-chromium/tabsordnung@latest
```

## Usage

```
tabsordnung [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--profile` | | Firefox profile name (skips profile picker) |
| `--stale-days` | 7 | Days before a tab is considered stale |

## Keys

| Key | Action |
|-----|--------|
| `j`/`k` | Navigate up/down |
| `h` | Collapse group or jump to parent group |
| `l` | Expand group or enter first tab |
| `Enter` | Toggle expand/collapse |
| `f` | Cycle filter: all / stale / dead / duplicate |
| `r` | Reload session data |
| `p` | Switch Firefox profile |
| `q` | Quit |

## Supported platforms

Linux and macOS. Requires Firefox profile data on disk.
