package tui

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/lotas/tabsordnung/internal/analyzer"
	"github.com/lotas/tabsordnung/internal/applog"
	"github.com/lotas/tabsordnung/internal/firefox"
	"github.com/lotas/tabsordnung/internal/server"
	"github.com/lotas/tabsordnung/internal/signal"
	"github.com/lotas/tabsordnung/internal/storage"
	"github.com/lotas/tabsordnung/internal/summarize"
	"github.com/lotas/tabsordnung/internal/types"
)

// --- Messages ---

type sessionLoadedMsg struct {
	data *types.SessionData
	err  error
}

type analysisCompleteMsg struct{}
type githubAnalysisCompleteMsg struct{}

type summarizeCompleteMsg struct {
	url     string
	summary string
	err     error
}

type signalCompleteMsg struct {
	source string
	err    error
}

type signalPollTickMsg struct{}

type signalActionMsg struct {
	source string
	err    error
}

type signalNavigateMsg struct {
	Source string
	Title  string
}

// SourceMode distinguishes live vs offline.
type SourceMode int

const (
	ModeOffline SourceMode = iota
	ModeLive
)

// Messages from the WebSocket server
type wsDisconnectedMsg struct{}
type wsSnapshotMsg struct {
	data *types.SessionData
}
type wsTabCreatedMsg struct{ tab *types.Tab }
type wsTabRemovedMsg struct{ tabID int }
type wsTabUpdatedMsg struct{ tab *types.Tab }
type wsCmdResponseMsg struct {
	id      string
	ok      bool
	error   string
	content string
	items   string
}
type wsGetTabInfoMsg struct {
	id    string
	tabID int
}
type wsSummarizeTabMsg struct {
	id    string
	tabID int
}

// --- Command helpers ---

var cmdCounter atomic.Int64

func nextCmdID() string {
	return fmt.Sprintf("cmd-%d", cmdCounter.Add(1))
}

func sendCmd(srv *server.Server, msg server.OutgoingMsg) tea.Cmd {
	return func() tea.Msg {
		msg.ID = nextCmdID()
		srv.Send(msg)
		return nil
	}
}

func sendCmdWithID(srv *server.Server, msg server.OutgoingMsg) (string, tea.Cmd) {
	id := nextCmdID()
	msg.ID = id
	return id, func() tea.Msg {
		srv.Send(msg)
		return nil
	}
}

// SummarizeJob tracks a single in-flight summarization.
type SummarizeJob struct {
	Tab            *types.Tab
	ContentID      string // non-empty = waiting for browser content (live mode)
	PopupRequestID string // non-empty = send summary back to extension popup when done
}

// SignalJob tracks a single in-flight signal capture.
type SignalJob struct {
	Tab       *types.Tab
	Source    string
	ContentID string
}

// --- Model ---

type Model struct {
	// Data
	profiles  []types.Profile
	profile   types.Profile
	session   *types.SessionData
	stats     types.Stats
	staleDays int

	// UI state
	tree       TreeModel
	detail     DetailModel
	picker     SourcePicker
	showPicker bool
	loading    bool
	err        error
	width      int
	height     int

	// Dead link checking
	deadChecking bool
	// GitHub status checking
	githubChecking bool

	// Live mode
	mode            SourceMode
	server          *server.Server
	port            int
	connected       bool
	selected        map[int]bool // BrowserID -> selected (live mode)
	groupPicker      GroupPicker
	showGroupPicker  bool
	filterPicker     FilterPicker
	showFilterPicker bool

	// Summarization
	summaryDir       string
	ollamaModel      string
	ollamaHost       string
	summarizeJobs    map[string]*SummarizeJob // URL → active job
	summarizeErrors  map[string]string        // URL → error message (persists after job, cleared on retry)

	// Focus
	focusDetail  bool

	// View switching
	activeView    ViewType
	signalsView   SignalsView
	snapshotsView SnapshotsView

	// Signals
	db            *sql.DB
	signalQueue   []*SignalJob
	signalActive  *SignalJob
	signalErrors  map[string]string
	signals       []storage.SignalRecord  // signals for currently viewed source
	signalCursor  int                      // cursor position in signal list
	signalSource  string                   // source of currently loaded signals
}

