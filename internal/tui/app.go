package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/nickel-chromium/tabsordnung/internal/analyzer"
	"github.com/nickel-chromium/tabsordnung/internal/firefox"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// --- Messages ---

type sessionLoadedMsg struct {
	data *types.SessionData
	err  error
}

type analysisCompleteMsg struct{}

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
	picker     ProfilePicker
	showPicker bool
	loading    bool
	err        error
	width      int
	height     int

	// Dead link checking
	deadChecking bool
}

func NewModel(profiles []types.Profile, staleDays int) Model {
	m := Model{
		profiles:  profiles,
		staleDays: staleDays,
	}
	if len(profiles) == 1 {
		m.loading = true
	} else {
		m.showPicker = true
		m.picker = NewProfilePicker(profiles)
	}
	return m
}

func (m Model) Init() tea.Cmd {
	if len(m.profiles) == 1 {
		// Return command to load the single profile. The profile field will be
		// set when sessionLoadedMsg arrives (via data.Profile), so the value
		// receiver issue is avoided.
		return loadSession(m.profiles[0])
	}
	// Multiple profiles: show picker (handled in View via showPicker logic)
	return nil
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
		// Profile picker mode
		if m.showPicker {
			switch msg.String() {
			case "up", "k":
				m.picker.MoveUp()
			case "down", "j":
				m.picker.MoveDown()
			case "enter":
				m.profile = m.picker.Selected()
				m.showPicker = false
				m.loading = true
				return m, loadSession(m.profile)
			case "esc":
				if m.session != nil {
					m.showPicker = false
				}
			case "q", "ctrl+c":
				return m, tea.Quit
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
			m.tree.Toggle()
		case "f":
			m.tree.Filter = (m.tree.Filter + 1) % 4
			m.tree.Cursor = 0
			m.tree.Offset = 0
		case "r":
			m.loading = true
			return m, loadSession(m.profile)
		case "p":
			m.showPicker = true
			m.picker = NewProfilePicker(m.profiles)
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
		m.tree = NewTreeModel(m.session.Groups)
		m.tree.Width = m.width * 60 / 100
		m.tree.Height = m.height - 5

		// Start dead link checks async
		m.deadChecking = true
		return m, runDeadLinkChecks(m.session.AllTabs)

	case analysisCompleteMsg:
		m.deadChecking = false
		m.stats = analyzer.ComputeStats(m.session)
		return m, nil
	}

	return m, nil
}

func (m Model) View() string {
	if m.loading {
		return "\n  Loading session data...\n"
	}

	if m.showPicker {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.picker.View())
	}

	if m.err != nil {
		return fmt.Sprintf("\n  Error: %v\n\n  Press 'p' to pick a profile, 'q' to quit.\n", m.err)
	}

	if m.session == nil {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.picker.View())
	}

	// Top bar
	topBarStyle := lipgloss.NewStyle().Bold(true).Padding(0, 1)
	profileStr := fmt.Sprintf("Profile: %s", m.profile.Name)
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
	filterNames := []string{"all", "stale", "dead", "duplicate"}
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

	left := treeBorder.Render(m.tree.View())
	right := detailBorder.Render(detailContent)
	panes := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	// Bottom bar
	bottomBarStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Padding(0, 1)
	bottomBar := bottomBarStyle.Render(
		"↑↓/jk navigate · enter expand · f filter · r refresh · p profile · q quit  " + filterStr,
	)

	return lipgloss.JoinVertical(lipgloss.Left, topBar, panes, bottomBar)
}
