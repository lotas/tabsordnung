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

type rebuildTickMsg struct{}

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
	staleDays int

	// UI state
	picker     SourcePicker
	showPicker bool
	loading    bool
	err        error
	width      int
	height     int

	// Live mode
	mode             SourceMode
	server           *server.Server
	port             int
	connected        bool
	cancel           context.CancelFunc
	groupPicker      GroupPicker
	showGroupPicker  bool
	filterPicker     FilterPicker
	showFilterPicker bool

	// Summarization config (needed for WS-triggered summarize)
	summaryDir  string
	ollamaModel string
	ollamaHost  string

	// Database
	db *sql.DB

	// View switching
	activeView    ViewType
	tabsView      TabsView
	signalsView   SignalsView
	snapshotsView SnapshotsView

	// Debounced rebuild
	rebuildDirty     bool
	rebuildScheduled bool
}

func NewModel(profiles []types.Profile, staleDays int, liveMode bool, srv *server.Server, summaryDir, ollamaModel, ollamaHost string, db *sql.DB) Model {
	m := Model{
		profiles:    profiles,
		staleDays:   staleDays,
		server:      srv,
		port:        srv.Port(),
		summaryDir:  summaryDir,
		ollamaModel: ollamaModel,
		ollamaHost:  ollamaHost,
		db:          db,
	}
	m.tabsView = NewTabsView(srv, db, summaryDir, ollamaModel, ollamaHost)
	m.tabsView.staleDays = staleDays
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

func (m Model) Init() tea.Cmd {
	if m.mode == ModeLive {
		return tea.Batch(
			listenWebSocket(m.server),
			startWSServerCtx(context.Background(), m.server),
		)
	}
	if len(m.profiles) == 1 {
		return loadSession(m.profiles[0])
	}
	return nil
}

func (m *Model) startLiveMode() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	return tea.Batch(
		listenWebSocket(m.server),
		startWSServerCtx(ctx, m.server),
	)
}

func startWSServerCtx(ctx context.Context, srv *server.Server) tea.Cmd {
	return func() tea.Msg {
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

const signalPollInterval = 5 * time.Minute

func signalPollTick() tea.Cmd {
	return tea.Tick(signalPollInterval, func(time.Time) tea.Msg {
		return signalPollTickMsg{}
	})
}

func runReconcileSignals(db *sql.DB, source string, items []signal.SignalItem, capturedAt time.Time) tea.Cmd {
	return func() tea.Msg {
		applog.Info("signal.reconcile.start", "source", source, "itemCount", len(items), "capturedAt", capturedAt.Format(time.RFC3339))
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
			applog.Error("signal.reconcile.error", err, "source", source)
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
					continue
				}
				return wsTabCreatedMsg{tab: tab}
			case "tab.removed":
				return wsTabRemovedMsg{tabID: msg.TabID}
			case "tab.updated", "tab.moved":
				tab, err := server.ParseTab(msg.Tab)
				if err != nil {
					continue
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
			}
		}
	}
}

func navigateSignalCmd(srv *server.Server, tabID int, source, title string) tea.Cmd {
	return sendCmd(srv, server.OutgoingMsg{
		Action: "navigate-signal",
		TabID:  tabID,
		Source: source,
		Title:  title,
	})
}

// --- Debounce helpers ---

func rebuildTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return rebuildTickMsg{}
	})
}

func (m *Model) scheduleRebuild() tea.Cmd {
	m.rebuildDirty = true
	if m.rebuildScheduled {
		return nil
	}
	m.rebuildScheduled = true
	return rebuildTick()
}

func (m *Model) doRebuild() {
	if !m.rebuildDirty || m.session == nil {
		return
	}
	analyzer.AnalyzeStale(m.session.AllTabs, m.staleDays)
	analyzer.AnalyzeDuplicates(m.session.AllTabs)
	m.tabsView.stats = analyzer.ComputeStats(m.session)
	m.tabsView.RebuildTree()
	m.rebuildDirty = false
	m.rebuildScheduled = false
}