func NewModel(profiles []types.Profile, staleDays int, liveMode bool, srv *server.Server, summaryDir, ollamaModel, ollamaHost string, db *sql.DB) Model {
	m := Model{
		profiles:        profiles,
		staleDays:       staleDays,
		selected:        make(map[int]bool),
		server:          srv,
		port:            srv.Port(),
		summaryDir:      summaryDir,
		ollamaModel:     ollamaModel,
		ollamaHost:      ollamaHost,
		summarizeJobs:   make(map[string]*SummarizeJob),
		summarizeErrors: make(map[string]string),
		db:              db,
		signalErrors:    make(map[string]string),
	}
	m.signalsView = NewSignalsView(db)
	m.snapshotsView = NewSnapshotsView(db)
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

func (m Model) Init() tea.Cmd {
	if m.mode == ModeLive {
		return m.startLiveMode()
	}
	if len(m.profiles) == 1 {
		// Return command to load the single profile. The profile field will be
		// set when sessionLoadedMsg arrives (via data.Profile), so the value
		// receiver issue is avoided.
		return loadSession(m.profiles[0])
	}
	// Multiple profiles: show picker (handled in View via showPicker logic)
	return nil
}

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

func loadSession(profile types.Profile) tea.Cmd {
	return func() tea.Msg {
		data, err := firefox.ReadSessionFile(profile.Path)
		if err != nil {
			return sessionLoadedMsg{err: err}
		}
		data.Profile = profile
		return sessionLoadedMsg{data: data}
	}
}

func runDeadLinkChecks(tabs []*types.Tab) tea.Cmd {
	return func() tea.Msg {
		results := make(chan analyzer.DeadLinkResult, len(tabs))
		go func() {
			analyzer.AnalyzeDeadLinks(tabs, results)
			close(results)
		}()
		// Drain the channel. AnalyzeDeadLinks modifies tabs in-place,
		// so we just wait for all checks to complete.
		for range results {
		}
		return analysisCompleteMsg{}
	}
}

func runGitHubChecks(tabs []*types.Tab) tea.Cmd {
	return func() tea.Msg {
		analyzer.AnalyzeGitHub(tabs)
		return githubAnalysisCompleteMsg{}
	}
}

func runSummarizeTab(tab *types.Tab, outDir, model, host string) tea.Cmd {
	return func() tea.Msg {
		title, text, err := summarize.FetchReadable(tab.URL)
		if err != nil {
			return summarizeCompleteMsg{url: tab.URL, err: err}
		}
		if len(strings.TrimSpace(text)) < 50 {
			return summarizeCompleteMsg{url: tab.URL, err: fmt.Errorf("not enough readable content")}
		}
		if title == "" {
			title = tab.Title
		}
		ctx := context.Background()
		sum, err := summarize.OllamaSummarize(ctx, model, host, text)
		if err != nil {
			return summarizeCompleteMsg{url: tab.URL, err: err}
		}
		outPath := summarize.SummaryPath(outDir, tab.URL, tab.Title)
		os.MkdirAll(filepath.Dir(outPath), 0o755)
		content := fmt.Sprintf("# %s\n\n**Source:** %s\n**Summarized:** %s\n\n## Summary\n\n%s\n",
			title, tab.URL, time.Now().Format("2006-01-02"), sum)
		if err := os.WriteFile(outPath, []byte(content), 0o644); err != nil {
			return summarizeCompleteMsg{url: tab.URL, err: err}
		}
		return summarizeCompleteMsg{url: tab.URL, summary: sum}
	}
}

func runSummarizeWithContent(tab *types.Tab, content, outDir, model, host string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		sum, err := summarize.OllamaSummarize(ctx, model, host, content)
		if err != nil {
			return summarizeCompleteMsg{url: tab.URL, err: err}
		}
		outPath := summarize.SummaryPath(outDir, tab.URL, tab.Title)
		os.MkdirAll(filepath.Dir(outPath), 0o755)
		md := fmt.Sprintf("# %s\n\n**Source:** %s\n**Summarized:** %s\n\n## Summary\n\n%s\n",
			tab.Title, tab.URL, time.Now().Format("2006-01-02"), sum)
		if err := os.WriteFile(outPath, []byte(md), 0o644); err != nil {
			return summarizeCompleteMsg{url: tab.URL, err: err}
		}
		return summarizeCompleteMsg{url: tab.URL, summary: sum}
	}
}

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

const signalPollInterval = 5 * time.Minute

func signalPollTick() tea.Cmd {
	return tea.Tick(signalPollInterval, func(time.Time) tea.Msg {
		return signalPollTickMsg{}
	})
}

// queueSignalPoll walks all tabs, picks one tab per signal source, and queues
// signal jobs for sources that aren't already queued or active.
func (m *Model) queueSignalPoll() tea.Cmd {
	if m.session == nil || !m.connected {
		return signalPollTick()
	}

	// Collect one tab per source.
	sourceTabs := make(map[string]*types.Tab)
	for _, tab := range m.session.AllTabs {
		src := signal.DetectSource(tab.URL)
		if src == "" {
			continue
		}
		if _, ok := sourceTabs[src]; !ok {
			sourceTabs[src] = tab
		}
	}

	// Skip sources already in queue or active.
	if m.signalActive != nil {
		delete(sourceTabs, m.signalActive.Source)
	}
	for _, j := range m.signalQueue {
		delete(sourceTabs, j.Source)
	}

	for src, tab := range sourceTabs {
		m.signalQueue = append(m.signalQueue, &SignalJob{Tab: tab, Source: src})
	}

	return tea.Batch(m.processNextSignal(), signalPollTick())
}

func runReconcileSignals(db *sql.DB, source string, items []signal.SignalItem, capturedAt time.Time) tea.Cmd {
	return func() tea.Msg {
		records := make([]storage.SignalRecord, len(items))
		for i, item := range items {
			records[i] = storage.SignalRecord{
				Title:    item.Title,
				Preview:  item.Preview,
				Snippet:  item.Snippet,
				SourceTS: item.Timestamp,
			}
		}
		err := storage.ReconcileSignals(db, source, records, capturedAt)
		if err != nil {
			return signalCompleteMsg{source: source, err: err}
		}
		return signalCompleteMsg{source: source}
	}
}

func completeSignalCmd(db *sql.DB, id int64, source string) tea.Cmd {
	return func() tea.Msg {
		err := storage.CompleteSignal(db, id)
		return signalActionMsg{source: source, err: err}
	}
}

