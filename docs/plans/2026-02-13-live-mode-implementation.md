# Live Mode & WebExtension Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a live mode that connects to a Firefox WebExtension via WebSocket, enabling real-time tab data and actions (close, focus, move-to-group) from the TUI.

**Architecture:** The TUI starts a WebSocket server. A companion WebExtension connects as a client, sends tab snapshots + incremental updates, and executes commands (close/focus/move). The existing offline session-file mode is preserved, selectable via a numbered source picker.

**Tech Stack:** Go (nhooyr.io/websocket), Bubble Tea, Firefox WebExtension Manifest V3, vanilla JS

---

### Task 1: Add `BrowserID` to Tab type

**Files:**
- Modify: `internal/types/types.go:6-22`

**Step 1: Add the field**

Add `BrowserID int` to the `Tab` struct, after `TabIndex`:

```go
type Tab struct {
	URL          string
	Title        string
	LastAccessed time.Time
	GroupID      string // empty if ungrouped
	Favicon      string
	WindowIndex  int
	TabIndex     int
	BrowserID    int // live Firefox tab ID; 0 in offline mode

	// Analyzer findings (populated after analysis)
	IsStale     bool
	IsDead      bool
	IsDuplicate bool
	DeadReason  string // e.g. "404", "timeout", "dns"
	StaleDays   int
	DuplicateOf []int // indices of duplicate tabs
}
```

**Step 2: Verify build**

Run: `make build`
Expected: compiles cleanly — field is additive, nothing references it yet.

**Step 3: Commit**

```bash
git add internal/types/types.go
git commit -m "types: add BrowserID field to Tab for live mode"
```

---

### Task 2: WebSocket server package

**Files:**
- Create: `internal/server/server.go`
- Create: `internal/server/server_test.go`

**Step 1: Add nhooyr.io/websocket dependency**

Run: `GOMAXPROCS=1 go get -p 1 nhooyr.io/websocket@latest`

**Step 2: Write the failing test**

```go
package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func TestServerAcceptsConnection(t *testing.T) {
	srv := New(0) // port 0 = pick any free port
	msgs := srv.Messages()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	// Send a snapshot message
	snap := IncomingMsg{Type: "snapshot"}
	data, _ := json.Marshal(snap)
	err = conn.Write(ctx, websocket.MessageText, data)
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case msg := <-msgs:
		if msg.Type != "snapshot" {
			t.Errorf("got type %q, want snapshot", msg.Type)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for message")
	}
}

func TestServerSendsCommand(t *testing.T) {
	srv := New(0)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	// Give server a moment to register the connection
	time.Sleep(50 * time.Millisecond)

	// Send command from server side
	cmd := OutgoingMsg{ID: "cmd-1", Action: "close", TabIDs: []int{42}}
	srv.Send(cmd)

	// Read it on the client side
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got OutgoingMsg
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != "cmd-1" || got.Action != "close" {
		t.Errorf("got %+v, want cmd-1/close", got)
	}
}
```

**Step 3: Run test to verify it fails**

Run: `GOMAXPROCS=1 go test -p 1 ./internal/server/ -v`
Expected: FAIL — package does not exist yet.

**Step 4: Write the implementation**

```go
package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"nhooyr.io/websocket"
)

// IncomingMsg is a message from the extension to the TUI.
type IncomingMsg struct {
	Type    string          `json:"type"`
	Tab     json.RawMessage `json:"tab,omitempty"`
	Tabs    json.RawMessage `json:"tabs,omitempty"`
	Groups  json.RawMessage `json:"groups,omitempty"`
	TabID   int             `json:"tabId,omitempty"`
	Group   json.RawMessage `json:"group,omitempty"`
	// Command response fields
	ID    string `json:"id,omitempty"`
	OK    *bool  `json:"ok,omitempty"`
	Error string `json:"error,omitempty"`
}

// OutgoingMsg is a command from the TUI to the extension.
type OutgoingMsg struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	TabID   int    `json:"tabId,omitempty"`
	TabIDs  []int  `json:"tabIds,omitempty"`
	GroupID int    `json:"groupId,omitempty"`
}

// Server manages the WebSocket connection to the extension.
type Server struct {
	port     int
	msgs     chan IncomingMsg
	mu       sync.Mutex
	conn     *websocket.Conn
	connCtx  context.Context
}

// New creates a new Server. Port 0 means the caller manages the listener.
func New(port int) *Server {
	return &Server{
		port: port,
		msgs: make(chan IncomingMsg, 64),
	}
}

// Messages returns the channel of incoming messages from the extension.
func (s *Server) Messages() <-chan IncomingMsg {
	return s.msgs
}

// Connected reports whether an extension is connected.
func (s *Server) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn != nil
}

// Send sends a command to the connected extension.
func (s *Server) Send(msg OutgoingMsg) error {
	s.mu.Lock()
	conn := s.conn
	ctx := s.connCtx
	s.mu.Unlock()

	if conn == nil {
		return nil
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

// Handler returns an http.Handler that accepts WebSocket upgrades.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			log.Printf("websocket accept: %v", err)
			return
		}

		ctx := r.Context()
		s.mu.Lock()
		// Close any existing connection
		if s.conn != nil {
			s.conn.CloseNow()
		}
		s.conn = conn
		s.connCtx = ctx
		s.mu.Unlock()

		defer func() {
			s.mu.Lock()
			if s.conn == conn {
				s.conn = nil
				s.connCtx = nil
			}
			s.mu.Unlock()
			conn.CloseNow()
		}()

		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var msg IncomingMsg
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			select {
			case s.msgs <- msg:
			default:
				// Drop message if channel is full
			}
		}
	})
}

// ListenAndServe starts the WebSocket server on the configured port.
func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.Handle("/", s.Handler())

	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	return srv.ListenAndServe()
}
```

