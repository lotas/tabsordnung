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

	b.WriteString("\n" + normalStyle.Render("\u2191\u2193 navigate \u00b7 enter select \u00b7 1-9 quick select"))

	return boxStyle.Render(b.String())
}