func reopenSignalCmd(db *sql.DB, id int64, source string) tea.Cmd {
	return func() tea.Msg {
		err := storage.ReopenSignal(db, id)
		return signalActionMsg{source: source, err: err}
	}
}

func listenWebSocket(srv *server.Server) tea.Cmd {
	return func() tea.Msg {
		for {
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
					continue // skip malformed, keep listening
				}
				return wsTabCreatedMsg{tab: tab}
			case "tab.removed":
				return wsTabRemovedMsg{tabID: msg.TabID}
			case "tab.updated", "tab.moved":
				tab, err := server.ParseTab(msg.Tab)
				if err != nil {
					continue // skip malformed, keep listening
				}
				return wsTabUpdatedMsg{tab: tab}
			case "get-tab-info":
				return wsGetTabInfoMsg{id: msg.ID, tabID: msg.TabID}
			case "summarize-tab":
				return wsSummarizeTabMsg{id: msg.ID, tabID: msg.TabID}
			default:
				if msg.ID != "" && msg.OK != nil {
					return wsCmdResponseMsg{id: msg.ID, ok: *msg.OK, error: msg.Error, content: msg.Content, items: msg.Items}
				}
				// Unknown message type, skip and keep listening
			}
		}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		treeWidth := m.width * 60 / 100
		detailWidth := m.width - treeWidth - 3 // borders
		paneHeight := m.height - 4              // navbar + borders + bottom bar
		m.tree.Width = treeWidth
		m.tree.Height = paneHeight
		m.detail.Width = detailWidth
		m.detail.Height = paneHeight
		m.picker.Width = m.width
		m.picker.Height = m.height
		m.signalsView.SetSize(m.width, paneHeight)
		m.snapshotsView.SetSize(m.width, paneHeight)
		return m, nil

	case tea.MouseMsg:
		// Determine which pane the mouse is over based on X position.
		// Tree pane occupies roughly 0..treeWidth, detail pane is the rest.
		treeWidth := m.width * 60 / 100
		onDetail := msg.X > treeWidth+1
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if onDetail {
				m.detail.ScrollUp()
			} else {
				m.tree.MoveUp()
				m.detail.Scroll = 0
				m.refreshSignals()
			}
		case tea.MouseButtonWheelDown:
			if onDetail {
				m.detail.ScrollDown()
			} else {
				m.tree.MoveDown()
				m.detail.Scroll = 0
				m.refreshSignals()
			}
		}
		return m, nil

	case tea.KeyMsg:
		// 1/2/3 switches views, Tab toggles pane focus (when no modal)
		if !m.showPicker && !m.showGroupPicker && !m.showFilterPicker {
			switch msg.String() {
			case "1":
				if m.activeView != ViewTabs {
					m.activeView = ViewTabs
					m.focusDetail = false
					m.detail.Scroll = 0
				}
				return m, nil
			case "2":
				if m.activeView != ViewSignals {
					m.activeView = ViewSignals
					m.focusDetail = false
					m.detail.Scroll = 0
					return m, m.signalsView.Reload()
				}
				return m, nil
			case "3":
				if m.activeView != ViewSnapshots {
					m.activeView = ViewSnapshots
					m.focusDetail = false
					m.detail.Scroll = 0
					return m, m.snapshotsView.SetProfile(m.profile.Name)
				}
				return m, nil
			case "tab", "shift+tab":
				switch m.activeView {
				case ViewTabs:
					if m.focusDetail {
						m.focusDetail = false
						m.detail.Scroll = 0
					} else {
						node := m.tree.SelectedNode()
						if node != nil && (node.Tab != nil || node.Group != nil) {
							m.focusDetail = true
						}
					}
				case ViewSignals:
					m.signalsView.focusDetail = !m.signalsView.focusDetail
					if !m.signalsView.focusDetail {
						m.signalsView.detail.Scroll = 0
					}
				case ViewSnapshots:
					m.snapshotsView.focusDetail = !m.snapshotsView.focusDetail
					if !m.snapshotsView.focusDetail {
						m.snapshotsView.detail.Scroll = 0
					}
				}
				return m, nil
			}
		}

		// Delegate to active view (skip when modal is open)
		if !m.showPicker && !m.showGroupPicker && !m.showFilterPicker {
			if m.activeView == ViewSignals {
				if msg.String() == "q" || msg.String() == "ctrl+c" {
					return m, tea.Quit
				}
				if msg.String() != "p" {
					v, cmd := m.signalsView.Update(msg)
					m.signalsView = v
					return m, cmd
				}
			}
			if m.activeView == ViewSnapshots {
				if msg.String() == "q" || msg.String() == "ctrl+c" {
					return m, tea.Quit
				}
				if msg.String() != "p" {
					v, cmd := m.snapshotsView.Update(msg)
					m.snapshotsView = v
					return m, cmd
				}
			}
		}

		// Detail pane focus mode
		if m.focusDetail {
			// Signal navigation when viewing a signal-source tab.
			if m.signalSource != "" && len(m.signals) > 0 {
				switch msg.String() {
				case "j", "down":
					if m.signalCursor < len(m.signals)-1 {
						m.signalCursor++
					}
					m.scrollDetailToSignalCursor()
					return m, nil
				case "k", "up":
					if m.signalCursor > 0 {
						m.signalCursor--
					}
					m.scrollDetailToSignalCursor()
					return m, nil
				case "enter":
					if m.mode == ModeLive && m.connected {
						sig := m.signals[m.signalCursor]
						tab := m.findTabForSource(m.signalSource)
						if tab != nil && tab.BrowserID != 0 {
							return m, navigateSignalCmd(m.server, tab.BrowserID, m.signalSource, sig.Title)
						}
					}
					return m, nil
				case "x":
					sig := m.signals[m.signalCursor]
					if sig.CompletedAt == nil {
						return m, completeSignalCmd(m.db, sig.ID, m.signalSource)
					}
					return m, nil
				case "u":
					sig := m.signals[m.signalCursor]
					if sig.CompletedAt != nil {
						return m, reopenSignalCmd(m.db, sig.ID, m.signalSource)
					}
					return m, nil
				case "esc":
					m.focusDetail = false
					m.detail.Scroll = 0
					return m, nil
				case "q", "ctrl+c":
					return m, tea.Quit
				case "c":
					// Fall through to main 'c' handler below by breaking out
				default:
					return m, nil
				}
			} else {
				// Existing non-signal detail focus handling
				switch msg.String() {
				case "esc":
					m.focusDetail = false
					m.detail.Scroll = 0
					return m, nil
				case "j", "down":
					m.detail.ScrollDown()
					return m, nil
				case "k", "up":
					m.detail.ScrollUp()
					return m, nil
				case "s":
					// fall through to main handler
				case "q", "ctrl+c":
					return m, tea.Quit
				default:
					return m, nil
				}
			}
		}

		// Group picker mode
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

		// Filter picker mode
		if m.showFilterPicker {
			switch msg.String() {
			case "up", "k":
				m.filterPicker.MoveUp()
			case "down", "j":
				m.filterPicker.MoveDown()
			case "enter":
				m.tree.SetFilter(m.filterPicker.Selected().Mode)
				m.showFilterPicker = false
			case "esc":
				m.showFilterPicker = false
			case "q", "ctrl+c":
				return m, tea.Quit
			}
			return m, nil
		}

		// Source picker mode
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

		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			m.tree.MoveUp()
			m.refreshSignals()
		case "down", "j":
			m.tree.MoveDown()
			m.refreshSignals()
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
			node := m.tree.SelectedNode()
			if node != nil && node.Group != nil {
				m.tree.Toggle()
			} else if node != nil && node.Tab != nil {
				m.focusDetail = true
			}
		case "h":
			m.tree.CollapseOrParent()
		case "l":
			m.tree.ExpandOrEnter()
		case "s":
			node := m.tree.SelectedNode()
			if node != nil && node.Tab != nil {
				url := node.Tab.URL
				if _, exists := m.summarizeJobs[url]; exists {
					break // already in progress
				}
				delete(m.summarizeErrors, url)
				job := &SummarizeJob{Tab: node.Tab}
				m.summarizeJobs[url] = job
				if m.mode == ModeLive && m.connected {
					id, cmd := sendCmdWithID(m.server, server.OutgoingMsg{
						Action: "get-content",
						TabID:  node.Tab.BrowserID,
					})
					job.ContentID = id
					return m, cmd
				}
				return m, runSummarizeTab(node.Tab, m.summaryDir, m.ollamaModel, m.ollamaHost)
			}
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
			alreadyQueued := false
			for _, j := range m.signalQueue {
				if j.Tab.BrowserID == node.Tab.BrowserID {
					alreadyQueued = true
					break
				}
			}
			if alreadyQueued {
				break
			}
			if m.signalActive != nil && m.signalActive.Tab.BrowserID == node.Tab.BrowserID {
				break
			}
			delete(m.signalErrors, source)
			job := &SignalJob{Tab: node.Tab, Source: source}
			m.signalQueue = append(m.signalQueue, job)
			return m, m.processNextSignal()
		case "f":
			m.showFilterPicker = true
			m.filterPicker = NewFilterPicker(m.tree.Filter)
			m.filterPicker.Width = m.width
			m.filterPicker.Height = m.height
		case "r":
			if m.mode == ModeLive {
				// In live mode, the extension will re-send a snapshot on reconnect.
				// For now, just re-listen — the extension auto-sends on connect.
				return m, nil
			}
			m.loading = true
			return m, loadSession(m.profile)
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
			m.refreshSignals()
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
		case "esc":
			if m.focusDetail {
				m.focusDetail = false
				m.detail.Scroll = 0
			} else {
				m.selected = make(map[int]bool)
			}
		case "p":
			m.showPicker = true
			m.picker = NewSourcePicker(m.profiles)
			m.picker.Width = m.width
			m.picker.Height = m.height
		}
		return m, nil

	case sessionLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.session = msg.data
		m.profile = msg.data.Profile

		// Run synchronous analyzers
		analyzer.AnalyzeStale(m.session.AllTabs, m.staleDays)
		analyzer.AnalyzeDuplicates(m.session.AllTabs)
		m.stats = analyzer.ComputeStats(m.session)

		// Set up tree
		m.rebuildTree()

		// Notify snapshots view of profile change
		snapshotsCmd := m.snapshotsView.SetProfile(m.profile.Name)

		// Start async checks
		m.deadChecking = true
		m.githubChecking = true
		return m, tea.Batch(
			runDeadLinkChecks(m.session.AllTabs),
			runGitHubChecks(m.session.AllTabs),
			snapshotsCmd,
		)

	case analysisCompleteMsg:
		m.deadChecking = false
		m.stats = analyzer.ComputeStats(m.session)
		return m, nil

	case githubAnalysisCompleteMsg:
		m.githubChecking = false
		m.stats = analyzer.ComputeStats(m.session)
		return m, nil

	case summarizeCompleteMsg:
		job := m.summarizeJobs[msg.url]
		popupID := ""
		if job != nil {
			popupID = job.PopupRequestID
		}
		delete(m.summarizeJobs, msg.url)
		if msg.err != nil {
			m.summarizeErrors[msg.url] = msg.err.Error()
			if popupID != "" {
				m.server.Send(server.OutgoingMsg{
					ID:     popupID,
					Action: "summarize-result",
					Error:  msg.err.Error(),
				})
			}
		} else {
			delete(m.summarizeErrors, msg.url)
			if popupID != "" {
				m.server.Send(server.OutgoingMsg{
					ID:      popupID,
					Action:  "summarize-result",
					Summary: msg.summary,
				})
			}
		}
		return m, nil

	case signalCompleteMsg:
		if msg.err != nil {
			applog.Error("tui.signal", msg.err, "source", msg.source)
			m.signalErrors[msg.source] = msg.err.Error()
		} else {
			applog.Info("tui.signal", "source", msg.source)
			delete(m.signalErrors, msg.source)
		}
		// Reload signals for current source.
		if m.signalSource != "" {
			m.signals, _ = storage.ListSignals(m.db, m.signalSource, true)
		}
		m.tree.SignalCounts, _ = storage.ActiveSignalCounts(m.db)
		return m, m.processNextSignal()

	case signalActionMsg:
		if msg.err != nil {
			m.signalErrors[msg.source] = msg.err.Error()
		}
		// Reload signals.
		if m.signalSource != "" {
			m.signals, _ = storage.ListSignals(m.db, m.signalSource, true)
			if m.signalCursor >= len(m.signals) {
				m.signalCursor = len(m.signals) - 1
			}
			if m.signalCursor < 0 {
				m.signalCursor = 0
			}
		}
		m.tree.SignalCounts, _ = storage.ActiveSignalCounts(m.db)
		// Also route to signals view if active
		if m.activeView == ViewSignals {
			v, cmd := m.signalsView.Update(msg)
			m.signalsView = v
			return m, cmd
		}
		return m, nil

	case signalNavigateMsg:
		if m.mode == ModeLive && m.connected {
			tab := m.findTabForSource(msg.Source)
			if tab != nil && tab.BrowserID != 0 {
				return m, navigateSignalCmd(m.server, tab.BrowserID, msg.Source, msg.Title)
			}
		}
		return m, nil

	case signalPollTickMsg:
		return m, m.queueSignalPoll()

	case wsSnapshotMsg:
		m.loading = false
		m.connected = true
		m.session = msg.data
		applog.Info("tui.snapshot", "tabs", len(msg.data.AllTabs), "groups", len(msg.data.Groups))

		analyzer.AnalyzeStale(m.session.AllTabs, m.staleDays)
		analyzer.AnalyzeDuplicates(m.session.AllTabs)
		m.stats = analyzer.ComputeStats(m.session)

		m.rebuildTree()

		m.deadChecking = true
		m.githubChecking = true
		return m, tea.Batch(
			runDeadLinkChecks(m.session.AllTabs),
			runGitHubChecks(m.session.AllTabs),
			listenWebSocket(m.server),
			signalPollTick(),
		)

	case wsDisconnectedMsg:
		m.connected = false
		// Clear any in-flight signal job
		if m.signalActive != nil {
			m.signalErrors[m.signalActive.Source] = "disconnected"
			m.signalActive = nil
		}
		m.signalQueue = nil
		var cmds []tea.Cmd
		for _, job := range m.summarizeJobs {
			if job.ContentID != "" {
				job.ContentID = ""
				cmds = append(cmds, runSummarizeTab(job.Tab, m.summaryDir, m.ollamaModel, m.ollamaHost))
			}
		}
		if m.mode == ModeLive && m.server != nil {
			cmds = append(cmds, listenWebSocket(m.server))
		}
		if len(cmds) > 0 {
			return m, tea.Batch(cmds...)
		}
		return m, nil

	case wsTabRemovedMsg:
		if m.session != nil {
			m.removeTab(msg.tabID)
			analyzer.AnalyzeDuplicates(m.session.AllTabs)
			m.stats = analyzer.ComputeStats(m.session)
			m.rebuildTree()
		}
		return m, listenWebSocket(m.server)

	case wsTabCreatedMsg:
		if m.session != nil {
			m.addTab(msg.tab)
			analyzer.AnalyzeStale(m.session.AllTabs, m.staleDays)
			analyzer.AnalyzeDuplicates(m.session.AllTabs)
			m.stats = analyzer.ComputeStats(m.session)
			m.rebuildTree()
		}
		return m, listenWebSocket(m.server)

	case wsTabUpdatedMsg:
		if m.session != nil {
			m.updateTab(msg.tab)
			analyzer.AnalyzeStale(m.session.AllTabs, m.staleDays)
			analyzer.AnalyzeDuplicates(m.session.AllTabs)
			m.stats = analyzer.ComputeStats(m.session)
			m.rebuildTree()
		}
		return m, listenWebSocket(m.server)

	case wsGetTabInfoMsg:
		payload := m.buildTabInfoPayload(msg.tabID)
		m.server.Send(server.OutgoingMsg{
			ID:      msg.id,
			Action:  "tab-info",
			TabInfo: payload,
		})
		return m, listenWebSocket(m.server)

	case wsSummarizeTabMsg:
		tab := m.findTabByBrowserID(msg.tabID)
		if tab == nil {
			m.server.Send(server.OutgoingMsg{
				ID:    msg.id,
				Action: "summarize-result",
				Error: "Tab not found",
			})
			return m, listenWebSocket(m.server)
		}
		// Check if already in progress
		if existing, ok := m.summarizeJobs[tab.URL]; ok {
			existing.PopupRequestID = msg.id
			return m, listenWebSocket(m.server)
		}
		// Start summarization
		job := &SummarizeJob{Tab: tab, PopupRequestID: msg.id}
		m.summarizeJobs[tab.URL] = job
		if m.mode == ModeLive && m.connected {
			id, cmd := sendCmdWithID(m.server, server.OutgoingMsg{
				Action: "get-content",
				TabID:  tab.BrowserID,
			})
			job.ContentID = id
			return m, tea.Batch(listenWebSocket(m.server), cmd)
		}
		return m, tea.Batch(
			listenWebSocket(m.server),
			runSummarizeTab(tab, m.summaryDir, m.ollamaModel, m.ollamaHost),
		)

	case wsCmdResponseMsg:
		applog.Info("tui.cmdResponse", "id", msg.id, "ok", msg.ok)
		// Check if this response matches a signal job
		if m.signalActive != nil && m.signalActive.ContentID == msg.id {
			source := m.signalActive.Source
			m.signalActive = nil
			if !msg.ok {
				m.signalErrors[source] = msg.error
				return m, tea.Batch(listenWebSocket(m.server), m.processNextSignal())
			}
			items, err := signal.ParseItemsJSON(msg.items)
			if err != nil {
				m.signalErrors[source] = err.Error()
				return m, tea.Batch(listenWebSocket(m.server), m.processNextSignal())
			}
			// Empty items is valid: means nothing unread. Reconcile to auto-complete active signals.
			return m, tea.Batch(
				listenWebSocket(m.server),
				runReconcileSignals(m.db, source, items, time.Now()),
			)
		}
		// Check if this response matches a summarize job
		for _, job := range m.summarizeJobs {
			if job.ContentID != "" && job.ContentID == msg.id {
				tab := job.Tab
				job.ContentID = ""
				content := strings.TrimSpace(msg.content)
				if msg.ok && len(content) >= 50 {
					return m, tea.Batch(
						listenWebSocket(m.server),
						runSummarizeWithContent(tab, content, m.summaryDir, m.ollamaModel, m.ollamaHost),
					)
				}
				// Fallback to HTTP fetch
				return m, tea.Batch(
					listenWebSocket(m.server),
					runSummarizeTab(tab, m.summaryDir, m.ollamaModel, m.ollamaHost),
				)
			}
		}
		return m, listenWebSocket(m.server)

	case signalsViewLoadedMsg:
		v, cmd := m.signalsView.Update(msg)
		m.signalsView = v
		return m, cmd

	case snapshotDetailMsg:
		v, cmd := m.snapshotsView.Update(msg)
		m.snapshotsView = v
		return m, cmd

	case snapshotsLoadedMsg:
		v, cmd := m.snapshotsView.Update(msg)
		m.snapshotsView = v
		return m, cmd
	}

	return m, nil
}

