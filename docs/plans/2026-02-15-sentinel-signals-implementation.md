# Sentinel Signals Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add manual signal capture to the TUI — press a key on a Gmail/Slack/Matrix tab to scrape unread items via the extension, display results in the detail pane, and persist to disk.

**Architecture:** The extension gets a new `scrape-activity` command with per-source CSS selectors. The Go side adds a signal queue (mirroring the summarize queue), signal file storage, and detail pane rendering. Signals display in reverse chronological order using the existing glamour markdown renderer.

**Tech Stack:** Go/Bubble Tea, Firefox WebExtension scripting API, glamour markdown rendering

---

### Task 1: Add `scrape-activity` command to extension

**Files:**
- Modify: `extension/background.js:104-162` (add case to `handleCommand` switch)

**Step 1: Add the `scrape-activity` case**

Add this case before the `default:` line in `handleCommand`:

```javascript
case "scrape-activity": {
  const scrapers = {
    gmail: () => {
      const rows = document.querySelectorAll("tr.zE");
      return Array.from(rows).slice(0, 20).map(row => {
        const sender = row.querySelector(".yX.yW span")?.getAttribute("name") || row.querySelector(".yX.yW")?.textContent?.trim() || "";
        const subject = row.querySelector(".bog span")?.textContent?.trim() || row.querySelector(".y6 span")?.textContent?.trim() || "";
        return { title: sender, preview: subject };
      });
    },
    slack: () => {
      const unreads = document.querySelectorAll(".p-channel_sidebar__link--unread .p-channel_sidebar__name");
      if (unreads.length > 0) {
        return Array.from(unreads).slice(0, 20).map(el => ({
          title: el.textContent?.trim() || "",
          preview: "unread channel",
        }));
      }
      const msgs = document.querySelectorAll("[data-qa='virtual-list-item'] .c-message_kit__text");
      return Array.from(msgs).slice(-20).map(el => ({
        title: "",
        preview: el.textContent?.trim() || "",
      }));
    },
    matrix: () => {
      const badges = document.querySelectorAll(".mx_RoomTile_badge, .mx_NotificationBadge");
      const rooms = document.querySelectorAll(".mx_RoomTile");
      const items = [];
      rooms.forEach(room => {
        const badge = room.querySelector(".mx_RoomTile_badge, .mx_NotificationBadge");
        if (badge && badge.textContent?.trim() !== "0") {
          const name = room.querySelector(".mx_RoomTile_title")?.textContent?.trim() || "";
          items.push({ title: name, preview: badge.textContent?.trim() + " unread" });
        }
      });
      return items.length > 0 ? items : Array.from(badges).slice(0, 20).map(b => ({
        title: "",
        preview: b.textContent?.trim() + " notifications",
      }));
    },
  };

  const scraper = scrapers[msg.source];
  if (!scraper) {
    send({ id: msg.id, ok: false, error: `unknown source: ${msg.source}` });
    return;
  }

  const results = await browser.scripting.executeScript({
    target: { tabId: msg.tabId },
    func: scraper,
  });

  const items = results?.[0]?.result || [];
  send({ id: msg.id, ok: true, items: JSON.stringify(items), source: msg.source });
  return;
}
```

**Step 2: Verify manually**

Open Firefox, navigate to Gmail/Slack/Matrix. In the extension debug console, run:
```javascript
handleCommand({ action: "scrape-activity", tabId: <id>, source: "gmail", id: "test-1" })
```
Verify the response contains items.

**Step 3: Commit**

```bash
git add extension/background.js
git commit -m "feat: add scrape-activity command to extension"
```

---

### Task 2: Add `Source` field to `OutgoingMsg` and `Items` field to `IncomingMsg`

**Files:**
- Modify: `internal/server/server.go:14-46`

**Step 1: Write test for new message fields**

Create `internal/server/signal_msg_test.go`:

