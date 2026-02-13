package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/lotas/tabsordnung/internal/types"
)

type FilterOption struct {
	Label string
	Mode  types.FilterMode
}

type FilterPicker struct {
	Options []FilterOption
	Cursor  int
	Width   int
	Height  int
}

func NewFilterPicker(current types.FilterMode) FilterPicker {
	options := []FilterOption{
		{"All tabs", types.FilterAll},
		{"Stale", types.FilterStale},
		{"Dead links", types.FilterDead},
		{"Duplicates", types.FilterDuplicate},
		{"Older than 7 days", types.FilterAge7},
		{"Older than 30 days", types.FilterAge30},
		{"Older than 90 days", types.FilterAge90},
		{"GitHub done", types.FilterGitHubDone},
	}
	cursor := 0
	for i, opt := range options {
		if opt.Mode == current {
			cursor = i
			break
		}
	}
	return FilterPicker{Options: options, Cursor: cursor}
}

func (m *FilterPicker) MoveUp() {
	if m.Cursor > 0 {
		m.Cursor--
	}
}

func (m *FilterPicker) MoveDown() {
	if m.Cursor < len(m.Options)-1 {
		m.Cursor++
	}
}

func (m FilterPicker) Selected() FilterOption {
	return m.Options[m.Cursor]
}

func (m FilterPicker) View() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Padding(0, 1)
	selectedStyle := lipgloss.NewStyle().Bold(true).Reverse(true).Padding(0, 1)
	normalStyle := lipgloss.NewStyle().Padding(0, 1)
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(1, 2)

	var b strings.Builder
	b.WriteString(titleStyle.Render("Select filter:") + "\n\n")

	for i, opt := range m.Options {
		label := opt.Label
		if i == m.Cursor {
			label = selectedStyle.Render(label)
		} else {
			label = normalStyle.Render("  " + label)
		}
		b.WriteString(label + "\n")
	}

	b.WriteString("\n" + normalStyle.Render("\u2191\u2193 navigate \u00b7 enter select \u00b7 esc cancel"))

	return boxStyle.Render(b.String())
}