// --- Update ---

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.tabsView.SetSize(m.width, m.height)
		m.picker.Width = m.width
		m.picker.Height = m.height
		paneHeight := m.height - 4
		m.signalsView.SetSize(m.width, paneHeight)
		m.snapshotsView.SetSize(m.width, paneHeight)
		return m, nil

	case tea.KeyMsg:
		// View switching and global keys (when no modal)
		if !m.showPicker && !m.showGroupPicker && !m.showFilterPicker {
			switch msg.String() {
			case "1":
				if m.activeView != ViewTabs {
					m.activeView = ViewTabs
					m.tabsView.focusDetail = false
					m.tabsView.detail.Scroll = 0
				}
				return m, nil
			case "2":
				if m.activeView != ViewSignals {
					m.activeView = ViewSignals
					return m, m.signalsView.Reload()
				}
				return m, nil
			case "3":
				if m.activeView != ViewSnapshots {
					m.activeView = ViewSnapshots
					if !m.snapshotsView.loaded {
						return m, m.snapshotsView.LoadAll()
					}
				}
				return m, nil
			}
		}

		// Modal handling
		if m.showGroupPicker {
			return m.updateGroupPicker(msg)
		}
		if m.showFilterPicker {
			return m.updateFilterPicker(msg)
		}
		if m.showPicker {
			return m.updateSourcePicker(msg)
		}

		// Global keys handled before view delegation
		switch msg.String() {
		case "q", "ctrl+c":
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		case "p":
			m.showPicker = true
			m.picker = NewSourcePicker(m.profiles)
			m.picker.Width = m.width
			m.picker.Height = m.height
			return m, nil
		}

		// Delegate to active view
		switch m.activeView {
		case ViewTabs:
			v, cmd := m.tabsView.Update(msg)
			m.tabsView = v
			return m, cmd

		case ViewSignals:
			v, cmd := m.signalsView.Update(msg)
			m.signalsView = v
			return m, cmd

		case ViewSnapshots:
			v, cmd := m.snapshotsView.Update(msg)
			m.snapshotsView = v
			return m, cmd
		}
		return m, nil

	case tea.MouseMsg:
		if m.activeView == ViewTabs {
			v, cmd := m.tabsView.Update(msg)
			m.tabsView = v
			return m, cmd
		}
		return m, nil

	// --- Custom messages from TabsView ---
	case showGroupPickerMsg:
		m.showGroupPicker = true
		m.groupPicker = NewGroupPicker(m.session.Groups)
		m.groupPicker.Width = m.width
		m.groupPicker.Height = m.height
		return m, nil

	case showFilterPickerMsg:
		m.showFilterPicker = true
		m.filterPicker = NewFilterPicker(m.tabsView.tree.Filter)
		m.filterPicker.Width = m.width
		m.filterPicker.Height = m.height
		return m, nil

	case reloadSessionMsg:
		m.loading = true
		return m, loadSession(m.profile)

	// --- Async results ---
	case sessionLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.session = msg.data
		m.profile = msg.data.Profile
		m.tabsView.session = m.session
		m.tabsView.mode = m.mode
		m.tabsView.connected = m.connected

		analyzer.AnalyzeStale(m.session.AllTabs, m.staleDays)
		analyzer.AnalyzeDuplicates(m.session.AllTabs)
		m.tabsView.stats = analyzer.ComputeStats(m.session)
		m.tabsView.RebuildTree()

		snapshotsCmd := m.snapshotsView.LoadAll()

		m.tabsView.deadChecking = true
		m.tabsView.githubChecking = true
		return m, tea.Batch(
			runDeadLinkChecks(m.session.AllTabs),
			runGitHubChecks(m.session.AllTabs),
			snapshotsCmd,
		)

	case analysisCompleteMsg:
		m.tabsView.deadChecking = false
		m.tabsView.stats = analyzer.ComputeStats(m.session)
		return m, nil

	case githubAnalysisCompleteMsg:
		m.tabsView.githubChecking = false
		m.tabsView.stats = analyzer.ComputeStats(m.session)
		return m, nil

	case summarizeCompleteMsg:
		job := m.tabsView.summarizeJobs[msg.url]
		popupID := ""
		if job != nil {
			popupID = job.PopupRequestID
		}
		delete(m.tabsView.summarizeJobs, msg.url)
		if msg.err != nil {
			m.tabsView.summarizeErrors[msg.url] = msg.err.Error()
			if popupID != "" {
				m.server.Send(server.OutgoingMsg{
					ID:     popupID,
					Action: "summarize-result",
					Error:  msg.err.Error(),
				})
			}
		} else {
			delete(m.tabsView.summarizeErrors, msg.url)
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
			m.tabsView.signalErrors[msg.source] = msg.err.Error()
		} else {
			applog.Info("tui.signal", "source", msg.source)
			delete(m.tabsView.signalErrors, msg.source)
		}
		if m.tabsView.signalSource != "" {
			m.tabsView.signals, _ = storage.ListSignals(m.db, m.tabsView.signalSource, true)
		}
		m.tabsView.tree.SignalCounts, _ = storage.ActiveSignalCounts(m.db)
		var cmds []tea.Cmd
		cmds = append(cmds, m.tabsView.processNextSignal())
		if m.activeView == ViewSignals {
			cmds = append(cmds, m.signalsView.Reload())
		}
		return m, tea.Batch(cmds...)

	case signalActionMsg:
		if msg.err != nil {
			m.tabsView.signalErrors[msg.source] = msg.err.Error()
		}
		if m.tabsView.signalSource != "" {
			m.tabsView.signals, _ = storage.ListSignals(m.db, m.tabsView.signalSource, true)
			if m.tabsView.signalCursor >= len(m.tabsView.signals) {
				m.tabsView.signalCursor = len(m.tabsView.signals) - 1
			}
			if m.tabsView.signalCursor < 0 {
				m.tabsView.signalCursor = 0
			}
		}
		m.tabsView.tree.SignalCounts, _ = storage.ActiveSignalCounts(m.db)
		if m.activeView == ViewSignals {
			v, cmd := m.signalsView.Update(msg)
			m.signalsView = v
			return m, cmd
		}
		return m, nil

	case signalNavigateMsg:
		if m.mode == ModeLive && m.connected {
			tab := m.tabsView.findTabForSource(msg.Source)
			if tab != nil && tab.BrowserID != 0 {
				return m, navigateSignalCmd(m.server, tab.BrowserID, msg.Source, msg.Title)
			}
		}
		return m, nil

	case signalPollTickMsg:
		return m, m.tabsView.queueSignalPoll()

	case rebuildTickMsg:
		m.doRebuild()
		return m, nil

	// --- WebSocket messages ---
	case wsSnapshotMsg:
		m.loading = false
		m.connected = true
		m.session = msg.data
		m.tabsView.session = m.session
		m.tabsView.mode = m.mode
		m.tabsView.connected = m.connected
		applog.Info("tui.snapshot", "tabs", len(msg.data.AllTabs), "groups", len(msg.data.Groups))

		analyzer.AnalyzeStale(m.session.AllTabs, m.staleDays)
		analyzer.AnalyzeDuplicates(m.session.AllTabs)
		m.tabsView.stats = analyzer.ComputeStats(m.session)
		m.tabsView.RebuildTree()

		m.tabsView.deadChecking = true
		m.tabsView.githubChecking = true
		return m, tea.Batch(
			runDeadLinkChecks(m.session.AllTabs),
			runGitHubChecks(m.session.AllTabs),
			listenWebSocket(m.server),
			signalPollTick(),
		)

	case wsDisconnectedMsg:
		m.connected = false
		m.tabsView.connected = false
		if m.tabsView.signalActive != nil {
			m.tabsView.signalErrors[m.tabsView.signalActive.Source] = "disconnected"
			m.tabsView.signalActive = nil
		}
		m.tabsView.signalQueue = nil
		var cmds []tea.Cmd
		for _, job := range m.tabsView.summarizeJobs {
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
			return m, tea.Batch(listenWebSocket(m.server), m.scheduleRebuild())
		}
		return m, listenWebSocket(m.server)

	case wsTabCreatedMsg:
		if m.session != nil {
			m.addTab(msg.tab)
			return m, tea.Batch(listenWebSocket(m.server), m.scheduleRebuild())
		}
		return m, listenWebSocket(m.server)

	case wsTabUpdatedMsg:
		if m.session != nil {
			m.updateTab(msg.tab)
			return m, tea.Batch(listenWebSocket(m.server), m.scheduleRebuild())
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
				ID:     msg.id,
				Action: "summarize-result",
				Error:  "Tab not found",
			})
			return m, listenWebSocket(m.server)
		}
		if existing, ok := m.tabsView.summarizeJobs[tab.URL]; ok {
			existing.PopupRequestID = msg.id
			return m, listenWebSocket(m.server)
		}
		job := &SummarizeJob{Tab: tab, PopupRequestID: msg.id}
		m.tabsView.summarizeJobs[tab.URL] = job
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
		if m.tabsView.signalActive != nil && m.tabsView.signalActive.ContentID == msg.id {
			source := m.tabsView.signalActive.Source
			m.tabsView.signalActive = nil
			applog.Info("signal.extResponse", "source", source, "ok", msg.ok, "error", msg.error, "rawLen", len(msg.items))
			applog.Info("signal.extItems", "source", source, "raw", msg.items)
			if !msg.ok {
				applog.Error("signal.extResponse.fail", fmt.Errorf("%s", msg.error), "source", source)
				m.tabsView.signalErrors[source] = msg.error
				return m, tea.Batch(listenWebSocket(m.server), m.tabsView.processNextSignal())
			}
			items, err := signal.ParseItemsJSON(msg.items)
			if err != nil {
				applog.Error("signal.parsed.fail", err, "source", source)
				m.tabsView.signalErrors[source] = err.Error()
				return m, tea.Batch(listenWebSocket(m.server), m.tabsView.processNextSignal())
			}
			applog.Info("signal.parsed", "source", source, "count", len(items))
			return m, tea.Batch(
				listenWebSocket(m.server),
				runReconcileSignals(m.db, source, items, time.Now()),
			)
		}
		for _, job := range m.tabsView.summarizeJobs {
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

// --- Modal handlers ---

func (m Model) updateGroupPicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		m.groupPicker.MoveUp()
	case "down", "j":
		m.groupPicker.MoveDown()
	case "enter":
		group := m.groupPicker.Selected()
		if group != nil {
			ids := m.tabsView.selectedOrCurrentTabIDs()
			groupID, _ := strconv.Atoi(group.ID)
			m.showGroupPicker = false
			m.tabsView.selected = make(map[int]bool)
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

func (m Model) updateFilterPicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		m.filterPicker.MoveUp()
	case "down", "j":
		m.filterPicker.MoveDown()
	case "enter":
		m.tabsView.tree.SetFilter(m.filterPicker.Selected().Mode)
		m.showFilterPicker = false
	case "esc":
		m.showFilterPicker = false
	case "q", "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) updateSourcePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

// --- View ---

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

	// Navbar
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
		statsStr = m.tabsView.StatsString()
	}
	var viewCounts [3]int
	viewCounts[ViewTabs] = m.tabsView.stats.TotalTabs
	for _, c := range m.tabsView.tree.SignalCounts {
		viewCounts[ViewSignals] += c
	}
	viewCounts[ViewSnapshots] = len(m.snapshotsView.snapshots)
	navbar := renderNavbar(m.activeView, profileName, viewCounts, statsStr, m.width)

	// Pane content
	treeWidth := m.width * TreeWidthPct / 100
	detailWidth := m.width - treeWidth - 3
	paneHeight := m.height - 4

	var leftContent, rightContent string
	var isFocusDetail bool

	switch m.activeView {
	case ViewTabs:
		isFocusDetail = m.tabsView.FocusDetail()
		leftContent = m.tabsView.ViewList()
		rightContent = m.tabsView.ViewDetail()

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
		bottomText = m.tabsView.BottomBar()
	case ViewSignals:
		bottomText = "\u2191\u2193/jk navigate \u00b7 \u21b5 open \u00b7 tab focus \u00b7 x complete \u00b7 u reopen \u00b7 1-3 view \u00b7 p source \u00b7 q quit"
	case ViewSnapshots:
		bottomText = "\u2191\u2193/jk navigate \u00b7 tab focus \u00b7 1-3 view \u00b7 p source \u00b7 q quit"
	}
	bottomBar := bottomBarStyle.Render(bottomText)

	return lipgloss.JoinVertical(lipgloss.Left, navbar, panes, bottomBar)
}

// --- Session mutation helpers ---

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
	delete(m.tabsView.selected, browserID)
}

func (m *Model) addTab(tab *types.Tab) {
	m.session.AllTabs = append(m.session.AllTabs, tab)
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
	sumPath := summarize.SummaryPath(m.summaryDir, tab.URL, tab.Title)
	if raw, err := summarize.ReadSummary(sumPath); err == nil {
		payload.Summary = raw
	}
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
