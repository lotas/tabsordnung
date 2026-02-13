package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

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

	b.WriteString("\n" + normalStyle.Render("\u2191\u2193 navigate \u00b7 enter confirm \u00b7 esc cancel"))

	return boxStyle.Render(b.String())
}