// scrollDetailToSignalCursor adjusts detail pane scroll so the signal cursor is visible.
// Signal items start after the tab info header in the rendered content.
// We estimate the line offset: ViewTab produces ~8 lines, then header + blank = 2 more,
// then 1 line per signal.
func (m *Model) scrollDetailToSignalCursor() {
	headerLines := 10 // approximate lines before signal list items
	cursorLine := headerLines + m.signalCursor
	if cursorLine < m.detail.Scroll {
		m.detail.Scroll = cursorLine
	} else if cursorLine >= m.detail.Scroll+m.detail.Height-1 {
		m.detail.Scroll = cursorLine - m.detail.Height + 2
	}
	if m.detail.Scroll < 0 {
		m.detail.Scroll = 0
	}
}

func (m *Model) refreshSignals() {
	node := m.tree.SelectedNode()
	var source string
	if node != nil && node.Tab != nil {
		source = signal.DetectSource(node.Tab.URL)
	}
	if source != m.signalSource {
		m.signalSource = source
		m.signalCursor = 0
		if source != "" && m.db != nil {
			m.signals, _ = storage.ListSignals(m.db, source, true)
		} else {
			m.signals = nil
		}
	}
}

func (m *Model) findTabForSource(source string) *types.Tab {
	if m.session == nil {
		return nil
	}
	for _, tab := range m.session.AllTabs {
		if signal.DetectSource(tab.URL) == source {
			return tab
		}
	}
	return nil
}