```go
package server

import (
	"encoding/json"
	"testing"
)

func TestOutgoingMsgSourceField(t *testing.T) {
	msg := OutgoingMsg{
		ID:     "cmd-1",
		Action: "scrape-activity",
		TabID:  123,
		Source: "gmail",
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]interface{}
	json.Unmarshal(data, &parsed)
	if parsed["source"] != "gmail" {
		t.Errorf("source = %v, want gmail", parsed["source"])
	}
}

func TestIncomingMsgItemsField(t *testing.T) {
	raw := `{"id":"cmd-1","ok":true,"items":"[{\"title\":\"Alice\",\"preview\":\"hello\"}]","source":"gmail"}`
	var msg IncomingMsg
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Items != `[{"title":"Alice","preview":"hello"}]` {
		t.Errorf("items = %q", msg.Items)
	}
	if msg.Source != "gmail" {
		t.Errorf("source = %q, want gmail", msg.Source)
	}
}
```

**Step 2: Run test to verify it fails**

```bash
GOMAXPROCS=1 go test -p 1 -run TestOutgoingMsgSourceField ./internal/server/ -v
GOMAXPROCS=1 go test -p 1 -run TestIncomingMsgItemsField ./internal/server/ -v
```

Expected: FAIL — `Source` and `Items` fields don't exist yet.

**Step 3: Add fields to structs**

In `internal/server/server.go`, add to `IncomingMsg`:
```go
Items   string `json:"items,omitempty"`
Source  string `json:"source,omitempty"`
```

Add to `OutgoingMsg`:
```go
Source  string `json:"source,omitempty"`
```

**Step 4: Run tests to verify they pass**

```bash
GOMAXPROCS=1 go test -p 1 ./internal/server/ -v
```

Expected: ALL PASS.

**Step 5: Commit**

```bash
git add internal/server/server.go internal/server/signal_msg_test.go
git commit -m "feat: add Source/Items fields to WebSocket messages"
```

---

### Task 3: Create signal storage package

**Files:**
- Create: `internal/signal/signal.go`
- Create: `internal/signal/signal_test.go`

**Step 1: Write tests for signal storage**

Create `internal/signal/signal_test.go`:

```go
package signal

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDetectSource(t *testing.T) {
	tests := []struct {
		url    string
		want   string
	}{
		{"https://mail.google.com/mail/u/0/#inbox", "gmail"},
		{"https://app.slack.com/client/T123/C456", "slack"},
		{"https://my-company.slack.com/", "slack"},
		{"https://app.element.io/#/room/!abc:matrix.org", "matrix"},
		{"https://matrix.example.com/", "matrix"},
		{"https://github.com/foo/bar", ""},
		{"https://example.com", ""},
	}
	for _, tt := range tests {
		got := DetectSource(tt.url)
		if got != tt.want {
			t.Errorf("DetectSource(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestWriteAndReadSignal(t *testing.T) {
	dir := t.TempDir()
	sig := Signal{
		Source:     "gmail",
		CapturedAt: time.Date(2026, 2, 15, 14, 30, 0, 0, time.UTC),
		Items: []SignalItem{
			{Title: "From: Alice", Preview: "Production DB latency spike"},
			{Title: "From: Bob", Preview: "Weekly sync notes"},
		},
	}

	path, err := WriteSignal(dir, sig)
	if err != nil {
		t.Fatal(err)
	}

	// Check file exists under gmail/
	if filepath.Dir(path) != filepath.Join(dir, "gmail") {
		t.Errorf("path dir = %q, want gmail subdir", filepath.Dir(path))
	}

	// Read all signals for source
	signals, err := ReadSignals(dir, "gmail")
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 1 {
		t.Fatalf("got %d signals, want 1", len(signals))
	}
	if len(signals[0].Items) != 2 {
		t.Errorf("got %d items, want 2", len(signals[0].Items))
	}
}

func TestReadSignalsReverseChronological(t *testing.T) {
	dir := t.TempDir()

	sig1 := Signal{
		Source:     "slack",
		CapturedAt: time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC),
		Items:      []SignalItem{{Title: "first", Preview: "old"}},
	}
	sig2 := Signal{
		Source:     "slack",
		CapturedAt: time.Date(2026, 2, 15, 14, 0, 0, 0, time.UTC),
		Items:      []SignalItem{{Title: "second", Preview: "new"}},
	}

	WriteSignal(dir, sig1)
	WriteSignal(dir, sig2)

	signals, err := ReadSignals(dir, "slack")
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 2 {
		t.Fatalf("got %d signals, want 2", len(signals))
	}
	// Newest first
	if signals[0].Items[0].Title != "second" {
		t.Errorf("first signal title = %q, want 'second'", signals[0].Items[0].Title)
	}
}

func TestAppendSignalLog(t *testing.T) {
	dir := t.TempDir()
	sig := Signal{
		Source:     "gmail",
		CapturedAt: time.Date(2026, 2, 15, 14, 30, 0, 0, time.UTC),
		Items: []SignalItem{
			{Title: "Alice", Preview: "hello"},
		},
	}

	AppendSignalLog(dir, sig)

	data, err := os.ReadFile(filepath.Join(dir, "signals.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Error("signals.md is empty")
	}
}

func TestParseItemsJSON(t *testing.T) {
	raw := `[{"title":"Alice","preview":"hello"},{"title":"Bob","preview":"world"}]`
	items, err := ParseItemsJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].Title != "Alice" {
		t.Errorf("items[0].Title = %q", items[0].Title)
	}
}
```

