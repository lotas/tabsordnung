# Sentinel Signals Design

## Goal

Add manual signal capture to the TUI. Select a Gmail/Slack/Matrix tab, press `g`, and the extension scrapes the page for unread items. Results display in the detail pane and persist to disk. Signals queue up and process one at a time.

## Extension — New `scrape-activity` Command

Add a `scrape-activity` case to `handleCommand` in `background.js`. Receives `tabId` and `source` hint, injects a selector-based scraper into that tab.

Per-source extractors (simple CSS selectors, fix when they break):

- **Gmail**: Unread rows (`tr.zE`), extract sender + subject text
- **Slack**: Unread channel indicators or visible message list, extract channel name + recent message text
- **Matrix**: Notification badges and room names with unread counts

Response over WebSocket:

```json
{
  "action": "scrape-activity-result",
  "tabId": 123,
  "source": "gmail",
  "items": [
    {"title": "From: Alice", "preview": "Production DB latency spike..."},
    {"title": "From: Bob", "preview": "Weekly sync notes"}
  ],
  "error": ""
}
```

Selectors live in a simple `switch` on source name. No abstraction.

## Signal Storage

Signals are markdown files on disk, organized by source.

**Path:** `~/.local/share/tabsordnung/signals/<source>/<timestamp>.md`

Example: `signals/gmail/2026-02-15T14-30-00.md`

File content:

```markdown
# Gmail Signal — 2026-02-15 14:30

- **From: Alice** — Production DB latency spike...
- **From: Bob** — Weekly sync notes
```

**`signals.md` append:** Every signal also appends to a single `signals.md` in the signals directory — future automation hook. External scripts tail this file.

**Source detection:** Match tab URL against known patterns:
- `*mail.google.com*` → gmail
- `*slack.com*` → slack
- `*matrix*` or `*element*` → matrix

Simple function, not a config file.

## TUI Integration

### Keybinding

`g` (siGnal) — active when a tab is selected whose URL matches a known source. No-op for other tabs.

### Detail Pane

All signals for the matched source listed in **reverse date order** (latest first), rendered as markdown with the existing glamour renderer and scrollable.

When signals exist:

```
Title
  Gmail - mail.google.com/mail

URL
  https://mail.google.com/mail/u/0/#inbox

Signals
  [glamour-rendered markdown of all signals, newest first]

  Press 'g' to capture new signal
```

When no signals exist:

```
Title
  Gmail - mail.google.com/mail

  Press 'g' to capture signal
```

Signal display takes priority over summary display for known-source tabs.

### Signal Queue

Signals process one at a time, sequentially (same pattern as summarization queue).

New model fields:

```go
signalQueue   []SignalRequest  // pending requests
signalActive  *SignalRequest   // currently processing

type SignalRequest struct {
    TabID  int
    Source string
    URL    string
    Title  string
}
```

Queue logic:

1. `g` press → append to `signalQueue`, show "Queued..." in detail pane
2. If `signalActive == nil`, pop first item and start it
3. On `signalCompleteMsg` → write files, clear `signalActive`, pop next from queue
4. On error → log error inline, move to next

Visual feedback: "Signal queued (2 ahead)" or "Capturing signal..." for active item.

## WebSocket Protocol

**Outbound (Go → Extension):**

```json
{
  "action": "scrape-activity",
  "tabId": 123,
  "source": "gmail"
}
```

`source` tells the extension which selectors to use. Go detects source from URL.

**Inbound (Extension → Go):**

```json
{
  "action": "scrape-activity-result",
  "tabId": 123,
  "source": "gmail",
  "items": [{"title": "...", "preview": "..."}],
  "error": ""
}
```

Go handles the response, writes signal files, dispatches `signalCompleteMsg` to TUI. Errors display inline in detail pane.

No new extension permissions needed — existing `scripting` and `tabs` suffice.
