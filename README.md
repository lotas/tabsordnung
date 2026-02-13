# Tabsordnung

Terminal UI for analyzing Firefox tabs. Reads session data directly from Firefox profile files and surfaces stale tabs, duplicates, dead links, and GitHub issue/PR status.

## How it works

Firefox stores open tabs in `recovery.jsonlz4` (active session) or `previous.jsonlz4` (last closed session) inside each profile's `sessionstore-backups/` directory. Tabsordnung decompresses these mozlz4 files, parses the session JSON, and runs analysis:

- **Stale tabs** -- not accessed within a configurable number of days
- **Duplicate tabs** -- multiple tabs with the same URL
- **Dead links** -- URLs that return HTTP errors (checked async in the background)
- **GitHub status** -- checks if GitHub issue/PR tabs are still open or closed/merged

Tabs are displayed in a collapsible tree grouped by Firefox tab groups.

## Install

```
go install github.com/nickel-chromium/tabsordnung@latest
```

## Usage

```
tabsordnung                              # TUI (default)
tabsordnung export                       # Export tabs to stdout or file
tabsordnung profiles                     # List Firefox profiles
tabsordnung snapshot <command>           # Manage tab snapshots
tabsordnung triage                       # Classify GitHub tabs into groups
tabsordnung help                         # Show help
```

### TUI mode (default)

```
tabsordnung [--profile X] [--stale-days N] [--live] [--port N]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--profile` | | Firefox profile name (skips profile picker) |
| `--stale-days` | 7 | Days before a tab is considered stale |
| `--live` | false | Start in live mode (connect to extension) |
| `--port` | 19191 | WebSocket port for live mode |

### Export

```
tabsordnung export [--profile X] [--format md|json] [--out FILE] [--live] [--port N]
```

Exports tabs to stdout or a file. Use `--live` to export from the Firefox extension instead of session files.

### Profiles

```
tabsordnung profiles
```

Lists discovered Firefox profiles.

### Snapshots

Save and restore tab sessions. Snapshots are stored in `~/.local/share/tabsordnung/tabsordnung.db`.

```
tabsordnung snapshot create <name> [--profile name]
tabsordnung snapshot list
tabsordnung snapshot restore <name> [--port N]
tabsordnung snapshot diff <name> [--profile name]
tabsordnung snapshot delete <name> [--yes]
```

`restore` requires the Firefox extension running in live mode.

### GitHub Triage

Classify GitHub tabs into groups (Needs Attention, Open PRs, Open Issues, Closed/Merged) based on issue/PR status, review requests, and assignment.

```
tabsordnung triage [--profile name] [--apply] [--port N]
```

Dry-run by default -- shows proposed moves and asks for confirmation. Use `--apply` to skip confirmation (for automation). Requires `gh auth login` or `GITHUB_TOKEN` environment variable.

## Keys

| Key | Action |
|-----|--------|
| `j`/`k` | Navigate up/down |
| `h` | Collapse group or jump to parent group |
| `l` | Expand group or enter first tab |
| `Enter` | Toggle expand/collapse |
| `f` | Open filter picker |
| `r` | Reload session data |
| `p` | Switch Firefox profile |
| `q` | Quit |

## Live mode

Install the companion Firefox extension from the `extension/` directory. The extension communicates with tabsordnung over a local WebSocket connection (default port 19191). Live mode enables:

- Real-time tab synchronization
- Close, focus, and move tabs from the TUI
- Snapshot restore and triage apply

## Supported platforms

Linux and macOS. Requires Firefox profile data on disk.