Note: `ListenAndServe` uses `fmt.Sprintf` — add `"fmt"` to the import list.

**Step 5: Run tests**

Run: `GOMAXPROCS=1 go test -p 1 ./internal/server/ -v`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/server/ go.mod go.sum
git commit -m "server: add WebSocket server for extension communication"
```

---

### Task 3: Wire WebSocket messages into Bubble Tea

**Files:**
- Modify: `internal/tui/app.go`

This task adds the message types and plumbing so the TUI can receive WebSocket messages as `tea.Msg` values. No UI changes yet.

**Step 1: Add new message types and mode enum to app.go**

Add after the existing `analysisCompleteMsg` type:

```go
// SourceMode distinguishes live vs offline.
type SourceMode int

const (
	ModeOffline SourceMode = iota
	ModeLive
)

// Messages from the WebSocket server
type wsConnectedMsg struct{}
type wsDisconnectedMsg struct{}
type wsSnapshotMsg struct {
	data *types.SessionData
}
type wsTabCreatedMsg struct{ tab *types.Tab }
type wsTabRemovedMsg struct{ tabID int }
type wsTabUpdatedMsg struct{ tab *types.Tab }
type wsCmdResponseMsg struct {
	id    string
	ok    bool
	error string
}
```

**Step 2: Add new fields to Model**

Add to the `Model` struct:

```go
	// Live mode
	mode      SourceMode
	server    *server.Server
	connected bool
	selected  map[int]bool // BrowserID -> selected (live mode)
```

**Step 3: Add a command that listens on the server messages channel and converts to tea.Msg**

```go
func listenWebSocket(srv *server.Server) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-srv.Messages()
		if !ok {
			return wsDisconnectedMsg{}
		}
		switch msg.Type {
		case "snapshot":
			data, err := parseSnapshot(msg)
			if err != nil {
				return sessionLoadedMsg{err: err}
			}
			return wsSnapshotMsg{data: data}
		// Additional message types will be handled in later tasks
		}
		return nil
	}
}
```

Note: `parseSnapshot` will be implemented in Task 5 when we build the full snapshot parsing. For now, add a stub that returns an error:

```go
func parseSnapshot(msg server.IncomingMsg) (*types.SessionData, error) {
	return nil, fmt.Errorf("snapshot parsing not implemented")
}
```

**Step 4: Verify build**

Run: `make build`
Expected: compiles — new types exist but nothing references them in Update yet.

**Step 5: Commit**

```bash
git add internal/tui/app.go
git commit -m "tui: add live mode message types and model fields"
```

---

### Task 4: Source picker (replaces profile picker)

**Files:**
- Create: `internal/tui/source_picker.go`
- Modify: `internal/tui/app.go`

**Step 1: Write source_picker.go**

```go
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// Source represents a selectable data source.
type Source struct {
	Label   string
	Profile *types.Profile // nil for live mode
	IsLive  bool
}

// SourcePicker is an overlay for selecting live mode or a profile.
type SourcePicker struct {
	Sources []Source
	Cursor  int
	Width   int
	Height  int
}

func NewSourcePicker(profiles []types.Profile) SourcePicker {
	sources := []Source{
		{Label: "Live (connected)", IsLive: true},
	}
	for i := range profiles {
		sources = append(sources, Source{
			Label:   profiles[i].Name,
			Profile: &profiles[i],
		})
	}
	return SourcePicker{Sources: sources, Cursor: 0}
}

func (m *SourcePicker) MoveUp() {
	if m.Cursor > 0 {
		m.Cursor--
	}
}

func (m *SourcePicker) MoveDown() {
	if m.Cursor < len(m.Sources)-1 {
		m.Cursor++
	}
}

func (m SourcePicker) Selected() Source {
	return m.Sources[m.Cursor]
}

// SelectByNumber selects a source by its 1-based number. Returns false if out of range.
func (m *SourcePicker) SelectByNumber(n int) bool {
	idx := n - 1
	if idx >= 0 && idx < len(m.Sources) {
		m.Cursor = idx
		return true
	}
	return false
}

func (m SourcePicker) View() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Padding(0, 1)
	selectedStyle := lipgloss.NewStyle().Bold(true).Reverse(true).Padding(0, 1)
	normalStyle := lipgloss.NewStyle().Padding(0, 1)
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(1, 2)

	var b strings.Builder
	b.WriteString(titleStyle.Render("Select source:") + "\n\n")

	for i, src := range m.Sources {
		num := i + 1
		label := fmt.Sprintf("%d  %s", num, src.Label)
		if src.Profile != nil && src.Profile.IsDefault {
			label += " (default)"
		}
		if i == m.Cursor {
			label = selectedStyle.Render(fmt.Sprintf("%d  %s", num, src.Label))
		} else {
			label = normalStyle.Render("  " + label)
		}
		b.WriteString(label + "\n")
	}

	b.WriteString("\n" + normalStyle.Render("↑↓ navigate · enter select · 1-9 quick select"))

	return boxStyle.Render(b.String())
}
```

**Step 2: Update Model to use SourcePicker instead of ProfilePicker**

In `app.go`, replace `picker ProfilePicker` with `picker SourcePicker` in the Model struct. Update `NewModel` to create a `SourcePicker`:

```go
func NewModel(profiles []types.Profile, staleDays int, liveMode bool) Model {
	m := Model{
		profiles:  profiles,
		staleDays: staleDays,
		selected:  make(map[int]bool),
	}
	if liveMode {
		m.mode = ModeLive
		m.loading = true
	} else if len(profiles) == 1 {
		m.mode = ModeOffline
		m.loading = true
	} else {
		m.showPicker = true
		m.picker = NewSourcePicker(profiles)
	}
	return m
}
```

Update `Init()`:

```go
func (m Model) Init() tea.Cmd {
	if m.mode == ModeLive {
		return m.startLiveMode()
	}
	if len(m.profiles) == 1 {
		return loadSession(m.profiles[0])
	}
	return nil
}

