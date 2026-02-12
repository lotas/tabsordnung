package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// DetailModel shows information about the selected item.
type DetailModel struct {
	Width  int
	Height int
}

func (m DetailModel) ViewTab(tab *types.Tab) string {
	if tab == nil {
		return ""
	}

	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	valueStyle := lipgloss.NewStyle()
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	staleWarnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)

	var b strings.Builder

	b.WriteString(labelStyle.Render("Title") + "\n")
	title := tab.Title
	if len(title) > m.Width-2 {
		title = title[:m.Width-3] + "…"
	}
	b.WriteString(valueStyle.Render(title) + "\n\n")

	b.WriteString(labelStyle.Render("URL") + "\n")
	url := tab.URL
	// Wrap long URLs
	for len(url) > m.Width-2 {
		b.WriteString(valueStyle.Render(url[:m.Width-2]) + "\n")
		url = url[m.Width-2:]
	}
	b.WriteString(valueStyle.Render(url) + "\n\n")

	b.WriteString(labelStyle.Render("Last Visited") + "\n")
	age := time.Since(tab.LastAccessed)
	days := int(age.Hours() / 24)
	var ageStr string
	if days == 0 {
		hours := int(age.Hours())
		if hours == 0 {
			ageStr = "just now"
		} else {
			ageStr = fmt.Sprintf("%d hours ago", hours)
		}
	} else {
		ageStr = fmt.Sprintf("%d days ago", days)
	}
	b.WriteString(valueStyle.Render(ageStr) + "\n\n")

	// Status section
	var statuses []string
	if tab.IsDead {
		statuses = append(statuses, warnStyle.Render(fmt.Sprintf("Dead link (%s)", tab.DeadReason)))
	}
	if tab.IsStale {
		statuses = append(statuses, staleWarnStyle.Render(fmt.Sprintf("Stale (%d days)", tab.StaleDays)))
	}
	if tab.IsDuplicate {
		statuses = append(statuses, lipgloss.NewStyle().
			Foreground(lipgloss.Color("33")).Bold(true).
			Render(fmt.Sprintf("Duplicate (%d copies)", len(tab.DuplicateOf)+1)))
	}

	if len(statuses) > 0 {
		b.WriteString(labelStyle.Render("Status") + "\n")
		for _, s := range statuses {
			b.WriteString(s + "\n")
		}
	}

	return b.String()
}

func (m DetailModel) ViewGroup(group *types.TabGroup) string {
	if group == nil {
		return ""
	}

	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	valueStyle := lipgloss.NewStyle()

	var b strings.Builder

	b.WriteString(labelStyle.Render("Group") + "\n")
	b.WriteString(valueStyle.Render(group.Name) + "\n\n")

	b.WriteString(labelStyle.Render("Tabs") + "\n")
	b.WriteString(valueStyle.Render(fmt.Sprintf("%d", len(group.Tabs))) + "\n\n")

	b.WriteString(labelStyle.Render("Color") + "\n")
	b.WriteString(valueStyle.Render(group.Color) + "\n\n")

	state := "expanded"
	if group.Collapsed {
		state = "collapsed"
	}
	b.WriteString(labelStyle.Render("State") + "\n")
	b.WriteString(valueStyle.Render(state) + "\n")

	// Count issues in group
	var stale, dead, dup int
	for _, tab := range group.Tabs {
		if tab.IsStale {
			stale++
		}
		if tab.IsDead {
			dead++
		}
		if tab.IsDuplicate {
			dup++
		}
	}

	if stale+dead+dup > 0 {
		b.WriteString("\n" + labelStyle.Render("Issues") + "\n")
		if dead > 0 {
			b.WriteString(fmt.Sprintf("  %d dead links\n", dead))
		}
		if stale > 0 {
			b.WriteString(fmt.Sprintf("  %d stale tabs\n", stale))
		}
		if dup > 0 {
			b.WriteString(fmt.Sprintf("  %d duplicates\n", dup))
		}
	}

	return b.String()
}