**Step 2: Run tests to verify they fail**

```bash
GOMAXPROCS=1 go test -p 1 -run TestDetectSource ./internal/signal/ -v
```

Expected: FAIL — package doesn't exist.

**Step 3: Implement the signal package**

Create `internal/signal/signal.go`:

```go
package signal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type SignalItem struct {
	Title   string `json:"title"`
	Preview string `json:"preview"`
}

type Signal struct {
	Source     string
	CapturedAt time.Time
	Items      []SignalItem
}

// DetectSource returns the signal source name for a URL, or "" if not a known source.
func DetectSource(url string) string {
	lower := strings.ToLower(url)
	switch {
	case strings.Contains(lower, "mail.google.com"):
		return "gmail"
	case strings.Contains(lower, "slack.com"):
		return "slack"
	case strings.Contains(lower, "element.io"),
		strings.Contains(lower, "matrix."):
		return "matrix"
	}
	return ""
}

// ParseItemsJSON parses the JSON string of items from the extension response.
func ParseItemsJSON(raw string) ([]SignalItem, error) {
	var items []SignalItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, err
	}
	return items, nil
}

// WriteSignal writes a signal to disk as a markdown file.
// Returns the written file path.
func WriteSignal(dir string, sig Signal) (string, error) {
	subDir := filepath.Join(dir, sig.Source)
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		return "", err
	}

	filename := sig.CapturedAt.Format("2006-01-02T15-04-05") + ".md"
	path := filepath.Join(subDir, filename)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("# %s Signal — %s\n\n", strings.Title(sig.Source), sig.CapturedAt.Format("2006-01-02 15:04")))
	for _, item := range sig.Items {
		if item.Title != "" {
			b.WriteString(fmt.Sprintf("- **%s** — %s\n", item.Title, item.Preview))
		} else {
			b.WriteString(fmt.Sprintf("- %s\n", item.Preview))
		}
	}

	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// ReadSignals reads all signals for a source, returned newest-first.
func ReadSignals(dir, source string) ([]Signal, error) {
	subDir := filepath.Join(dir, source)
	entries, err := os.ReadDir(subDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	// Sort by name descending (filenames are timestamps)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() > entries[j].Name()
	})

	var signals []Signal
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(subDir, entry.Name()))
		if err != nil {
			continue
		}
		sig := parseSignalMarkdown(source, entry.Name(), string(data))
		signals = append(signals, sig)
	}
	return signals, nil
}

func parseSignalMarkdown(source, filename, content string) Signal {
	// Parse timestamp from filename: 2026-02-15T14-30-00.md
	name := strings.TrimSuffix(filename, ".md")
	ts, _ := time.Parse("2006-01-02T15-04-05", name)

	var items []SignalItem
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		line = strings.TrimPrefix(line, "- ")
		if strings.HasPrefix(line, "**") {
			// Format: **Title** — Preview
			parts := strings.SplitN(line, "** — ", 2)
			if len(parts) == 2 {
				title := strings.TrimPrefix(parts[0], "**")
				items = append(items, SignalItem{Title: title, Preview: parts[1]})
			}
		} else {
			items = append(items, SignalItem{Preview: line})
		}
	}

	return Signal{Source: source, CapturedAt: ts, Items: items}
}

// AppendSignalLog appends a signal summary to signals.md in the base dir.
func AppendSignalLog(dir string, sig Signal) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "signals.md")

	var b strings.Builder
	b.WriteString(fmt.Sprintf("\n## %s — %s\n\n", strings.Title(sig.Source), sig.CapturedAt.Format("2006-01-02 15:04")))
	for _, item := range sig.Items {
		if item.Title != "" {
			b.WriteString(fmt.Sprintf("- **%s** — %s\n", item.Title, item.Preview))
		} else {
			b.WriteString(fmt.Sprintf("- %s\n", item.Preview))
		}
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(b.String())
	return err
}

// RenderSignalsMarkdown renders all signals for a source as a single markdown string.
func RenderSignalsMarkdown(signals []Signal) string {
	var b strings.Builder
	for i, sig := range signals {
		if i > 0 {
			b.WriteString("\n---\n\n")
		}
		b.WriteString(fmt.Sprintf("### %s — %s\n\n", strings.Title(sig.Source), sig.CapturedAt.Format("2006-01-02 15:04")))
		for _, item := range sig.Items {
			if item.Title != "" {
				b.WriteString(fmt.Sprintf("- **%s** — %s\n", item.Title, item.Preview))
			} else {
				b.WriteString(fmt.Sprintf("- %s\n", item.Preview))
			}
		}
	}
	return b.String()
}
```