func navigateSignalCmd(srv *server.Server, tabID int, source, title string) tea.Cmd {
	return sendCmd(srv, server.OutgoingMsg{
		Action: "navigate-signal",
		TabID:  tabID,
		Source: source,
		Title:  title,
	})
}

func (m *Model) findTabByBrowserID(browserID int) *types.Tab {
	if m.session == nil {
		return nil
	}
	for _, t := range m.session.AllTabs {
		if t.BrowserID == browserID {
			return t
		}
	}
	return nil
}

func (m *Model) buildTabInfoPayload(browserID int) *server.TabInfoPayload {
	tab := m.findTabByBrowserID(browserID)
	if tab == nil {
		return nil
	}
	payload := &server.TabInfoPayload{
		URL:          tab.URL,
		Title:        tab.Title,
		LastAccessed: tab.LastAccessed.Format("2006-01-02 15:04"),
		StaleDays:    tab.StaleDays,
		IsStale:      tab.IsStale,
		IsDead:       tab.IsDead,
		DeadReason:   tab.DeadReason,
		IsDuplicate:  tab.IsDuplicate,
		GitHubStatus: tab.GitHubStatus,
	}
	// Read summary if available
	sumPath := summarize.SummaryPath(m.summaryDir, tab.URL, tab.Title)
	if raw, err := summarize.ReadSummary(sumPath); err == nil {
		payload.Summary = raw
	}
	// Check for signal source
	source := signal.DetectSource(tab.URL)
	if source != "" && m.db != nil {
		payload.SignalSource = source
		if signals, err := storage.ListSignals(m.db, source, false); err == nil {
			for _, s := range signals {
				payload.Signals = append(payload.Signals, server.SignalPayload{
					ID:       s.ID,
					Title:    s.Title,
					Preview:  s.Preview,
					Snippet:  s.Snippet,
					SourceTS: s.SourceTS,
					Active:   s.CompletedAt == nil,
				})
			}
		}
	}
	return payload
}