func (m Model) startLiveMode() tea.Cmd {
	// Will be implemented in Task 6 when we wire up the server
	return nil
}
```

Update the picker key handling in `Update` to use `SourcePicker` and handle number keys + live mode selection:

```go
// In the picker section of Update:
if m.showPicker {
	switch msg.String() {
	case "up", "k":
		m.picker.MoveUp()
	case "down", "j":
		m.picker.MoveDown()
	case "enter":
		src := m.picker.Selected()
		m.showPicker = false
		m.loading = true
		if src.IsLive {
			m.mode = ModeLive
			return m, m.startLiveMode()
		}
		m.mode = ModeOffline
		m.profile = *src.Profile
		return m, loadSession(m.profile)
	case "esc":
		if m.session != nil {
			m.showPicker = false
		}
	case "q", "ctrl+c":
		return m, tea.Quit
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		n := int(msg.String()[0] - '0')
		if m.picker.SelectByNumber(n) {
			src := m.picker.Selected()
			m.showPicker = false
			m.loading = true
			if src.IsLive {
				m.mode = ModeLive
				return m, m.startLiveMode()
			}
			m.mode = ModeOffline
			m.profile = *src.Profile
			return m, loadSession(m.profile)
		}
	}
	return m, nil
}
```

Replace the `"p"` keybinding in the main key handler:

```go
// Replace: case "p": m.showPicker = true; m.picker = NewProfilePicker(m.profiles)
// With number keys for source switching:
case "1", "2", "3", "4", "5", "6", "7", "8", "9":
	m.showPicker = true
	m.picker = NewSourcePicker(m.profiles)
	n := int(msg.String()[0] - '0')
	m.picker.SelectByNumber(n)
```

**Step 3: Update main.go to pass `liveMode` flag**

```go
liveMode := flag.Bool("live", false, "Start in live mode (connect to extension)")
port := flag.Int("port", 19191, "WebSocket port for live mode")
```

Pass to `NewModel`:

```go
model := tui.NewModel(profiles, *staleDays, *liveMode)
```

(The `port` flag will be wired in Task 6.)

**Step 4: Update top bar in View() to show mode**

Replace the `profileStr` line:

```go
var profileStr string
if m.mode == ModeLive {
	if m.connected {
		profileStr = "Live ● connected"
	} else {
		profileStr = "Live ○ waiting..."
	}
} else {
	profileStr = fmt.Sprintf("Profile: %s (offline)", m.profile.Name)
}
```

**Step 5: Verify build**

Run: `make build`
Expected: compiles. The old `profile_picker.go` can remain for now (it's unused but doesn't cause errors). Delete it if the compiler complains about unused types.

**Step 6: Commit**

```bash
git add internal/tui/source_picker.go internal/tui/app.go main.go
git commit -m "tui: replace profile picker with source picker, add --live flag"
```

---

### Task 5: Snapshot parsing from WebSocket JSON

**Files:**
- Create: `internal/server/parse.go`
- Create: `internal/server/parse_test.go`

**Step 1: Write the failing test**

```go
package server

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseSnapshot(t *testing.T) {
	snapshot := `{
		"type": "snapshot",
		"tabs": [
			{"id": 1, "url": "https://example.com", "title": "Example", "lastAccessed": 1700000000000, "groupId": 5, "windowId": 1, "index": 0},
			{"id": 2, "url": "https://other.com", "title": "Other", "lastAccessed": 1700000060000, "groupId": -1, "windowId": 1, "index": 1}
		],
		"groups": [
			{"id": 5, "title": "Work", "color": "blue", "collapsed": false}
		]
	}`

	var msg IncomingMsg
	if err := json.Unmarshal([]byte(snapshot), &msg); err != nil {
		t.Fatal(err)
	}

	data, err := ParseSnapshot(msg)
	if err != nil {
		t.Fatal(err)
	}

	if len(data.AllTabs) != 2 {
		t.Errorf("got %d tabs, want 2", len(data.AllTabs))
	}
	if data.AllTabs[0].BrowserID != 1 {
		t.Errorf("tab BrowserID = %d, want 1", data.AllTabs[0].BrowserID)
	}
	if data.AllTabs[0].URL != "https://example.com" {
		t.Errorf("tab URL = %q", data.AllTabs[0].URL)
	}
	if data.AllTabs[0].LastAccessed.IsZero() {
		t.Error("tab LastAccessed is zero")
	}

	// Should have 2 groups: "Work" + "Ungrouped"
	if len(data.Groups) != 2 {
		t.Errorf("got %d groups, want 2", len(data.Groups))
	}

	// Work group should have 1 tab
	var workGroup *types.TabGroup
	for _, g := range data.Groups {
		if g.Name == "Work" {
			workGroup = g
		}
	}
	if workGroup == nil {
		t.Fatal("Work group not found")
	}
	if len(workGroup.Tabs) != 1 {
		t.Errorf("Work group has %d tabs, want 1", len(workGroup.Tabs))
	}
}