**Step 4: Run tests to verify they pass**

```bash
GOMAXPROCS=1 go test -p 1 ./internal/signal/ -v
```

Expected: ALL PASS.

**Step 5: Commit**

```bash
git add internal/signal/
git commit -m "feat: add signal storage package"
```

---

### Task 4: Add signal queue and keybinding to TUI

**Files:**
- Modify: `internal/tui/app.go`

**Step 1: Add new message type and model fields**

At the top of `app.go`, after `summarizeCompleteMsg` (line 37), add:

```go
type signalCompleteMsg struct {
	source string
	err    error
}
```

Add a `SignalJob` struct after `SummarizeJob` (line 91):

```go
// SignalJob tracks a single in-flight signal capture.
type SignalJob struct {
	Tab       *types.Tab
	Source    string
	ContentID string // command ID waiting for extension response
}
```

Add fields to `Model` struct after the summarization block (after line 134):

```go
	// Signals
	signalDir     string
	signalQueue   []*SignalJob
	signalActive  *SignalJob
	signalErrors  map[string]string // source → error message
```

**Step 2: Initialize signal fields in `NewModel`**

In `NewModel` (line 140), add signal dir resolution and initialization. Add a `signalDir` parameter to `NewModel` signature. Initialize:

```go
signalDir:     signalDir,
signalErrors:  make(map[string]string),
```

The `signalDir` is resolved in `main.go` using env `TABSORDNUNG_SIGNAL_DIR` or default `~/.local/share/tabsordnung/signals/`.

**Step 3: Add the keybinding**

In the main `switch msg.String()` block (around line 465), add a new case. Since `g` is taken for group move, use `c` (Capture):

```go
case "c":
	if m.mode != ModeLive || !m.connected {
		break
	}
	node := m.tree.SelectedNode()
	if node == nil || node.Tab == nil {
		break
	}
	source := signal.DetectSource(node.Tab.URL)
	if source == "" {
		break
	}
	// Check not already queued or active
	for _, j := range m.signalQueue {
		if j.Tab.BrowserID == node.Tab.BrowserID {
			break
		}
	}
	if m.signalActive != nil && m.signalActive.Tab.BrowserID == node.Tab.BrowserID {
		break
	}
	delete(m.signalErrors, source)
	job := &SignalJob{Tab: node.Tab, Source: source}
	m.signalQueue = append(m.signalQueue, job)
	return m, m.processNextSignal()
```

**Step 4: Add signal processing helpers**

