package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/lotas/tabsordnung/internal/summarize"
	"github.com/lotas/tabsordnung/internal/types"
)

// TreeNode represents a visible row in the tree.
type TreeNode struct {
	Group *types.TabGroup // non-nil for group headers
	Tab   *types.Tab      // non-nil for tab rows
}

// TreeModel manages the collapsible tree view.
type TreeModel struct {
	Groups        []*types.TabGroup
	Expanded      map[string]bool // group ID -> expanded
	SavedExpanded map[string]bool // snapshot before filter override
	Selected      map[int]bool    // BrowserID -> selected
	SummaryDir    string          // path to summaries directory
	Cursor        int
	Offset        int // scroll offset
	Width         int
	Height        int
	Filter        types.FilterMode
}

func NewTreeModel(groups []*types.TabGroup) TreeModel {
	expanded := make(map[string]bool)
	for _, g := range groups {
		expanded[g.ID] = !g.Collapsed
	}
	return TreeModel{
		Groups:   groups,
		Expanded: expanded,
		Selected: make(map[int]bool),
	}
}

// VisibleNodes returns the flat list of currently visible nodes.
func (m TreeModel) VisibleNodes() []TreeNode {
	var nodes []TreeNode
	for _, g := range m.Groups {
		nodes = append(nodes, TreeNode{Group: g})
		if m.Expanded[g.ID] {
			for _, tab := range g.Tabs {
				if m.matchesFilter(tab) {
					nodes = append(nodes, TreeNode{Tab: tab})
				}
			}
		}
	}
	return nodes
}

func (m TreeModel) matchesFilter(tab *types.Tab) bool {
	switch m.Filter {
	case types.FilterStale:
		return tab.IsStale
	case types.FilterDead:
		return tab.IsDead
	case types.FilterDuplicate:
		return tab.IsDuplicate
	case types.FilterAge7:
		return tab.StaleDays > 7
	case types.FilterAge30:
		return tab.StaleDays > 30
	case types.FilterAge90:
		return tab.StaleDays > 90
	case types.FilterGitHubDone:
		return tab.GitHubStatus == "closed" || tab.GitHubStatus == "merged"
	case types.FilterHasSummary:
		if m.SummaryDir == "" {
			return false
		}
		p := summarize.SummaryPath(m.SummaryDir, tab.URL, tab.Title)
		_, err := os.Stat(p)
		return err == nil
	case types.FilterNoSummary:
		if m.SummaryDir == "" {
			return true
		}
		p := summarize.SummaryPath(m.SummaryDir, tab.URL, tab.Title)
		_, err := os.Stat(p)
		return err != nil
	default:
		return true
	}
}

// SetFilter changes the active filter and manages expanded-state save/restore.
func (m *TreeModel) SetFilter(f types.FilterMode) {
	prevFilter := m.Filter
	m.Filter = f

	if f != types.FilterAll {
		if prevFilter == types.FilterAll {
			// Save current expanded state before overriding.
			m.SavedExpanded = make(map[string]bool, len(m.Expanded))
			for id, exp := range m.Expanded {
				m.SavedExpanded[id] = exp
			}
		}
		// Force all groups expanded.
		for _, g := range m.Groups {
			m.Expanded[g.ID] = true
		}
	} else if m.SavedExpanded != nil {
		// Restore saved state when switching back to "all".
		for id, exp := range m.SavedExpanded {
			m.Expanded[id] = exp
		}
		m.SavedExpanded = nil
	}

	m.Cursor = 0
	m.Offset = 0
}

// SelectedNode returns the currently selected node, or nil.
func (m TreeModel) SelectedNode() *TreeNode {
	nodes := m.VisibleNodes()
	if m.Cursor >= 0 && m.Cursor < len(nodes) {
		return &nodes[m.Cursor]
	}
	return nil
}

// MoveUp moves the cursor up.
func (m *TreeModel) MoveUp() {
	if m.Cursor > 0 {
		m.Cursor--
	}
	if m.Cursor < m.Offset {
		m.Offset = m.Cursor
	}
}

// MoveDown moves the cursor down.
func (m *TreeModel) MoveDown() {
	nodes := m.VisibleNodes()
	if m.Cursor < len(nodes)-1 {
		m.Cursor++
	}
	visibleRows := m.Height - 2 // account for padding
	if visibleRows < 1 {
		visibleRows = 1
	}
	if m.Cursor >= m.Offset+visibleRows {
		m.Offset = m.Cursor - visibleRows + 1
	}
}

// Toggle expands/collapses the selected group.
func (m *TreeModel) Toggle() {
	node := m.SelectedNode()
	if node == nil || node.Group == nil {
		return
	}
	m.Expanded[node.Group.ID] = !m.Expanded[node.Group.ID]
}