func TestParseSnapshotNoGroups(t *testing.T) {
	snapshot := `{
		"type": "snapshot",
		"tabs": [
			{"id": 1, "url": "https://example.com", "title": "Example", "lastAccessed": 1700000000000, "groupId": -1, "windowId": 1, "index": 0}
		],
		"groups": []
	}`

	var msg IncomingMsg
	json.Unmarshal([]byte(snapshot), &msg)
	data, err := ParseSnapshot(msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Groups) != 1 || data.Groups[0].Name != "Ungrouped" {
		t.Errorf("expected single Ungrouped group, got %+v", data.Groups)
	}
}
```

Add `"time"` and the types import. Note: you'll need `"github.com/nickel-chromium/tabsordnung/internal/types"`.

**Step 2: Run test to verify it fails**

Run: `GOMAXPROCS=1 go test -p 1 ./internal/server/ -run TestParseSnapshot -v`
Expected: FAIL — `ParseSnapshot` not defined.

**Step 3: Write the implementation**

```go
package server

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// wireTab is the JSON shape sent by the extension.
type wireTab struct {
	ID           int    `json:"id"`
	URL          string `json:"url"`
	Title        string `json:"title"`
	LastAccessed int64  `json:"lastAccessed"` // Unix ms
	GroupID      int    `json:"groupId"`       // -1 if ungrouped
	WindowID     int    `json:"windowId"`
	Index        int    `json:"index"`
	FavIconURL   string `json:"favIconUrl"`
}

// wireGroup is the JSON shape sent by the extension.
type wireGroup struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	Color     string `json:"color"`
	Collapsed bool   `json:"collapsed"`
}

// ParseSnapshot converts a snapshot IncomingMsg into a SessionData.
func ParseSnapshot(msg IncomingMsg) (*types.SessionData, error) {
	var tabs []wireTab
	if err := json.Unmarshal(msg.Tabs, &tabs); err != nil {
		return nil, fmt.Errorf("parse tabs: %w", err)
	}
	var groups []wireGroup
	if err := json.Unmarshal(msg.Groups, &groups); err != nil {
		return nil, fmt.Errorf("parse groups: %w", err)
	}

	// Build group map
	groupMap := make(map[int]*types.TabGroup)
	var result []*types.TabGroup
	for _, g := range groups {
		tg := &types.TabGroup{
			ID:        strconv.Itoa(g.ID),
			Name:      g.Title,
			Color:     g.Color,
			Collapsed: g.Collapsed,
		}
		groupMap[g.ID] = tg
		result = append(result, tg)
	}

	// Ungrouped bucket
	ungrouped := &types.TabGroup{ID: "ungrouped", Name: "Ungrouped"}

	var allTabs []*types.Tab
	for _, wt := range tabs {
		tab := &types.Tab{
			BrowserID:    wt.ID,
			URL:          wt.URL,
			Title:        wt.Title,
			LastAccessed: time.UnixMilli(wt.LastAccessed),
			GroupID:      strconv.Itoa(wt.GroupID),
			Favicon:      wt.FavIconURL,
			WindowIndex:  wt.WindowID,
			TabIndex:     wt.Index,
		}
		allTabs = append(allTabs, tab)

		if g, ok := groupMap[wt.GroupID]; ok {
			g.Tabs = append(g.Tabs, tab)
		} else {
			tab.GroupID = ""
			ungrouped.Tabs = append(ungrouped.Tabs, tab)
		}
	}

	if len(ungrouped.Tabs) > 0 {
		result = append(result, ungrouped)
	}

	return &types.SessionData{
		Groups:   result,
		AllTabs:  allTabs,
		ParsedAt: time.Now(),
	}, nil
}
```

**Step 4: Update the stub in app.go**

Replace the `parseSnapshot` stub in `app.go`:

```go
// In listenWebSocket, change:
//   data, err := parseSnapshot(msg)
// to:
//   data, err := server.ParseSnapshot(msg)
```

Remove the old `parseSnapshot` stub function.

**Step 5: Run tests**

Run: `GOMAXPROCS=1 go test -p 1 ./internal/server/ -v`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/server/parse.go internal/server/parse_test.go internal/tui/app.go
git commit -m "server: add snapshot parsing from extension JSON to SessionData"
```

---

### Task 6: Wire WebSocket server into the TUI lifecycle

**Files:**
- Modify: `internal/tui/app.go`
- Modify: `main.go`

This task connects the WebSocket server to the Bubble Tea event loop. When in live mode, the TUI starts the server and processes snapshot messages.

**Step 1: Pass server and port into Model**

Update `NewModel` signature:

```go
func NewModel(profiles []types.Profile, staleDays int, liveMode bool, srv *server.Server) Model {
```

Store `srv` in the `server` field (add `server *server.Server` to Model — already declared in Task 3).

**Step 2: Implement startLiveMode**

Replace the stub:

```go
func (m Model) startLiveMode() tea.Cmd {
	return tea.Batch(
		listenWebSocket(m.server),
		startWSServer(m.server),
	)
}

func startWSServer(srv *server.Server) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		srv.ListenAndServe(ctx)
		return wsDisconnectedMsg{}
	}
}
```

**Step 3: Handle wsSnapshotMsg in Update**

Add a case in the `Update` switch:

```go
case wsSnapshotMsg:
	m.loading = false
	m.connected = true
	m.session = msg.data

	analyzer.AnalyzeStale(m.session.AllTabs, m.staleDays)
	analyzer.AnalyzeDuplicates(m.session.AllTabs)
	m.stats = analyzer.ComputeStats(m.session)

	m.tree = NewTreeModel(m.session.Groups)
	m.tree.Width = m.width * 60 / 100
	m.tree.Height = m.height - 5

	m.deadChecking = true
	return m, tea.Batch(
		runDeadLinkChecks(m.session.AllTabs),
		listenWebSocket(m.server),
	)

case wsDisconnectedMsg:
	m.connected = false
	return m, nil
```

Note: after processing a snapshot, we call `listenWebSocket` again to keep listening for the next message.

**Step 4: Update main.go to create the server**

```go
import "github.com/nickel-chromium/tabsordnung/internal/server"

// After flag parsing:
var srv *server.Server
if *liveMode {
	srv = server.New(*port)
}

model := tui.NewModel(profiles, *staleDays, *liveMode, srv)
```

**Step 5: Verify build and manual test**

Run: `make build`
Expected: compiles.

Run: `./tabsordnung --live`
Expected: Shows "Live ○ waiting..." in the top bar. No crash.

**Step 6: Commit**

```bash
git add internal/tui/app.go main.go
git commit -m "tui: wire WebSocket server into Bubble Tea event loop"
```

---

### Task 7: WebExtension — manifest and connection

**Files:**
- Create: `extension/manifest.json`
- Create: `extension/background.js`

**Step 1: Create manifest.json**

```json
{
  "manifest_version": 3,
  "name": "Tabsordnung Companion",
  "version": "0.1.0",
  "description": "Connects Firefox tabs to the Tabsordnung TUI",
  "permissions": ["tabs"],
  "background": {
    "scripts": ["background.js"]
  }
}
```

Note: `tabGroups` permission omitted for now — Firefox doesn't support it yet in all versions. We'll handle groups via `browser.tabs.query` which includes `groupId` where available.

**Step 2: Write background.js with connection, snapshot, and event forwarding**

```js
const PORT = 19191;
const RECONNECT_BASE_MS = 1000;
const RECONNECT_MAX_MS = 30000;

let ws = null;
let reconnectDelay = RECONNECT_BASE_MS;

function connect() {
  ws = new WebSocket(`ws://127.0.0.1:${PORT}`);

  ws.addEventListener("open", async () => {
    console.log("Tabsordnung: connected");
    reconnectDelay = RECONNECT_BASE_MS;
    await sendSnapshot();
  });

  ws.addEventListener("message", (event) => {
    handleCommand(JSON.parse(event.data));
  });

  ws.addEventListener("close", () => {
    console.log("Tabsordnung: disconnected, reconnecting...");
    ws = null;
    scheduleReconnect();
  });

  ws.addEventListener("error", () => {
    ws?.close();
  });
}

function scheduleReconnect() {
  setTimeout(() => {
    connect();
    reconnectDelay = Math.min(reconnectDelay * 2, RECONNECT_MAX_MS);
  }, reconnectDelay);
}

function send(obj) {
  if (ws?.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify(obj));
  }
}

// --- Snapshot ---

async function sendSnapshot() {
  const tabs = await browser.tabs.query({});
  // tabGroups API may not be available
  let groups = [];
  if (browser.tabGroups?.query) {
    groups = await browser.tabGroups.query({});
  }

  send({
    type: "snapshot",
    tabs: tabs.map(serializeTab),
    groups: groups.map(serializeGroup),
  });
}

function serializeTab(tab) {
  return {
    id: tab.id,
    url: tab.url || "",
    title: tab.title || "",
    lastAccessed: tab.lastAccessed || 0,
    groupId: tab.groupId ?? -1,
    windowId: tab.windowId,
    index: tab.index,
    favIconUrl: tab.favIconUrl || "",
  };
}

function serializeGroup(group) {
  return {
    id: group.id,
    title: group.title || "",
    color: group.color || "",
    collapsed: group.collapsed || false,
  };
}

// --- Events ---

browser.tabs.onCreated.addListener((tab) => {
  send({ type: "tab.created", tab: serializeTab(tab) });
});

browser.tabs.onRemoved.addListener((tabId) => {
  send({ type: "tab.removed", tabId });
});

browser.tabs.onUpdated.addListener((_tabId, _changeInfo, tab) => {
  send({ type: "tab.updated", tab: serializeTab(tab) });
});

browser.tabs.onMoved.addListener(async (tabId) => {
  const tab = await browser.tabs.get(tabId);
  send({ type: "tab.moved", tab: serializeTab(tab) });
});

// --- Commands ---

async function handleCommand(msg) {
  try {
    switch (msg.action) {
      case "close":
        await browser.tabs.remove(msg.tabIds);
        break;
      case "focus":
        await browser.tabs.update(msg.tabId, { active: true });
        const tab = await browser.tabs.get(msg.tabId);
        await browser.windows.update(tab.windowId, { focused: true });
        break;
      case "move":
        if (browser.tabs.group) {
          await browser.tabs.group({ tabIds: msg.tabIds, groupId: msg.groupId });
        }
        break;
      default:
        send({ id: msg.id, ok: false, error: `unknown action: ${msg.action}` });
        return;
    }
    send({ id: msg.id, ok: true });
  } catch (e) {
    send({ id: msg.id, ok: false, error: e.message });
  }
}

// --- Start ---