```go
func (m *Model) processNextSignal() tea.Cmd {
	if m.signalActive != nil || len(m.signalQueue) == 0 {
		return nil
	}
	m.signalActive = m.signalQueue[0]
	m.signalQueue = m.signalQueue[1:]

	id, cmd := sendCmdWithID(m.server, server.OutgoingMsg{
		Action: "scrape-activity",
		TabID:  m.signalActive.Tab.BrowserID,
		Source: m.signalActive.Source,
	})
	m.signalActive.ContentID = id
	return cmd
}

func runWriteSignal(dir string, sig signal.Signal) tea.Cmd {
	return func() tea.Msg {
		_, err := signal.WriteSignal(dir, sig)
		if err != nil {
			return signalCompleteMsg{source: sig.Source, err: err}
		}
		signal.AppendSignalLog(dir, sig)
		return signalCompleteMsg{source: sig.Source}
	}
}
```

**Step 5: Handle signal responses in `wsCmdResponseMsg`**

In the `wsCmdResponseMsg` handler (line 692), before the summarize job check, add signal job check:

```go
case wsCmdResponseMsg:
	// Check if this response matches a signal job
	if m.signalActive != nil && m.signalActive.ContentID == msg.id {
		source := m.signalActive.Source
		m.signalActive = nil
		if !msg.ok {
			m.signalErrors[source] = msg.error
			return m, tea.Batch(listenWebSocket(m.server), m.processNextSignal())
		}
		items, err := signal.ParseItemsJSON(msg.items)
		if err != nil || len(items) == 0 {
			errMsg := "no items found"
			if err != nil {
				errMsg = err.Error()
			}
			m.signalErrors[source] = errMsg
			return m, tea.Batch(listenWebSocket(m.server), m.processNextSignal())
		}
		sig := signal.Signal{
			Source:     source,
			CapturedAt: time.Now(),
			Items:      items,
		}
		return m, tea.Batch(
			listenWebSocket(m.server),
			runWriteSignal(m.signalDir, sig),
		)
	}
	// ... existing summarize job check ...
```

**Step 6: Handle `signalCompleteMsg`**

Add a new case in the `Update` method, near the `summarizeCompleteMsg` handler:

```go
case signalCompleteMsg:
	if msg.err != nil {
		m.signalErrors[msg.source] = msg.err.Error()
	} else {
		delete(m.signalErrors, msg.source)
	}
	return m, m.processNextSignal()
```

**Step 7: Commit**

```bash
git add internal/tui/app.go
git commit -m "feat: add signal queue and keybinding to TUI"
```

---

### Task 5: Update `main.go` to resolve signal config

**Files:**
- Modify: `main.go`

**Step 1: Add signal dir resolution**

After the `summaryDir` resolution block, add:

```go
signalDir := os.Getenv("TABSORDNUNG_SIGNAL_DIR")
if signalDir == "" {
	home, _ := os.UserHomeDir()
	signalDir = filepath.Join(home, ".local", "share", "tabsordnung", "signals")
}
```

**Step 2: Pass `signalDir` to `NewModel`**

Update the `tui.NewModel` call to include `signalDir`:

```go
model := tui.NewModel(profiles, *staleDays, *liveMode, srv, summaryDir, resolvedModel, ollamaHost, signalDir)
```

**Step 3: Build and verify**

```bash
make build
```

Expected: Compiles cleanly.

**Step 4: Commit**

```bash
git add main.go
git commit -m "feat: resolve signal dir config in main"
```

---

### Task 6: Display signals in the detail pane

**Files:**
- Modify: `internal/tui/detail.go`
- Modify: `internal/tui/app.go` (View method)

**Step 1: Add `ViewTabWithSignal` to detail.go**

Add after `ViewTabWithSummary`:

```go
// ViewTabWithSignal renders tab info with signal content.
func (m *DetailModel) ViewTabWithSignal(tab *types.Tab, signalContent string, capturing bool, signalErr string) string {
	base := m.ViewTab(tab)

	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	activeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))

	if capturing {
		base += "\n" + activeStyle.Render("Capturing signal...")
	} else if signalContent != "" {
		base += "\n" + labelStyle.Render("Signals") + "\n" + signalContent
	} else if signalErr != "" {
		base += "\n" + errStyle.Render("Signal failed: "+signalErr)
		base += "\n" + dimStyle.Render("  Press 'c' to retry")
	} else {
		base += "\n" + dimStyle.Render("  Press 'c' to capture signal")
	}

	return base
}
```