func (m *Model) rebuildTree() {
	oldCursor := m.tree.Cursor
	oldOffset := m.tree.Offset
	oldExpanded := m.tree.Expanded
	oldFilter := m.tree.Filter
	oldSavedExpanded := m.tree.SavedExpanded

	m.tree = NewTreeModel(m.session.Groups)
	m.tree.Width = m.width * 60 / 100
	m.tree.Height = m.height - 4
	m.tree.Filter = oldFilter
	m.tree.SavedExpanded = oldSavedExpanded
	m.tree.SummaryDir = m.summaryDir
	if m.db != nil {
		m.tree.SignalCounts, _ = storage.ActiveSignalCounts(m.db)
	}

	// Restore expanded state from before rebuild
	if oldExpanded != nil {
		for id, exp := range oldExpanded {
			m.tree.Expanded[id] = exp
		}
	}

	// Expand any new groups when a filter is active
	if m.tree.Filter != types.FilterAll {
		for _, g := range m.session.Groups {
			if _, exists := oldExpanded[g.ID]; !exists {
				m.tree.Expanded[g.ID] = true
			}
		}
	}

	// Clamp cursor to valid range
	nodes := m.tree.VisibleNodes()
	if oldCursor >= len(nodes) {
		oldCursor = len(nodes) - 1
	}
	if oldCursor < 0 {
		oldCursor = 0
	}
	m.tree.Cursor = oldCursor
	m.tree.Offset = oldOffset
	m.refreshSignals()
}

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
	// Try to place in matching group (skip empty GroupID — that means ungrouped)
	placed := false
	if tab.GroupID != "" {
		for _, g := range m.session.Groups {
			if g.ID == tab.GroupID {
				g.Tabs = append(g.Tabs, tab)
				placed = true
				break
			}
		}
	}
	// Unmatched or empty GroupID → place in ungrouped group
	if !placed {
		tab.GroupID = ""
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
			if t.GroupID != tab.GroupID {
				m.removeTab(tab.BrowserID)
				m.addTab(tab)
			}
			return
		}
	}
	m.addTab(tab)
}