connect();
```

**Step 3: Test manually**

1. Run `./tabsordnung --live`
2. Open Firefox, go to `about:debugging#/runtime/this-firefox`
3. Load Temporary Add-on → select `extension/manifest.json`
4. The TUI should switch from "waiting..." to "connected" and show tabs.

**Step 4: Commit**

```bash
git add extension/
git commit -m "extension: add companion WebExtension with snapshot, events, and commands"
```

---

### Task 8: Tab actions — close and focus

**Files:**
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/tree.go`

**Step 1: Add a command ID counter and send helper to Model**

In `app.go`:

```go
var cmdCounter int

func nextCmdID() string {
	cmdCounter++
	return fmt.Sprintf("cmd-%d", cmdCounter)
}

func sendCmd(srv *server.Server, msg server.OutgoingMsg) tea.Cmd {
	return func() tea.Msg {
		msg.ID = nextCmdID()
		srv.Send(msg)
		return nil
	}
}
```

**Step 2: Handle `x` key (close) in Update**

In the main key handler (not picker), add:

```go
case "x":
	if m.mode != ModeLive || !m.connected {
		return m, nil
	}
	ids := m.selectedOrCurrentTabIDs()
	if len(ids) == 0 {
		return m, nil
	}
	return m, sendCmd(m.server, server.OutgoingMsg{
		Action: "close",
		TabIDs: ids,
	})
```

**Step 3: Handle `enter` key change for live mode**

Replace the existing `"enter"` case:

```go
case "enter":
	if m.mode == ModeLive && m.connected {
		node := m.tree.SelectedNode()
		if node != nil && node.Tab != nil {
			return m, sendCmd(m.server, server.OutgoingMsg{
				Action: "focus",
				TabID:  node.Tab.BrowserID,
			})
		}
	}
	m.tree.Toggle()
```

**Step 4: Add `selectedOrCurrentTabIDs` helper**

```go
func (m *Model) selectedOrCurrentTabIDs() []int {
	if len(m.selected) > 0 {
		ids := make([]int, 0, len(m.selected))
		for id := range m.selected {
			ids = append(ids, id)
		}
		return ids
	}
	node := m.tree.SelectedNode()
	if node != nil && node.Tab != nil && node.Tab.BrowserID != 0 {
		return []int{node.Tab.BrowserID}
	}
	return nil
}
```

**Step 5: Handle `space` key for selection toggle**

```go
case " ":
	if m.mode != ModeLive || !m.connected {
		return m, nil
	}
	node := m.tree.SelectedNode()
	if node != nil && node.Tab != nil && node.Tab.BrowserID != 0 {
		id := node.Tab.BrowserID
		if m.selected[id] {
			delete(m.selected, id)
		} else {
			m.selected[id] = true
		}
	}
	m.tree.MoveDown()
```

**Step 6: Handle `esc` to clear selection**

Add to the main key handler:

```go
case "esc":
	m.selected = make(map[int]bool)
```

**Step 7: Show selection markers in tree.go**

In `tree.go` `View()`, the tree needs access to the selected set. Add a `Selected` field to `TreeModel`:

```go
type TreeModel struct {
	// ... existing fields
	Selected map[int]bool // BrowserID -> selected
}
```

In `NewTreeModel`, initialize it:

```go
func NewTreeModel(groups []*types.TabGroup) TreeModel {
	// ... existing code
	return TreeModel{
		Groups:   groups,
		Expanded: expanded,
		Selected: make(map[int]bool),
	}
}
```

In `app.go`, sync the selected map to the tree before rendering. In `View()`, before rendering the tree, add:

```go
m.tree.Selected = m.selected
```

Wait — `View()` is a value receiver so this won't persist, but that's fine since we only need it for rendering.

In `tree.go` `View()`, add selection marker before the tab prefix:

```go
} else if node.Tab != nil {
	prefix := "  "
	if m.Selected[node.Tab.BrowserID] {
		prefix = "▸ "
	}
	// ... rest of tab rendering
```

**Step 8: Update bottom bar to show selection count and available actions**

In `app.go` `View()`, update the bottom bar:

```go
var bottomText string
if m.mode == ModeLive && m.connected {
	selCount := len(m.selected)
	if selCount > 0 {
		bottomText = fmt.Sprintf("%d selected · x close · g move · esc clear · ", selCount)
	}
	bottomText += "space select · enter focus · "
}
bottomText += "↑↓/jk navigate · h/l collapse/expand · f filter · r refresh · 1-9 source · q quit  " + filterStr
bottomBar := bottomBarStyle.Render(bottomText)
```

**Step 9: Verify build**

Run: `make build`
Expected: compiles.

**Step 10: Commit**

```bash
git add internal/tui/app.go internal/tui/tree.go
git commit -m "tui: add close, focus, and selection keybindings for live mode"
```

---

### Task 9: Move-to-group action with group picker

**Files:**
- Create: `internal/tui/group_picker.go`
- Modify: `internal/tui/app.go`

**Step 1: Write group_picker.go**

```go
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// GroupPicker is an overlay for selecting a destination tab group.
type GroupPicker struct {
	Groups []*types.TabGroup
	Cursor int
	Width  int
	Height int
}

func NewGroupPicker(groups []*types.TabGroup) GroupPicker {
	return GroupPicker{Groups: groups}
}

func (m *GroupPicker) MoveUp() {
	if m.Cursor > 0 {
		m.Cursor--
	}
}

func (m *GroupPicker) MoveDown() {
	if m.Cursor < len(m.Groups)-1 {
		m.Cursor++
	}
}

func (m GroupPicker) Selected() *types.TabGroup {
	if m.Cursor >= 0 && m.Cursor < len(m.Groups) {
		return m.Groups[m.Cursor]
	}
	return nil
}

func (m GroupPicker) View() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Padding(0, 1)
	selectedStyle := lipgloss.NewStyle().Bold(true).Reverse(true).Padding(0, 1)
	normalStyle := lipgloss.NewStyle().Padding(0, 1)
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(1, 2)

	var b strings.Builder
	b.WriteString(titleStyle.Render("Move to group:") + "\n\n")

	for i, g := range m.Groups {
		label := fmt.Sprintf("%s (%d tabs)", g.Name, len(g.Tabs))
		if i == m.Cursor {
			label = selectedStyle.Render(label)
		} else {
			label = normalStyle.Render("  " + label)
		}
		b.WriteString(label + "\n")
	}

	b.WriteString("\n" + normalStyle.Render("↑↓ navigate · enter confirm · esc cancel"))

	return boxStyle.Render(b.String())
}
```

**Step 2: Add group picker state to Model**

In `app.go`, add to Model:

```go
	groupPicker    GroupPicker
	showGroupPicker bool