**Step 2: Update the View() rendering in app.go**

In the `View()` method (around line 911), replace the detail rendering block. When the selected tab is a signal source, show signals instead of summary:

```go
if node.Tab != nil {
	source := signal.DetectSource(node.Tab.URL)
	if source != "" {
		// Signal source tab — show signals
		var signalContent string
		signals, _ := signal.ReadSignals(m.signalDir, source)
		if len(signals) > 0 {
			raw := signal.RenderSignalsMarkdown(signals)
			r, _ := glamour.NewTermRenderer(
				glamour.WithStylePath("dark"),
				glamour.WithWordWrap(m.detail.Width-2),
			)
			if rendered, err := r.Render(raw); err == nil {
				signalContent = rendered
			} else {
				signalContent = raw
			}
		}
		isCapturing := m.signalActive != nil && m.signalActive.Source == source
		if !isCapturing {
			for _, j := range m.signalQueue {
				if j.Source == source {
					isCapturing = true
					break
				}
			}
		}
		sigErr := m.signalErrors[source]
		detailContent = m.detail.ViewTabWithSignal(node.Tab, signalContent, isCapturing, sigErr)
	} else {
		// Regular tab — show summary (existing code)
		var summaryText string
		sumPath := summarize.SummaryPath(m.summaryDir, node.Tab.URL, node.Tab.Title)
		if raw, err := summarize.ReadSummary(sumPath); err == nil {
			r, _ := glamour.NewTermRenderer(
				glamour.WithStylePath("dark"),
				glamour.WithWordWrap(m.detail.Width-2),
			)
			if rendered, err := r.Render(raw); err == nil {
				summaryText = rendered
			} else {
				summaryText = raw
			}
		}
		_, isSummarizing := m.summarizeJobs[node.Tab.URL]
		tabErr := m.summarizeErrors[node.Tab.URL]
		detailContent = m.detail.ViewTabWithSummary(node.Tab, summaryText, isSummarizing, tabErr)
	}
}
```

**Step 3: Update the bottom bar**

In the bottom bar text (around line 959), add the signal keybinding hint:

```go
bottomText += "↑↓/jk navigate · h/l collapse/expand · tab focus · s summarize · c signal · f filter · r refresh · 1-9 source · q quit  " + filterStr
```

**Step 4: Build and verify**

```bash
make build
```

Expected: Compiles cleanly.

**Step 5: Commit**

```bash
git add internal/tui/detail.go internal/tui/app.go
git commit -m "feat: display signals in detail pane"
```

---

### Task 7: Handle disconnection for in-flight signal jobs

**Files:**
- Modify: `internal/tui/app.go`

**Step 1: Clear signal jobs on disconnect**

In the `wsDisconnectedMsg` handler (around line 646), add signal cleanup:

```go
case wsDisconnectedMsg:
	m.connected = false
	// Clear any in-flight signal job
	if m.signalActive != nil {
		m.signalErrors[m.signalActive.Source] = "disconnected"
		m.signalActive = nil
	}
	m.signalQueue = nil
	// ... existing summarize fallback code ...
```

**Step 2: Commit**

```bash
git add internal/tui/app.go
git commit -m "feat: handle disconnect for signal jobs"
```

---

### Task 8: Run all tests and verify build

**Step 1: Run all tests**

```bash
GOMAXPROCS=1 go test -p 1 ./... -v
```

Expected: ALL PASS.

**Step 2: Build**

```bash
make build
```

Expected: Clean build.

**Step 3: Commit any fixes if needed**

---

### Task 9: Manual end-to-end test

**Step 1:** Run `tabsordnung --live` with the extension connected.

**Step 2:** Navigate to a Gmail/Slack/Matrix tab in the tree. Verify "Press 'c' to capture signal" appears in the detail pane.

**Step 3:** Press `c`. Verify "Capturing signal..." appears, then results display.

**Step 4:** Press `c` again on same or different signal tab. Verify queue works.

**Step 5:** Check `~/.local/share/tabsordnung/signals/gmail/` for signal files.

**Step 6:** Check `~/.local/share/tabsordnung/signals/signals.md` for the append log.
