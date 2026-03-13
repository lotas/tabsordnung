package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

type ViewType int

const (
	ViewTabs ViewType = iota
	ViewSignals
	ViewGitHub
	ViewBugzilla
	ViewActivity
	ViewSnapshots
)

// TreeWidthPct is the percentage of terminal width used for the left (tree/list) pane.
const TreeWidthPct = 50

var viewNames = []string{"Tabs", "Signals", "GitHub", "Bugzilla", "Activity", "Snapshots"}

func renderNavbar(active ViewType, profileName string, counts [6]int, stats string, width int) string {
	activeStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("62")).Underline(true)
	inactiveStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	countStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	profileStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	statsStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	var tabs string
	for i, name := range viewNames {
		if i > 0 {
			tabs += inactiveStyle.Render(" │ ")
		}
		countSuffix := ""
		if counts[i] > 0 {
			countSuffix = fmt.Sprintf(" (%d)", counts[i])
		}
		if ViewType(i) == active {
			tabs += activeStyle.Render(name + countSuffix)
		} else {
			tabs += inactiveStyle.Render(name) + countStyle.Render(countSuffix)
		}
	}

	left := " " + tabs
	if stats != "" {
		left += "   " + statsStyle.Render(stats)
	}

	profile := profileStyle.Render("Profile: " + profileName)
	gap := width - lipgloss.Width(left) - lipgloss.Width(profile) - 2
	if gap < 1 {
		// Not enough room for profile — omit it to avoid wrapping
		gap = width - lipgloss.Width(left) - 1
		if gap < 1 {
			gap = 1
		}
		padding := lipgloss.NewStyle().Width(gap)
		return left + padding.Render("")
	}
	padding := lipgloss.NewStyle().Width(gap)

	return left + padding.Render("") + profile + " "
}

// navbarHitTest returns which view was clicked given an X coordinate on the navbar row.
// Returns -1 if the click didn't land on any tab.
func navbarHitTest(x int, counts [6]int) int {
	pos := 1 // leading space
	for i, name := range viewNames {
		if i > 0 {
			pos += 3 // " │ " separator
		}
		label := name
		if counts[i] > 0 {
			label += fmt.Sprintf(" (%d)", counts[i])
		}
		end := pos + len(label)
		if x >= pos && x < end {
			return i
		}
		pos = end
	}
	return -1
}
