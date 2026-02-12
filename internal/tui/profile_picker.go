package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// ProfilePicker is an overlay for selecting a Firefox profile.
type ProfilePicker struct {
	Profiles []types.Profile
	Cursor   int
	Width    int
	Height   int
}

func NewProfilePicker(profiles []types.Profile) ProfilePicker {
	// Pre-select the default profile
	cursor := 0
	for i, p := range profiles {
		if p.IsDefault {
			cursor = i
			break
		}
	}
	return ProfilePicker{
		Profiles: profiles,
		Cursor:   cursor,
	}
}

func (m *ProfilePicker) MoveUp() {
	if m.Cursor > 0 {
		m.Cursor--
	}
}

func (m *ProfilePicker) MoveDown() {
	if m.Cursor < len(m.Profiles)-1 {
		m.Cursor++
	}
}

func (m ProfilePicker) Selected() types.Profile {
	return m.Profiles[m.Cursor]
}

func (m ProfilePicker) View() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Padding(0, 1)
	selectedStyle := lipgloss.NewStyle().Bold(true).Reverse(true).Padding(0, 1)
	normalStyle := lipgloss.NewStyle().Padding(0, 1)
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(1, 2)

	var b strings.Builder
	b.WriteString(titleStyle.Render("Select a Firefox profile:") + "\n\n")

	for i, p := range m.Profiles {
		label := p.Name
		if p.IsDefault {
			label += " (default)"
		}
		line := fmt.Sprintf("  %s", label)
		if i == m.Cursor {
			line = selectedStyle.Render("> " + label)
		} else {
			line = normalStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}

	b.WriteString("\n" + normalStyle.Render("\u2191\u2193 navigate \u00b7 enter select \u00b7 esc cancel"))

	return boxStyle.Render(b.String())
}
