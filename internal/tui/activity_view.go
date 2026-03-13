package tui

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lotas/tabsordnung/internal/storage"
)

type activityPeriodsLoadedMsg struct {
	kind    storage.ActivityPeriodKind
	periods []storage.ActivityPeriodSummary
	err     error
}

type activityDetailLoadedMsg struct {
	kind    storage.ActivityPeriodKind
	period  *storage.ActivityPeriodSummary
	visits  []storage.TabVisitSummary
	signals []storage.SignalRecord
	err     error
}

type ActivityView struct {
	db      *sql.DB
	kind    storage.ActivityPeriodKind
	periods []storage.ActivityPeriodSummary
	cursor  int
	offset  int
	detail  DetailModel
	width   int
	height  int
	loading bool
	loaded  bool
	err     error

	selected *storage.ActivityPeriodSummary
	visits   []storage.TabVisitSummary
	signals  []storage.SignalRecord

	focusDetail bool
}

func NewActivityView(db *sql.DB) ActivityView {
	return ActivityView{
		db:   db,
		kind: storage.ActivityPeriodDay,
	}
}

func (v ActivityView) Init() tea.Cmd { return nil }

func (v *ActivityView) LoadPeriods() tea.Cmd {
	v.loading = true
	v.loaded = false
	v.err = nil
	v.cursor = 0
	v.offset = 0
	v.selected = nil
	v.visits = nil
	v.signals = nil
	return v.loadPeriods(v.kind)
}

func (v *ActivityView) RefreshPeriods() tea.Cmd {
	return v.loadPeriods(v.kind)
}

func (v *ActivityView) loadPeriods(kind storage.ActivityPeriodKind) tea.Cmd {
	db := v.db
	return func() tea.Msg {
		periods, err := storage.ListActivityPeriods(db, kind, time.Local)
		return activityPeriodsLoadedMsg{kind: kind, periods: periods, err: err}
	}
}

func (v *ActivityView) loadDetail(period storage.ActivityPeriodSummary) tea.Cmd {
	db := v.db
	kind := v.kind
	return func() tea.Msg {
		visits, err := storage.QueryTabVisitSummary(db, period.From, period.To)
		if err != nil {
			return activityDetailLoadedMsg{kind: kind, period: &period, err: err}
		}
		signals, err := storage.ListSignalsInRange(db, period.From, period.To)
		return activityDetailLoadedMsg{
			kind:    kind,
			period:  &period,
			visits:  visits,
			signals: signals,
			err:     err,
		}
	}
}

func (v *ActivityView) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.detail.Width = w - (w * TreeWidthPct / 100) - 4
	v.detail.Height = h
}

func (v ActivityView) FocusDetail() bool { return v.focusDetail }