```

**Step 3: Handle `g` key to open group picker**

In the main key handler:

```go
case "g":
	if m.mode != ModeLive || !m.connected || m.session == nil {
		return m, nil
	}
	ids := m.selectedOrCurrentTabIDs()
	if len(ids) == 0 {
		return m, nil
	}
	m.showGroupPicker = true
	m.groupPicker = NewGroupPicker(m.session.Groups)
	m.groupPicker.Width = m.width
	m.groupPicker.Height = m.height
```

**Step 4: Handle group picker input in Update**

Add a new block at the top of the `tea.KeyMsg` handler, before the source picker block:

```go
if m.showGroupPicker {
	switch msg.String() {
	case "up", "k":
		m.groupPicker.MoveUp()
	case "down", "j":
		m.groupPicker.MoveDown()
	case "enter":
		group := m.groupPicker.Selected()
		if group != nil {
			ids := m.selectedOrCurrentTabIDs()
			groupID, _ := strconv.Atoi(group.ID)
			m.showGroupPicker = false
			m.selected = make(map[int]bool)
			return m, sendCmd(m.server, server.OutgoingMsg{
				Action:  "move",
				TabIDs:  ids,
				GroupID: groupID,
			})
		}
	case "esc":
		m.showGroupPicker = false
	case "q", "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}
```

**Step 5: Render group picker overlay in View**

In `View()`, after the source picker check:

```go
if m.showGroupPicker {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.groupPicker.View())
}
```

**Step 6: Verify build**

Run: `make build`
Expected: compiles. Add `"strconv"` import to `app.go`.

**Step 7: Commit**

```bash
git add internal/tui/group_picker.go internal/tui/app.go
git commit -m "tui: add move-to-group action with group picker overlay"
```

---

### Task 10: Handle incremental updates from extension

**Files:**
- Modify: `internal/tui/app.go`
- Modify: `internal/server/parse.go`

**Step 1: Add ParseTab helper in parse.go**

```go
// ParseTab converts a single tab JSON message into a Tab.
func ParseTab(raw json.RawMessage) (*types.Tab, error) {
	var wt wireTab
	if err := json.Unmarshal(raw, &wt); err != nil {
		return nil, err
	}
	return &types.Tab{
		BrowserID:    wt.ID,
		URL:          wt.URL,
		Title:        wt.Title,
		LastAccessed: time.UnixMilli(wt.LastAccessed),
		GroupID:      strconv.Itoa(wt.GroupID),
		Favicon:      wt.FavIconURL,
		WindowIndex:  wt.WindowID,
		TabIndex:     wt.Index,
	}, nil
}
```

**Step 2: Expand listenWebSocket to handle all message types**

In `app.go`, update the `listenWebSocket` function:

```go
func listenWebSocket(srv *server.Server) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-srv.Messages()
		if !ok {
			return wsDisconnectedMsg{}
		}
		switch msg.Type {
		case "snapshot":
			data, err := server.ParseSnapshot(msg)
			if err != nil {
				return sessionLoadedMsg{err: err}
			}
			return wsSnapshotMsg{data: data}
		case "tab.created":
			tab, err := server.ParseTab(msg.Tab)
			if err != nil {
				return nil
			}
			return wsTabCreatedMsg{tab: tab}
		case "tab.removed":
			return wsTabRemovedMsg{tabID: msg.TabID}
		case "tab.updated", "tab.moved":
			tab, err := server.ParseTab(msg.Tab)
			if err != nil {
				return nil
			}
			return wsTabUpdatedMsg{tab: tab}
		default:
			// Could be a command response — check for id field
			if msg.ID != "" && msg.OK != nil {
				errStr := msg.Error
				return wsCmdResponseMsg{id: msg.ID, ok: *msg.OK, error: errStr}
			}
			return nil
		}
	}
}
```

**Step 3: Handle incremental updates in Update**

```go
case wsTabRemovedMsg:
	if m.session != nil {
		m.removeTab(msg.tabID)
		analyzer.AnalyzeDuplicates(m.session.AllTabs)
		m.stats = analyzer.ComputeStats(m.session)
		m.tree = NewTreeModel(m.session.Groups)
		m.tree.Width = m.width * 60 / 100
		m.tree.Height = m.height - 5
	}
	return m, listenWebSocket(m.server)

