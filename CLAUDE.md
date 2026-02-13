# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build Commands

All commands must use `GOMAXPROCS=1` and `-p 1` due to environment resource constraints.

```bash
make build          # GOMAXPROCS=1 go build -p 1 -o tabsordnung .
make test           # GOMAXPROCS=1 go test -p 1 ./... -v
make run            # build + run
make clean          # remove binary
```

Run a single test:
```bash
GOMAXPROCS=1 go test -p 1 -run TestName ./internal/firefox/ -v
```

## Architecture

Go TUI app built with Bubble Tea that reads Firefox session files and analyzes tabs for staleness, duplicates, and dead links.

**Data flow**: `main.go` → profile discovery → session file read (mozlz4 decompress) → JSON parse → analysis → TUI display

### Packages

- **`internal/types/`** — Shared types: `Tab`, `TabGroup`, `Profile`, `SessionData`, `Stats`, `FilterMode`, `SortMode`
- **`internal/firefox/`** — Profile discovery (`profiles.ini` parsing) and session reading (mozlz4 decompression + JSON parse)
- **`internal/analyzer/`** — Stale detection, duplicate detection (URL normalization), dead link checking (async HTTP HEAD with concurrency limit of 10), summary stats
- **`internal/tui/`** — Bubble Tea models: `app.go` (main model), `tree.go` (collapsible group tree), `detail.go` (right pane), `profile_picker.go` (profile selection overlay)

### Key Technical Details

- **mozlz4 format**: 8-byte magic `mozLz40\0` + 4-byte LE uint32 uncompressed size + raw LZ4 block. Must use `lz4.UncompressBlock()`, NOT `lz4.NewReader()` (not framed format).
- **Firefox session JSON**: Current page is `entries[index-1]` (1-based index). `lastAccessed` is Unix milliseconds. Tab-to-group field is `groupId`.
- **Session file fallback**: Tries `recovery.jsonlz4` first (active session), then `previous.jsonlz4` (closed session).
- **Bubble Tea Init() pitfall**: Uses value receiver — cannot persist state changes. Set state in the constructor (`NewModel`) or handle in `Update()`.
- **Dead link analysis**: Async with goroutines, 10-request semaphore, 5s per-request timeout. Results stream via channel for progressive UI updates. Skips `about:`, `moz-extension:`, `file:`, `chrome:`, `resource:`, `data:` URLs.