func (v ActivityView) Update(msg tea.Msg) (ActivityView, tea.Cmd) {
	switch msg := msg.(type) {
	case activityPeriodsLoadedMsg:
		if msg.kind != v.kind {
			return v, nil
		}
		v.loading = false
		v.loaded = true
		if msg.err != nil {
			v.err = msg.err
			return v, nil
		}
		v.err = nil
		v.periods = msg.periods
		if len(v.periods) == 0 {
			v.cursor = 0
			v.selected = nil
			v.visits = nil
			v.signals = nil
			return v, nil
		}
		if v.cursor >= len(v.periods) {
			v.cursor = len(v.periods) - 1
		}
		if v.cursor < 0 {
			v.cursor = 0
		}
		return v, v.loadDetail(v.periods[v.cursor])

	case activityDetailLoadedMsg:
		if msg.kind != v.kind {
			return v, nil
		}
		if msg.err != nil {
			v.err = msg.err
			return v, nil
		}
		v.err = nil
		v.selected = msg.period
		v.visits = msg.visits
		v.signals = msg.signals
		v.detail.Scroll = 0
		v.detail.ContentLen = v.computeDetailLineCount()
		return v, nil

	case tea.MouseMsg:
		treeWidth := v.width * TreeWidthPct / 100
		onDetail := msg.X > treeWidth+1
		switch msg.Button {
		case tea.MouseButtonLeft:
			v.focusDetail = onDetail
			if !onDetail && len(v.periods) > 0 {
				row := msg.Y - 3
				idx := v.offset + row
				if row >= 0 && idx >= 0 && idx < len(v.periods) && idx != v.cursor {
					v.cursor = idx
					v.adjustOffset()
					return v, v.loadDetail(v.periods[v.cursor])
				}
			}
		case tea.MouseButtonWheelUp:
			if onDetail {
				v.focusDetail = true
				v.detail.ScrollUp()
			} else if v.cursor > 0 {
				v.focusDetail = false
				v.cursor--
				v.adjustOffset()
				return v, v.loadDetail(v.periods[v.cursor])
			}
		case tea.MouseButtonWheelDown:
			if onDetail {
				v.focusDetail = true
				v.detail.ScrollDown()
			} else if v.cursor < len(v.periods)-1 {
				v.focusDetail = false
				v.cursor++
				v.adjustOffset()
				return v, v.loadDetail(v.periods[v.cursor])
			}
		}
		return v, nil

	case tea.KeyMsg:
		if v.focusDetail {
			switch msg.String() {
			case "esc":
				v.focusDetail = false
				v.detail.Scroll = 0
			case "j", "down", "pgdown":
				v.detail.ScrollDown()
			case "k", "up", "pgup":
				v.detail.ScrollUp()
			}
			return v, nil
		}

		switch msg.String() {
		case "j", "down":
			if v.cursor < len(v.periods)-1 {
				v.cursor++
				v.adjustOffset()
				return v, v.loadDetail(v.periods[v.cursor])
			}
		case "k", "up":
			if v.cursor > 0 {
				v.cursor--
				v.adjustOffset()
				return v, v.loadDetail(v.periods[v.cursor])
			}
		case "enter":
			if v.selected != nil {
				v.focusDetail = true
			}
		case "[":
			v.kind = prevActivityKind(v.kind)
			return v, v.LoadPeriods()
		case "]":
			v.kind = nextActivityKind(v.kind)
			return v, v.LoadPeriods()
		}
	}
	return v, nil
}

func prevActivityKind(kind storage.ActivityPeriodKind) storage.ActivityPeriodKind {
	switch kind {
	case storage.ActivityPeriodWeek:
		return storage.ActivityPeriodDay
	case storage.ActivityPeriodMonth:
		return storage.ActivityPeriodWeek
	default:
		return storage.ActivityPeriodMonth
	}
}

func nextActivityKind(kind storage.ActivityPeriodKind) storage.ActivityPeriodKind {
	switch kind {
	case storage.ActivityPeriodDay:
		return storage.ActivityPeriodWeek
	case storage.ActivityPeriodWeek:
		return storage.ActivityPeriodMonth
	default:
		return storage.ActivityPeriodDay
	}
}

func (v *ActivityView) adjustOffset() {
	if v.cursor < v.offset {
		v.offset = v.cursor
	}
	visible := v.height - 2
	if visible < 1 {
		visible = 1
	}
	if v.cursor >= v.offset+visible {
		v.offset = v.cursor - visible + 1
	}
}

func (v ActivityView) computeDetailLineCount() int {
	// Header: "Activity\n", summary line + "\n\n", "Tabs\n" = 4 lines
	lines := 4
	if len(v.visits) == 0 {
		lines++ // "No tab activity recorded.\n"
	} else {
		lines += 2 * len(v.visits) // title + url per visit
	}
	// blank + "Signals (N)\n" = 2 lines
	lines += 2
	if len(v.signals) == 0 {
		lines++ // "No signals captured.\n"
	} else {
		lines += 2 * len(v.signals) // signal line + timestamp per signal
	}
	return lines
}