// CollapseOrParent collapses the selected group if expanded, or jumps to the
// parent group header if the cursor is on a tab.
func (m *TreeModel) CollapseOrParent() {
	node := m.SelectedNode()
	if node == nil {
		return
	}
	if node.Group != nil {
		// On a group header: collapse it if expanded.
		if m.Expanded[node.Group.ID] {
			m.Expanded[node.Group.ID] = false
		}
		return
	}
	// On a tab: jump to the parent group header.
	nodes := m.VisibleNodes()
	for i := m.Cursor - 1; i >= 0; i-- {
		if nodes[i].Group != nil {
			m.Cursor = i
			if m.Cursor < m.Offset {
				m.Offset = m.Cursor
			}
			return
		}
	}
}

// ExpandOrEnter expands the selected group if collapsed, or moves into the
// first child tab if already expanded.
func (m *TreeModel) ExpandOrEnter() {
	node := m.SelectedNode()
	if node == nil || node.Group == nil {
		return
	}
	if !m.Expanded[node.Group.ID] {
		m.Expanded[node.Group.ID] = true
		return
	}
	// Already expanded: move to first child tab.
	nodes := m.VisibleNodes()
	if m.Cursor+1 < len(nodes) && nodes[m.Cursor+1].Tab != nil {
		m.Cursor++
		visibleRows := m.Height - 2
		if visibleRows < 1 {
			visibleRows = 1
		}
		if m.Cursor >= m.Offset+visibleRows {
			m.Offset = m.Cursor - visibleRows + 1
		}
	}
}

// View renders the tree.
func (m TreeModel) View() string {
	nodes := m.VisibleNodes()
	if len(nodes) == 0 {
		return "No tabs found."
	}

	visibleRows := m.Height
	if visibleRows < 1 {
		visibleRows = 20
	}

	var b strings.Builder
	end := m.Offset + visibleRows
	if end > len(nodes) {
		end = len(nodes)
	}

	cursorStyle := lipgloss.NewStyle().Bold(true).Reverse(true)
	staleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))    // orange
	deadStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))     // red
	dupStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("33"))       // blue
	ghDoneStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))    // green
	ghOpenStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("135"))   // purple
	summaryStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("cyan")) // cyan
	groupStyle := lipgloss.NewStyle().Bold(true)

	for i := m.Offset; i < end; i++ {
		node := nodes[i]
		var line string

		if node.Group != nil {
			icon := "▶"
			if m.Expanded[node.Group.ID] {
				icon = "▼"
			}
			var label string
			if m.Filter == types.FilterAll {
				label = fmt.Sprintf("%s %s (%d tabs)", icon, node.Group.Name, len(node.Group.Tabs))
			} else {
				matched := 0
				for _, tab := range node.Group.Tabs {
					if m.matchesFilter(tab) {
						matched++
					}
				}
				label = fmt.Sprintf("%s %s (%d/%d tabs)", icon, node.Group.Name, matched, len(node.Group.Tabs))
			}
			line = groupStyle.Render(label)
		} else if node.Tab != nil {
			prefix := "  "
			if m.Selected[node.Tab.BrowserID] {
				prefix = "\u25b8 "
			}
			var markers []string
			if node.Tab.IsDead {
				markers = append(markers, deadStyle.Render("●"))
			}
			if node.Tab.IsStale {
				markers = append(markers, staleStyle.Render("◷"))
			}
			if node.Tab.IsDuplicate {
				markers = append(markers, dupStyle.Render("⇄"))
			}
			if node.Tab.GitHubStatus == "closed" || node.Tab.GitHubStatus == "merged" {
				markers = append(markers, ghDoneStyle.Render("✓"))
			} else if node.Tab.GitHubStatus == "open" {
				markers = append(markers, ghOpenStyle.Render("○"))
			}
			if m.SummaryDir != "" {
				sumPath := summarize.SummaryPath(m.SummaryDir, node.Tab.URL, node.Tab.Title)
				if _, err := os.Stat(sumPath); err == nil {
					markers = append(markers, summaryStyle.Render("S"))
				}
			}

			marker := ""
			if len(markers) > 0 {
				marker = strings.Join(markers, "") + " "
			}

			// Truncate URL to fit width
			maxURLLen := m.Width - len(prefix) - len(marker) - 2
			if maxURLLen < 10 {
				maxURLLen = 10
			}
			url := node.Tab.URL
			if len(url) > maxURLLen {
				url = url[:maxURLLen-1] + "…"
			}
			line = prefix + marker + url
		}

		// Apply cursor highlight
		if i == m.Cursor {
			// Pad to full width for highlight
			for len(line) < m.Width {
				line += " "
			}
			line = cursorStyle.Render(line)
		}

		b.WriteString(line)
		if i < end-1 {
			b.WriteString("\n")
		}
	}

	return b.String()
}
