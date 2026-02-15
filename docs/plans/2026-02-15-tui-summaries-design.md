# TUI Tab Summaries Design

## Goal

Embed per-tab article summarization into the TUI. When a tab is selected, show its summary in the detail pane if available, or let the user trigger summarization with a keybinding.

## File Naming: Domain Subfolders

New exported function `SummaryPath(outDir, url, title) string`:

1. Parse URL to extract hostname (e.g., `blog.example.de`)
2. Sanitize hostname: lowercase, replace dots/non-alphanumeric with dashes → `blog-example-de`
3. Sanitize title with existing `sanitizeFilename` logic → `some-article-title`
4. Result: `<outDir>/blog-example-de/some-article-title.md`

Both the CLI batch `Run()` and the new TUI flow use this function. Existing `sanitizeFilename` stays as-is (unexported).

## Detail Pane Layout

When a tab is selected:

**Summary exists on disk:**
```
Title
  Some Article Title

URL
  https://blog.example.de/article

Last Visited
  3 days ago

Status
  Stale (14 days)

Summary
  [glamour-rendered markdown of the summary text]
```

**No summary exists:**
```
Title
  ...
URL
  ...

  Press 's' to summarize
```

Summary text is extracted by splitting the stored file on `## Summary\n\n` and taking everything after. The file header (title, source, date) is not displayed since the detail pane already shows that info.

## Markdown Rendering

Use `charmbracelet/glamour` (the library behind glow) to render summary markdown:
- `glamour.DarkStyleConfig` to match TUI aesthetic
- Word wrap set to detail pane width

## Focus Model

Tab key toggles focus between tree and detail panes.

**Visual indicator:** Focused pane gets bright border color (`62`), unfocused gets dim (`240`).

**Detail pane focused:**
- `j/k` or `up/down` scroll the detail content
- `s` triggers summarization
- `Tab` switches back to tree
- `q` still quits

**Tree pane focused (current behavior):**
- All existing keybindings unchanged
- `Tab` switches to detail pane

## Async Summarization Flow

When user presses `s` on a tab:

1. Detail pane shows "Summarizing..." immediately
2. Background `tea.Cmd` goroutine:
   - `FetchReadable(tab.URL)` → article content
   - `OllamaSummarize(ctx, model, host, text)` → summary
   - `os.MkdirAll` for domain subfolder
   - Write markdown file to disk
   - Return `summarizeCompleteMsg{url, summary, err}`
3. On success: detail pane re-renders with glamour-rendered summary
4. On error: detail pane shows error inline (e.g., "Summarize failed: connection refused"), user can retry

## Model Changes

New fields on `tui.Model`:
- `summaryDir string` — output directory (env `TABSORDNUNG_SUMMARY_DIR` or `~/.local/share/tabsordnung/summaries/`)
- `ollamaModel string` — model name (env `TABSORDNUNG_MODEL` or `llama3.2`)
- `ollamaHost string` — Ollama URL (env `OLLAMA_HOST` or `http://localhost:11434`)
- `summarizing bool` — whether summarization is in progress
- `focusDetail bool` — which pane has focus
- `detailScroll int` — scroll offset for detail pane content

New message type: `summarizeCompleteMsg{url string, summary string, err error}`

Config resolved once in `NewModel` constructor.

## Summary Lookup

On each detail render for a tab:
1. `SummaryPath(summaryDir, tab.URL, tab.Title)` → expected file path
2. `os.ReadFile` — if exists, extract text after `## Summary\n\n`
3. Render with glamour, display below tab metadata

Sync file read is fast enough for local files. No caching needed.

## Dependencies

- Add `github.com/charmbracelet/glamour` to go.mod
