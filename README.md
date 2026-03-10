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
go install github.com/lotas/tabsordnung@latest
```

## Disclaimer

This software is provided "as-is". The author is not responsible for any account-level consequences or service disruptions resulting from the use of the activity scraping feature.

## Usage

```
tabsordnung                              # TUI (default)
tabsordnung export                       # Export tabs to stdout or file
tabsordnung signals <command>            # List/complete/reopen activity signals
tabsordnung github [list]                # List tracked GitHub entities
tabsordnung bugzilla [list]              # List tracked Bugzilla issues
tabsordnung profiles                     # List Firefox profiles
tabsordnung snapshot <command>           # Manage tab snapshots
tabsordnung triage                       # Classify GitHub tabs into groups
tabsordnung summarize                    # Summarize tabs via Ollama
tabsordnung rules view|edit              # Manage urgency classification rules
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
tabsordnung export [--profile X] [--json] [--out FILE] [--live] [--port N]
```

Exports tabs to stdout or a file. Use `--live` to export from the Firefox extension instead of session files.

### Signals

List active or completed activity signals captured from Gmail/Slack/Matrix.

```
tabsordnung signals
tabsordnung signals list [--all] [--json] [--source gmail|slack|matrix]
tabsordnung signals complete <id>
tabsordnung signals reopen <id>
```

### GitHub Entities

List tracked GitHub issues/PRs discovered from tabs and signals. Markdown output by default, JSON with `--json`.

```
tabsordnung github
tabsordnung github [--json] [--all] [--state open|closed|merged] [--kind pull|issue] [--repo owner/repo]
tabsordnung github list [--json] [--all] [--state open|closed|merged] [--kind pull|issue] [--repo owner/repo]
```

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

### Bugzilla

List tracked Bugzilla issues discovered from tabs. Shows bug summary, status, resolution, and assignment.

```
tabsordnung bugzilla
tabsordnung bugzilla list [--json] [--host domain]
```

### Summarize

Summarize tab content using a local Ollama LLM. Processes tabs in a named group, fetches readable page content, and saves markdown summaries organized by domain.

```
tabsordnung summarize [--profile name] [--model name] [--out-dir path] [--group name]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--profile` | | Firefox profile name |
| `--model` | `llama3.2` | Ollama model name (env: `TABSORDNUNG_MODEL`) |
| `--out-dir` | `~/.local/share/tabsordnung/summaries/` | Output directory for summary files |
| `--group` | `Summarize This` | Tab group name to summarize |

### Rules

Manage urgency classification rules used to augment LLM-based signal classification.

```
tabsordnung rules view                   # Show current rules
tabsordnung rules edit                   # Open rules file in $EDITOR
```

Rules file location: `~/.config/tabsordnung/rules`

### GitHub Triage

Classify GitHub tabs into groups (Needs Attention, Open PRs, Open Issues, Closed/Merged) based on issue/PR status, review requests, and assignment.

```
tabsordnung triage [--profile name] [--apply] [--port N]
```

Dry-run by default -- shows proposed moves and asks for confirmation. Use `--apply` to skip confirmation (for automation). Requires `gh auth login` or `GITHUB_TOKEN` environment variable.

## Install local extension

To use live mode, load the extension from this repository into Firefox:

1. Open Firefox and go to `about:debugging#/runtime/this-firefox`.
2. Click **Load Temporary Add-on...**.
3. Select `extension/manifest.json` from this repo.
4. Start tabsordnung with live mode enabled: `tabsordnung --live` (or `tabsordnung --live --port 19191`).

Note: this is a temporary add-on install for local development/testing.

## TUI Views

The TUI has five views, switchable with number keys:

| Key | View | Description |
|-----|------|-------------|
| `1` | Tabs | Firefox tabs grouped by tab group, with analysis |
| `2` | Signals | Activity signals from Gmail, Slack, Matrix |
| `3` | GitHub | Tracked GitHub issues and PRs |
| `4` | Bugzilla | Tracked Bugzilla bugs |
| `5` | Snapshots | Saved tab snapshots |

## Keys

### Global

| Key | Action |
|-----|--------|
| `1`-`5` | Switch between views |
| `j`/`k` or `↑`/`↓` | Navigate up/down |
| `h` | Collapse group or jump to parent |
| `l` | Expand group or descend |
| `Tab` | Toggle focus between left pane and detail pane |
| `p` | Switch Firefox profile / source |
| `q` / `Ctrl+C` | Quit |

### Tabs view

| Key | Action |
|-----|--------|
| `Enter` | Focus tab in browser (live) or expand/collapse group |
| `Space` | Toggle select tab (live mode, multi-select) |
| `f` | Open filter picker |
| `t` | Cycle display mode (URL / Title / Both) |
| `s` | Summarize tab with Ollama |
| `c` | Capture signals from tab |
| `r` | Reload session data |
| `x` | Close selected tab(s) (live mode) |
| `g` | Move selected tab(s) to group (live mode) |
| `Esc` | Clear multi-select |

### Signals view

| Key | Action |
|-----|--------|
| `Enter` | Navigate to signal in browser |
| `x` | Mark signal as complete |
| `u` | Reopen completed signal |
| `[`/`]` | Cycle urgency (fyi / review / urgent) |

### GitHub / Bugzilla views

| Key | Action |
|-----|--------|
| `Enter` | Show detail pane |
| `t` | Toggle tree mode (grouped) vs flat list |
| `f` | Cycle filter |
| `o` | Open in browser |
| `r` | Refresh from API |

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `TABSORDNUNG_PROFILE` | | Default Firefox profile (overridden by `--profile`) |
| `TABSORDNUNG_MODEL` | `llama3.2` | Ollama model for summarization (overridden by `--model`) |
| `OLLAMA_HOST` | `http://localhost:11434` | Ollama server URL |
| `TABSORDNUNG_SUMMARY_DIR` | `~/.local/share/tabsordnung/summaries` | Output directory for summaries |
| `GITHUB_TOKEN` | | GitHub token (alternative to `gh auth login`) |
| `EDITOR` | `vi` | Editor for `rules edit` command |

## Live mode

Install the companion Firefox extension from the `extension/` directory. The extension communicates with tabsordnung over a local WebSocket connection (default port 19191). Live mode enables:

- Real-time tab synchronization
- Close, focus, and move tabs from the TUI
- Snapshot restore and triage apply

## Supported platforms

Linux and macOS. Requires Firefox profile data on disk.

## Ethics & Responsible Use

Tabsordnung is designed to respect the privacy of your data and the stability of the services it interacts with.

### Personal Use & Privacy
- **Local Only:** All activity signals (Slack, Gmail, Matrix) are processed entirely on your local machine. No data is ever transmitted to external servers or third-party APIs.
- **No Credentials:** The extension leverages your existing, active browser session. It does not see, store, or require your passwords or auth tokens.

### Respecting Service Boundaries
While the activity signal feature is designed to enhance personal productivity, please be aware of the following:
- **Terms of Service:** Automating interactions with web services (even locally) often falls outside of their standard Terms of Service. Use this feature at your own discretion; the primary risk is automated detection leading to temporary account flags or suspension.
- **Minimal Impact:** To minimize footprint, the extension only queries notification counts on-demand and targets specific DOM elements rather than full page content.
- **Educational Intent:** This feature is a proof-of-concept for local browser-to-TUI integration. If you require a production-grade solution for team workflows, please use official APIs.