func (v ActivityView) ViewList() string {
	if v.loading {
		return "Loading activity..."
	}
	if v.err != nil {
		return fmt.Sprintf("Error: %v", v.err)
	}
	if len(v.periods) == 0 {
		return "No activity recorded yet."
	}

	cursorStyle := lipgloss.NewStyle().Bold(true).Reverse(true)
	modeStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	countStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	var b strings.Builder
	b.WriteString(modeStyle.Render(fmt.Sprintf("Mode: %s", activityKindLabel(v.kind))))
	b.WriteString("\n\n")

	end := v.offset + max(v.height-2, 1)
	if end > len(v.periods) {
		end = len(v.periods)
	}
	treeWidth := v.width * TreeWidthPct / 100
	for i := v.offset; i < end; i++ {
		p := v.periods[i]
		line := fmt.Sprintf("  %-16s %s", p.Label, countStyle.Render(fmt.Sprintf("(%d)", p.VisitCount)))
		if i == v.cursor {
			for lipgloss.Width(line) < treeWidth {
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

func (v ActivityView) ViewDetail() string {
	if v.loading {
		return "Loading activity..."
	}
	if v.err != nil {
		return fmt.Sprintf("Error: %v", v.err)
	}
	if v.selected == nil {
		return "Select a period to inspect activity."
	}

	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	var totalMs int64
	var totalVisits int
	for _, visit := range v.visits {
		totalMs += visit.TotalMs
		totalVisits += visit.Visits
	}

	var b strings.Builder
	b.WriteString(labelStyle.Render("Activity") + "\n")
	b.WriteString(fmt.Sprintf("%s · %d visits · %d pages · %s total\n\n",
		v.selected.Label, totalVisits, len(v.visits), formatActivityDuration(totalMs)))

	b.WriteString(labelStyle.Render("Tabs") + "\n")
	if len(v.visits) == 0 {
		b.WriteString("No tab activity recorded.\n")
	} else {
		for _, visit := range v.visits {
			titleWidth := max(v.detail.Width-34, 12)
			title := truncateString(visit.Title, titleWidth)
			url := truncateString(visit.URL, max(v.detail.Width-18, 24))
			b.WriteString(fmt.Sprintf("%4d  %-7s  %s\n", visit.Visits, formatActivityDuration(visit.TotalMs), title))
			b.WriteString(dimStyle.Render("                 "+url) + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(labelStyle.Render(fmt.Sprintf("Signals (%d)", len(v.signals))) + "\n")
	if len(v.signals) == 0 {
		b.WriteString("No signals captured.\n")
	} else {
		for _, sig := range v.signals {
			line := fmt.Sprintf("[%s] %s", sig.Source, sig.Title)
			if sig.Preview != "" {
				line += " — " + sig.Preview
			}
			line = truncateString(line, max(v.detail.Width-8, 20))
			b.WriteString(line + "\n")
			b.WriteString(dimStyle.Render("  "+sig.CapturedAt.Local().Format("2006-01-02 15:04")) + "\n")
		}
	}

	content := b.String()
	scrollDetail := v.detail
	scrollDetail.Height -= 2
	if scrollDetail.Height < 1 {
		scrollDetail.Height = 1
	}
	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	maxScroll := totalLines - scrollDetail.Height
	if maxScroll < 0 {
		maxScroll = 0
	}
	scrollStyle := dimStyle
	if v.focusDetail {
		scrollStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("62"))
	}
	status := "Scroll 0/0"
	if maxScroll > 0 {
		status = fmt.Sprintf("Scroll %d/%d", v.detail.Scroll, maxScroll)
	}
	focus := "left pane"
	if v.focusDetail {
		focus = "detail"
	}
	header := scrollStyle.Render(fmt.Sprintf("%s · focus: %s · enter/click/wheel to scroll", status, focus))

	return header + "\n\n" + scrollDetail.ViewScrolled(content)
}

func truncateString(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	if maxLen == 1 {
		return "…"
	}
	return s[:maxLen-1] + "…"
}

func activityKindLabel(kind storage.ActivityPeriodKind) string {
	switch kind {
	case storage.ActivityPeriodWeek:
		return "Week"
	case storage.ActivityPeriodMonth:
		return "Month"
	default:
		return "Day"
	}
}

func formatActivityDuration(ms int64) string {
	secs := ms / 1000
	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
