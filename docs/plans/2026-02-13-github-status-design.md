# GitHub Issue/PR Status Check — Design Document

## Overview

Tabsordnung gains a GitHub analyzer that checks whether open GitHub issue/PR tabs are still open or have been closed/merged. This surfaces "done" items so users can close stale GitHub tabs.

## URL Parsing

Recognized patterns:
- `github.com/{owner}/{repo}/issues/{number}`
- `github.com/{owner}/{repo}/pull/{number}`

Extracts `owner`, `repo`, `number`, and `kind` (issue vs PR). Tabs not matching these patterns are skipped.

## Data Model Changes

### `types.Tab` — new field

```go
GitHubStatus string // "open", "closed", "merged", "" (not a GitHub URL)
```

`merged` is PR-specific. Both `closed` and `merged` count as "done" for filtering.

### `types.FilterMode` — new constant

```go
FilterGitHubDone // tabs where GitHubStatus is "closed" or "merged"
```

### `types.Stats` — new field

```go
GitHubDoneTabs int
```

## Token Resolution

GitHub's GraphQL API requires authentication. Token resolution order (tried once per analysis run):

1. `gh auth token` (captures stdout from the `gh` CLI)
2. `GITHUB_TOKEN` environment variable
3. No token found → skip GitHub analysis silently

## GraphQL Query

All GitHub tabs are batched into a single GraphQL request, grouped by `owner/repo`:

```graphql
query {
  r0: repository(owner: "org", name: "repo") {
    i0: issue(number: 42) { state }
    p1: pullRequest(number: 99) { state }
  }
  r1: repository(owner: "other", name: "lib") {
    i2: issue(number: 7) { state }
  }
}
```

- Issue state: `OPEN` or `CLOSED`
- PR state: `OPEN`, `CLOSED`, or `MERGED`
- Single HTTPS POST to `api.github.com/graphql`, 5-second timeout
- On failure (rate limit, network, no token): silently skip, no error in UI

## Analyzer

```go
func AnalyzeGitHub(tabs []*types.Tab) error
```

Synchronous — one HTTP call covers all tabs. No channel/streaming needed. Sets `GitHubStatus` on each matched tab.

Runs in the analysis pipeline: stale → duplicates → GitHub → dead links.

## TUI Changes

### Tree view markers

- `✓` green — closed or merged (done)
- `○` purple — open (still active)

Displayed alongside existing markers (a tab can be both a closed GitHub issue and stale).

### Filter

New "GitHub done" option in the filter popup. Shows only tabs where `GitHubStatus == "closed" || GitHubStatus == "merged"`.

### Status bar

Displays `N done` (green) when `GitHubDoneTabs > 0`.

## No new CLI flags or keybindings

The feature is automatic — if a GitHub token is available, GitHub tabs get checked on every analysis run.

## File changes

- `internal/types/types.go` — Add `GitHubStatus` field, `FilterGitHubDone` constant, `GitHubDoneTabs` stat
- `internal/analyzer/github.go` — New file: URL parsing, token resolution, GraphQL query, `AnalyzeGitHub()`
- `internal/analyzer/github_test.go` — Tests for URL parsing and response mapping
- `internal/analyzer/summary.go` — Count `GitHubDoneTabs` in stats
- `internal/tui/tree.go` — Render `✓`/`○` markers, add `matchesFilter` case
- `internal/tui/app.go` — Wire GitHub analyzer into analysis pipeline, add filter option