case wsTabCreatedMsg:
	if m.session != nil {
		m.addTab(msg.tab)
		analyzer.AnalyzeStale(m.session.AllTabs, m.staleDays)
		analyzer.AnalyzeDuplicates(m.session.AllTabs)
		m.stats = analyzer.ComputeStats(m.session)
		m.tree = NewTreeModel(m.session.Groups)
		m.tree.Width = m.width * 60 / 100
		m.tree.Height = m.height - 5
	}
	return m, listenWebSocket(m.server)

case wsTabUpdatedMsg:
	if m.session != nil {
		m.updateTab(msg.tab)
		analyzer.AnalyzeStale(m.session.AllTabs, m.staleDays)
		analyzer.AnalyzeDuplicates(m.session.AllTabs)
		m.stats = analyzer.ComputeStats(m.session)
		m.tree = NewTreeModel(m.session.Groups)
		m.tree.Width = m.width * 60 / 100
		m.tree.Height = m.height - 5
	}
	return m, listenWebSocket(m.server)

case wsCmdResponseMsg:
	// Log or display errors if needed. For now, just keep listening.
	return m, listenWebSocket(m.server)
```

**Step 4: Implement helper methods on Model**

```go
func (m *Model) removeTab(browserID int) {
	for _, g := range m.session.Groups {
		for i, t := range g.Tabs {
			if t.BrowserID == browserID {
				g.Tabs = append(g.Tabs[:i], g.Tabs[i+1:]...)
				break
			}
		}
	}
	for i, t := range m.session.AllTabs {
		if t.BrowserID == browserID {
			m.session.AllTabs = append(m.session.AllTabs[:i], m.session.AllTabs[i+1:]...)
			break
		}
	}
	delete(m.selected, browserID)
}

func (m *Model) addTab(tab *types.Tab) {
	m.session.AllTabs = append(m.session.AllTabs, tab)
	// Add to appropriate group or Ungrouped
	placed := false
	for _, g := range m.session.Groups {
		if g.ID == tab.GroupID {
			g.Tabs = append(g.Tabs, tab)
			placed = true
			break
		}
	}
	if !placed {
		// Find or create Ungrouped
		for _, g := range m.session.Groups {
			if g.ID == "ungrouped" {
				g.Tabs = append(g.Tabs, tab)
				placed = true
				break
			}
		}
		if !placed {
			ug := &types.TabGroup{ID: "ungrouped", Name: "Ungrouped", Tabs: []*types.Tab{tab}}
			m.session.Groups = append(m.session.Groups, ug)
		}
	}
}

func (m *Model) updateTab(tab *types.Tab) {
	for _, t := range m.session.AllTabs {
		if t.BrowserID == tab.BrowserID {
			t.URL = tab.URL
			t.Title = tab.Title
			t.LastAccessed = tab.LastAccessed
			t.Favicon = tab.Favicon
			t.TabIndex = tab.TabIndex
			// If group changed, move it
			if t.GroupID != tab.GroupID {
				m.removeTab(tab.BrowserID)
				m.session.AllTabs = append(m.session.AllTabs, tab)
				m.addTab(tab)
			}
			return
		}
	}
	// Tab not found — treat as new
	m.addTab(tab)
}
```

**Step 5: Verify build**

Run: `make build`
Expected: compiles.

**Step 6: Run all tests**

Run: `make test`
Expected: all existing tests still pass.

**Step 7: Commit**

```bash
git add internal/tui/app.go internal/server/parse.go
git commit -m "tui: handle incremental tab updates from extension"
```

---

### Task 11: Delete old profile_picker.go

**Files:**
- Delete: `internal/tui/profile_picker.go`

**Step 1: Remove the file**

```bash
rm internal/tui/profile_picker.go
```

**Step 2: Verify build**

Run: `make build`
Expected: compiles. If anything still references `ProfilePicker` or `NewProfilePicker`, update those references to use `SourcePicker`.

**Step 3: Run tests**

Run: `make test`
Expected: PASS

**Step 4: Commit**

```bash
git add -A
git commit -m "tui: remove old profile_picker.go, replaced by source_picker.go"
```

---

### Task 12: End-to-end manual test

This task has no code changes — it's a verification checklist.

**Step 1: Build**

Run: `make build`

**Step 2: Test offline mode**

Run: `./tabsordnung`
Expected: Source picker shows "1 Live (connected)" + profiles. Selecting a profile works as before.

**Step 3: Test live mode startup**

Run: `./tabsordnung --live`
Expected: Shows "Live ○ waiting..." in top bar.

**Step 4: Test extension connection**

1. Open Firefox → `about:debugging#/runtime/this-firefox`
2. Load Temporary Add-on → `extension/manifest.json`
3. TUI should switch to "Live ● connected" and display tabs.

**Step 5: Test actions**

- Navigate to a tab, press `Enter` → Firefox should focus that tab
- Navigate to a tab, press `x` → Tab should close in Firefox and disappear from TUI
- Select multiple tabs with `Space`, press `x` → All selected close
- Press `g` → Group picker appears, select a group → Tabs move

**Step 6: Test reconnection**

1. Disable the extension in `about:debugging`
2. TUI should show "disconnected"
3. Re-enable extension
4. TUI should reconnect and show tabs again

**Step 7: Commit any fixes discovered during testing**

---

Plan complete and saved to `docs/plans/2026-02-13-live-mode-implementation.md`. Two execution options:

**1. Subagent-Driven (this session)** - I dispatch a fresh subagent per task, review between tasks, fast iteration

**2. Parallel Session (separate)** - Open a new session with executing-plans, batch execution with checkpoints

Which approach?