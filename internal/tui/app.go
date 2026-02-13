package tui

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/nickel-chromium/tabsordnung/internal/analyzer"
	"github.com/nickel-chromium/tabsordnung/internal/firefox"
	"github.com/nickel-chromium/tabsordnung/internal/server"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// --- Messages ---

type sessionLoadedMsg struct {
	data *types.SessionData
	err  error
}

type analysisCompleteMsg struct{}

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
	id    string
	ok    bool
	error string
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
}

func NewModel(profiles []types.Profile, staleDays int, liveMode bool, srv *server.Server) Model {
	m := Model{
		profiles:  profiles,
		staleDays: staleDays,
		selected:  make(map[int]bool),
		server:    srv,
		port:      srv.Port(),
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
			default:
				if msg.ID != "" && msg.OK != nil {
					return wsCmdResponseMsg{id: msg.ID, ok: *msg.OK, error: msg.Error}
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
		paneHeight := m.height - 5              // top bar + bottom bar
		m.tree.Width = treeWidth
		m.tree.Height = paneHeight
		m.detail.Width = detailWidth
		m.detail.Height = paneHeight
		m.picker.Width = m.width
		m.picker.Height = m.height
		return m, nil

	case tea.KeyMsg:
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
		case "down", "j":
			m.tree.MoveDown()
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
		case "h":
			m.tree.CollapseOrParent()
		case "l":
			m.tree.ExpandOrEnter()
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
			m.selected = make(map[int]bool)
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			m.picker = NewSourcePicker(m.profiles)
			n := int(msg.String()[0] - '0')
			if m.picker.SelectByNumber(n) {
				src := m.picker.Selected()
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

		// Start dead link checks async
		m.deadChecking = true
		return m, runDeadLinkChecks(m.session.AllTabs)

	case analysisCompleteMsg:
		m.deadChecking = false
		m.stats = analyzer.ComputeStats(m.session)
		return m, nil

	case wsSnapshotMsg:
		m.loading = false
		m.connected = true
		m.session = msg.data

		analyzer.AnalyzeStale(m.session.AllTabs, m.staleDays)
		analyzer.AnalyzeDuplicates(m.session.AllTabs)
		m.stats = analyzer.ComputeStats(m.session)

		m.rebuildTree()

		m.deadChecking = true
		return m, tea.Batch(
			runDeadLinkChecks(m.session.AllTabs),
			listenWebSocket(m.server),
		)

	case wsDisconnectedMsg:
		m.connected = false
		if m.mode == ModeLive && m.server != nil {
			return m, listenWebSocket(m.server)
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

	case wsCmdResponseMsg:
		return m, listenWebSocket(m.server)
	}

	return m, nil
}

func (m *Model) rebuildTree() {
	oldCursor := m.tree.Cursor
	oldOffset := m.tree.Offset
	oldExpanded := m.tree.Expanded
	oldFilter := m.tree.Filter
	oldSavedExpanded := m.tree.SavedExpanded

	m.tree = NewTreeModel(m.session.Groups)
	m.tree.Width = m.width * 60 / 100
	m.tree.Height = m.height - 5
	m.tree.Filter = oldFilter
	m.tree.SavedExpanded = oldSavedExpanded

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
	placed := false
	for _, g := range m.session.Groups {
		if g.ID == tab.GroupID {
			g.Tabs = append(g.Tabs, tab)
			placed = true
			break
		}
	}
	if !placed {
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

	if m.session == nil {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.picker.View())
	}

	// Top bar
	topBarStyle := lipgloss.NewStyle().Bold(true).Padding(0, 1)
	var profileStr string
	if m.mode == ModeLive {
		if m.connected {
			profileStr = "Live \u25cf connected"
		} else {
			profileStr = "Live \u25cb waiting..."
		}
	} else {
		profileStr = fmt.Sprintf("Profile: %s (offline)", m.profile.Name)
	}
	statsStr := fmt.Sprintf("%d tabs · %d groups", m.stats.TotalTabs, m.stats.TotalGroups)
	if m.stats.DeadTabs > 0 {
		statsStr += fmt.Sprintf(" · %d dead", m.stats.DeadTabs)
	}
	if m.stats.StaleTabs > 0 {
		statsStr += fmt.Sprintf(" · %d stale", m.stats.StaleTabs)
	}
	if m.stats.DuplicateTabs > 0 {
		statsStr += fmt.Sprintf(" · %d dup", m.stats.DuplicateTabs)
	}
	if m.deadChecking {
		statsStr += " · checking links..."
	}
	topBar := topBarStyle.Render(profileStr + "  " + statsStr)

	// Filter indicator
	filterNames := []string{"all", "stale", "dead", "duplicate", ">7d", ">30d", ">90d"}
	filterStr := fmt.Sprintf("[filter: %s]", filterNames[m.tree.Filter])

	// Panes
	treeBorder := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Width(m.tree.Width).
		Height(m.tree.Height)

	detailBorder := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Width(m.detail.Width).
		Height(m.detail.Height)

	// Render detail based on selection
	var detailContent string
	if node := m.tree.SelectedNode(); node != nil {
		if node.Tab != nil {
			detailContent = m.detail.ViewTab(node.Tab)
		} else if node.Group != nil {
			detailContent = m.detail.ViewGroup(node.Group)
		}
	}

	m.tree.Selected = m.selected
	left := treeBorder.Render(m.tree.View())
	right := detailBorder.Render(detailContent)
	panes := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	// Bottom bar
	bottomBarStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Padding(0, 1)
	var bottomText string
	if m.mode == ModeLive && m.connected {
		selCount := len(m.selected)
		if selCount > 0 {
			bottomText = fmt.Sprintf("%d selected \u00b7 x close \u00b7 g move \u00b7 esc clear \u00b7 ", selCount)
		}
		bottomText += "space select \u00b7 enter focus \u00b7 "
	}
	bottomText += "\u2191\u2193/jk navigate \u00b7 h/l collapse/expand \u00b7 f filter \u00b7 r refresh \u00b7 1-9 source \u00b7 q quit  " + filterStr
	bottomBar := bottomBarStyle.Render(bottomText)

	return lipgloss.JoinVertical(lipgloss.Left, topBar, panes, bottomBar)
}
