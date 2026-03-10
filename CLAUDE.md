# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build Commands

All commands must use `GOMAXPROCS=1` and `-p 1` due to environment resource constraints.

```bash
make build          # GOMAXPROCS=1 go build -p 1 -o tabsordnung .
make test           # GOMAXPROCS=1 go test -p 1 ./... -v
make run            # build + run
make run-live       # build + run with --live flag
make clean          # remove binary
make install        # symlink binary to ~/.local/bin/
```

Run a single test:
```bash
GOMAXPROCS=1 go test -p 1 -run TestName ./internal/firefox/ -v
```

## Architecture

Go TUI app built with Bubble Tea that reads Firefox session files and analyzes tabs for staleness, duplicates, dead links, and tracks GitHub/Bugzilla status. Includes activity signal capture from Gmail/Slack/Matrix and Ollama-based tab summarization.

**Data flow**: `main.go` → profile discovery → session file read (mozlz4 decompress) → JSON parse → analysis → TUI display

**TUI views**: Tabs, Signals, GitHub, Bugzilla, Snapshots (switchable with `1`-`5`)

## CLI Commands

Main subcommands in `main.go`:

- `tabsordnung` (default TUI)
- `tabsordnung export [--json] [--out FILE] [--live] [--port N]`
- `tabsordnung snapshot ...`
- `tabsordnung triage ...`
- `tabsordnung summarize [--profile X] [--model X] [--out-dir X] [--group X]`
- `tabsordnung signals list [--all] [--json] [--source X]`
- `tabsordnung github [list] [--json] [--all] [--state open|closed|merged] [--kind pull|issue] [--repo owner/repo]`
- `tabsordnung bugzilla [list] [--json] [--host domain]`
- `tabsordnung rules view|edit`
- `tabsordnung profiles`

### Packages

- **`internal/types/`** — Shared types: `Tab`, `TabGroup`, `Profile`, `SessionData`, `Stats`, `FilterMode`, `SortMode`, `TabDisplayMode`
- **`internal/firefox/`** — Profile discovery (`profiles.ini` parsing) and session reading (mozlz4 decompression + JSON parse)
- **`internal/analyzer/`** — Stale detection, duplicate detection (URL normalization), dead link checking (async HTTP HEAD with concurrency limit of 10), GitHub status via GraphQL, summary stats
- **`internal/storage/`** — SQLite schema, snapshots, signals, GitHub entities, Bugzilla entities, events, migrations
- **`internal/tui/`** — Bubble Tea models: `app.go` (main model), `tabs_view.go`, `signals_view.go`, `github_view.go`, `bugzilla_view.go`, `snapshots_view.go`, `tree.go`, `detail.go`, `navbar.go`, `filter_picker.go`, `group_picker.go`, `source_picker.go`
- **`internal/server/`** — WebSocket server for live Firefox extension communication
- **`internal/export/`** — Session export formatters (Markdown, JSON)
- **`internal/snapshot/`** — Snapshot creation, diffing, and restoration via live mode
- **`internal/triage/`** — GitHub tab classification (Needs Attention, Open PRs, Open Issues, Closed/Merged) and live mode moves
- **`internal/summarize/`** — Ollama-based tab content summarization (fetch readable content, LLM summary, markdown output)
- **`internal/classify/`** — Email urgency classification: heuristic detection + LLM classification (urgent/review/fyi), custom rules file
- **`internal/github/`** — GitHub entity extraction from tab URLs and signals, metadata refresh
- **`internal/bugzilla/`** — Bugzilla issue tracking via REST API (summary, status, resolution, assignment), refresh with cooldown
- **`internal/signal/`** — Signal detection and parsing for Gmail/Slack/Matrix URLs, deduplication
- **`internal/applog/`** — Structured file-based application logging with rotation

### Key Technical Details

- **mozlz4 format**: 8-byte magic `mozLz40\0` + 4-byte LE uint32 uncompressed size + raw LZ4 block. Must use `lz4.UncompressBlock()`, NOT `lz4.NewReader()` (not framed format).
- **Firefox session JSON**: Current page is `entries[index-1]` (1-based index). `lastAccessed` is Unix milliseconds. Tab-to-group field is `groupId`.
- **Session file fallback**: Tries `recovery.jsonlz4` first (active session), then `previous.jsonlz4` (closed session).
- **Bubble Tea Init() pitfall**: Uses value receiver — cannot persist state changes. Set state in the constructor (`NewModel`) or handle in `Update()`.
- **Dead link analysis**: Async with goroutines, 10-request semaphore, 5s per-request timeout. Results stream via channel for progressive UI updates. Skips `about:`, `moz-extension:`, `file:`, `chrome:`, `resource:`, `data:` URLs.
