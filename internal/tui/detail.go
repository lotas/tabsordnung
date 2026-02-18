package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/lotas/tabsordnung/internal/storage"
	"github.com/lotas/tabsordnung/internal/types"
)

// DetailModel shows information about the selected item.
type DetailModel struct {
	Width      int
	Height     int
	Scroll     int    // scroll offset
	Content    string // rendered content (cached)
	ContentLen int    // total lines in content
}

// ScrollUp adjusts the scroll offset upward.
func (m *DetailModel) ScrollUp() {
	if m.Scroll > 0 {
		m.Scroll--
	}
}

// ScrollDown adjusts the scroll offset downward.
func (m *DetailModel) ScrollDown() {
	if m.Scroll < m.ContentLen-m.Height {
		m.Scroll++
	}
	if m.Scroll < 0 {
		m.Scroll = 0
	}
}

// ResetScroll resets the scroll offset to 0.
func (m *DetailModel) ResetScroll() {
	m.Scroll = 0
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
	if tab.GitHubStatus == "closed" || tab.GitHubStatus == "merged" {
		statuses = append(statuses, lipgloss.NewStyle().
			Foreground(lipgloss.Color("42")).Bold(true).
			Render(fmt.Sprintf("GitHub: %s", tab.GitHubStatus)))
	} else if tab.GitHubStatus == "open" {
		statuses = append(statuses, lipgloss.NewStyle().
			Foreground(lipgloss.Color("135")).Bold(true).
			Render("GitHub: open"))
	}

	if len(statuses) > 0 {
		b.WriteString(labelStyle.Render("Status") + "\n")
		for _, s := range statuses {
			b.WriteString(s + "\n")
		}
	}

	return b.String()
}

// ViewTabWithSummary renders tab info with optional summary content.
func (m *DetailModel) ViewTabWithSummary(tab *types.Tab, summary string, summarizing bool, summarizeErr string) string {
	base := m.ViewTab(tab)

	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	activeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))

	if summarizing {
		base += "\n" + activeStyle.Render("Summarizing... (fetching & processing)")
	} else if summary != "" {
		base += "\n" + labelStyle.Render("Summary") + "\n" + summary
		base += "\n" + dimStyle.Render("  Press 's' to re-summarize")
	} else if summarizeErr != "" {
		base += "\n" + errStyle.Render("Summarize failed: "+summarizeErr)
		base += "\n" + dimStyle.Render("  Press 's' to retry")
	} else {
		base += "\n" + dimStyle.Render("  Press 's' to summarize")
	}

	return base
}

// ViewTabWithSignal renders tab info with signal list from database.
func (m *DetailModel) ViewTabWithSignal(tab *types.Tab, signals []storage.SignalRecord, signalCursor int, capturing bool, signalErr string) string {
	base := m.ViewTab(tab)

	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	activeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	completedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	cursorStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("62"))
	urgentStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	reviewStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	fyiStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	unclassifiedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	if capturing {
		base += "\n" + activeStyle.Render("Capturing signal...")
	}

	if len(signals) > 0 {
		var activeCount, completedCount int
		for _, s := range signals {
			if s.CompletedAt == nil {
				activeCount++
			} else {
				completedCount++
			}
		}

		base += "\n" + labelStyle.Render(fmt.Sprintf("Signals — %d active, %d completed", activeCount, completedCount)) + "\n\n"

		for i, s := range signals {
			prefix := "  "
			if i == signalCursor {
				prefix = "> "
			}

			urgencyPrefix := unclassifiedStyle.Render("[?] ")
			if s.Urgency != nil {
				switch *s.Urgency {
				case "urgent":
					urgencyPrefix = urgentStyle.Render("[!] ")
				case "review":
					urgencyPrefix = reviewStyle.Render("[~] ")
				case "fyi":
					urgencyPrefix = fyiStyle.Render("[ ] ")
				}
			}

			age := formatSignalAge(s.CapturedAt)
			suffix := "  " + age
			line := s.Title
			if s.Preview != "" {
				line += " — " + s.Preview
			}

			// Truncate to fit within pane width (1 visual line per signal).
			maxLen := m.Width - len(prefix) - 4 - len(suffix) - 1
			if maxLen > 0 && len(line) > maxLen {
				line = line[:maxLen-1] + "…"
			}

			if i == signalCursor {
				base += cursorStyle.Render(prefix+urgencyPrefix+line+suffix) + "\n"
			} else if s.CompletedAt != nil {
				base += completedStyle.Render(prefix+"✓ "+line+suffix) + "\n"
			} else {
				base += prefix + urgencyPrefix + line + suffix + "\n"
			}
		}

		base += "\n" + dimStyle.Render("  c capture · ↵ open · x complete · u reopen")
	} else if signalErr != "" {
		base += "\n" + errStyle.Render("Signal failed: "+signalErr)
		base += "\n" + dimStyle.Render("  Press 'c' to retry")
	} else if !capturing {
		base += "\n" + dimStyle.Render("  Press 'c' to capture signal")
	}

	return base
}

func formatSignalAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// ViewScrolled applies scroll offset and height truncation to the content string.
func (m *DetailModel) ViewScrolled(content string) string {
	if content == "" {
		return content
	}

	lines := strings.Split(content, "\n")
	m.ContentLen = len(lines)

	// Clamp scroll
	maxScroll := m.ContentLen - m.Height
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.Scroll > maxScroll {
		m.Scroll = maxScroll
	}
	if m.Scroll < 0 {
		m.Scroll = 0
	}

	end := m.Scroll + m.Height
	if end > len(lines) {
		end = len(lines)
	}

	if m.Scroll >= len(lines) {
		return ""
	}

	return strings.Join(lines[m.Scroll:end], "\n")
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