func (m Model) View() string {
	if m.loading {
		if m.mode == ModeLive {
			return fmt.Sprintf("\n  Waiting for extension connection on :%d...\n", m.port)
		}
		return "\n  Loading session data...\n"
	}

	if m.showPicker {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.picker.View())
	}

	if m.showGroupPicker {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.groupPicker.View())
	}

	if m.showFilterPicker {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.filterPicker.View())
	}

	if m.err != nil {
		return fmt.Sprintf("\n  Error: %v\n\n  Press 1-9 to switch source, 'q' to quit.\n", m.err)
	}

	if m.session == nil && m.activeView == ViewTabs {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.picker.View())
	}

	// Navbar with inline stats
	var profileName string
	if m.mode == ModeLive {
		if m.connected {
			profileName = "Live \u25cf connected"
		} else {
			profileName = "Live \u25cb waiting..."
		}
	} else {
		profileName = m.profile.Name
	}

	var statsStr string
	if m.activeView == ViewTabs && m.session != nil {
		statsStr = fmt.Sprintf("%d tabs \u00b7 %d groups", m.stats.TotalTabs, m.stats.TotalGroups)
		if m.stats.DeadTabs > 0 {
			statsStr += fmt.Sprintf(" \u00b7 %d dead", m.stats.DeadTabs)
		}
		if m.stats.StaleTabs > 0 {
			statsStr += fmt.Sprintf(" \u00b7 %d stale", m.stats.StaleTabs)
		}
		if m.stats.DuplicateTabs > 0 {
			statsStr += fmt.Sprintf(" \u00b7 %d dup", m.stats.DuplicateTabs)
		}
		if m.stats.GitHubDoneTabs > 0 {
			statsStr += fmt.Sprintf(" \u00b7 %d done", m.stats.GitHubDoneTabs)
		}
		if m.deadChecking {
			statsStr += " \u00b7 checking links..."
		}
		if m.githubChecking {
			statsStr += " \u00b7 checking github..."
		}
		if n := len(m.summarizeJobs); n == 1 {
			statsStr += " \u00b7 summarizing 1 tab..."
		} else if n > 1 {
			statsStr += fmt.Sprintf(" \u00b7 summarizing %d tabs...", n)
		}
		if m.signalActive != nil {
			statsStr += " \u00b7 checking signals..."
		}
	}
	var viewCounts [3]int
	viewCounts[ViewTabs] = m.stats.TotalTabs
	for _, c := range m.tree.SignalCounts {
		viewCounts[ViewSignals] += c
	}
	viewCounts[ViewSnapshots] = len(m.snapshotsView.snapshots)
	navbar := renderNavbar(m.activeView, profileName, viewCounts, statsStr, m.width)

	// Pane content
	treeWidth := m.width * 60 / 100
	detailWidth := m.width - treeWidth - 3
	paneHeight := m.height - 4

	var leftContent, rightContent string
	var isFocusDetail bool

	switch m.activeView {
	case ViewTabs:
		if m.session == nil {
			leftContent = "No session loaded"
			rightContent = ""
		} else {
			isFocusDetail = m.focusDetail
			// Render detail based on selection
			var detailContent string
			if node := m.tree.SelectedNode(); node != nil {
				if node.Tab != nil {
					if m.signalSource != "" {
						isCapturing := m.signalActive != nil && m.signalActive.Source == m.signalSource
						if !isCapturing {
							for _, j := range m.signalQueue {
								if j.Source == m.signalSource {
									isCapturing = true
									break
								}
							}
						}
						sigErr := m.signalErrors[m.signalSource]
						detailContent = m.detail.ViewTabWithSignal(node.Tab, m.signals, m.signalCursor, isCapturing, sigErr)
					} else {
						var summaryText string
						sumPath := summarize.SummaryPath(m.summaryDir, node.Tab.URL, node.Tab.Title)
						if raw, err := summarize.ReadSummary(sumPath); err == nil {
							r, _ := glamour.NewTermRenderer(
								glamour.WithStylePath("dark"),
								glamour.WithWordWrap(detailWidth-2),
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
				} else if node.Group != nil {
					detailContent = m.detail.ViewGroup(node.Group)
				}
			}
			rightContent = m.detail.ViewScrolled(detailContent)

			m.tree.Selected = m.selected
			summarizingURLs := make(map[string]bool, len(m.summarizeJobs))
			for url := range m.summarizeJobs {
				summarizingURLs[url] = true
			}
			m.tree.SummarizingURLs = summarizingURLs
			leftContent = m.tree.View()
		}

	case ViewSignals:
		isFocusDetail = m.signalsView.FocusDetail()
		leftContent = m.signalsView.ViewList()
		rightContent = m.signalsView.ViewDetail()

	case ViewSnapshots:
		isFocusDetail = m.snapshotsView.FocusDetail()
		leftContent = m.snapshotsView.ViewList()
		rightContent = m.snapshotsView.ViewDetail()
	}

	// Pane borders
	treeBorderColor := "62"
	detailBorderColor := "240"
	if isFocusDetail {
		treeBorderColor = "240"
		detailBorderColor = "62"
	}

	treeBorder := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(treeBorderColor)).
		Width(treeWidth).
		Height(paneHeight)

	detailBorder := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(detailBorderColor)).
		Width(detailWidth).
		Height(paneHeight).
		MaxHeight(paneHeight + 2)

	left := treeBorder.Render(leftContent)
	right := detailBorder.Render(rightContent)
	panes := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	// Bottom bar
	bottomBarStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Padding(0, 1)
	var bottomText string
	switch m.activeView {
	case ViewTabs:
		if m.mode == ModeLive && m.connected {
			selCount := len(m.selected)
			if selCount > 0 {
				bottomText = fmt.Sprintf("%d selected \u00b7 x close \u00b7 g move \u00b7 esc clear \u00b7 ", selCount)
			}
			bottomText += "space select \u00b7 enter focus \u00b7 "
		}
		filterNames := []string{"all", "stale", "dead", "duplicate", ">7d", ">30d", ">90d", "gh done", "summarized", "unsummarized"}
		filterStr := fmt.Sprintf("[filter: %s]", filterNames[m.tree.Filter])
		bottomText += "\u2191\u2193/jk navigate \u00b7 tab focus \u00b7 s summarize \u00b7 c signal \u00b7 f filter \u00b7 r refresh \u00b7 p source \u00b7 q quit  " + filterStr
	case ViewSignals:
		bottomText = "\u2191\u2193/jk navigate \u00b7 \u21b5 open \u00b7 tab focus \u00b7 x complete \u00b7 u reopen \u00b7 1-3 view \u00b7 p source \u00b7 q quit"
	case ViewSnapshots:
		bottomText = "\u2191\u2193/jk navigate \u00b7 tab focus \u00b7 1-3 view \u00b7 p source \u00b7 q quit"
	}
	bottomBar := bottomBarStyle.Render(bottomText)

	// Assemble
	return lipgloss.JoinVertical(lipgloss.Left, navbar, panes, bottomBar)
}
