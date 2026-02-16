package tui

import (
	"database/sql"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lotas/tabsordnung/internal/storage"
)

type snapshotsLoadedMsg struct {
	snapshots []storage.SnapshotSummary
	err       error
}

type snapshotDetailMsg struct {
	snap *storage.SnapshotFull
	err  error
}

type SnapshotsView struct {
	db        *sql.DB
	profile   string
	snapshots []storage.SnapshotSummary
	selected  *storage.SnapshotFull
	cursor    int
	offset    int
	detail    DetailModel
	width     int
	height    int
	loading   bool
	err       error

	// Right pane state
	groupExpanded map[string]bool
	focusDetail   bool
}

func NewSnapshotsView(db *sql.DB) SnapshotsView {
	return SnapshotsView{
		db:            db,
		groupExpanded: make(map[string]bool),
	}
}

func (v SnapshotsView) Init() tea.Cmd { return nil }

func (v *SnapshotsView) SetProfile(profile string) tea.Cmd {
	if v.profile == profile {
		return nil
	}
	v.profile = profile
	v.cursor = 0
	v.offset = 0
	v.selected = nil
	v.loading = true
	return v.loadSnapshots()
}

func (v *SnapshotsView) loadSnapshots() tea.Cmd {
	profile := v.profile
	db := v.db
	return func() tea.Msg {
		snaps, err := storage.ListSnapshotsByProfile(db, profile)
		return snapshotsLoadedMsg{snapshots: snaps, err: err}
	}
}

func (v *SnapshotsView) loadDetail(profile string, rev int) tea.Cmd {
	db := v.db
	return func() tea.Msg {
		snap, err := storage.GetSnapshot(db, profile, rev)
		return snapshotDetailMsg{snap: snap, err: err}
	}
}

func (v *SnapshotsView) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.detail.Width = w - (w * 60 / 100) - 3
	v.detail.Height = h
}

func (v SnapshotsView) Update(msg tea.Msg) (SnapshotsView, tea.Cmd) {
	switch msg := msg.(type) {
	case snapshotsLoadedMsg:
		v.loading = false
		if msg.err != nil {
			v.err = msg.err
			return v, nil
		}
		v.snapshots = msg.snapshots
		v.err = nil
		if len(v.snapshots) > 0 {
			return v, v.loadDetail(v.snapshots[0].Profile, v.snapshots[0].Rev)
		}
		return v, nil

	case snapshotDetailMsg:
		if msg.err != nil {
			v.err = msg.err
			return v, nil
		}
		v.selected = msg.snap
		// Auto-expand all groups
		v.groupExpanded = make(map[string]bool)
		if msg.snap != nil {
			for _, g := range msg.snap.Groups {
				v.groupExpanded[g.Name] = true
			}
			v.groupExpanded["Ungrouped"] = true
		}
		v.detail.Scroll = 0
		return v, nil

	case tea.KeyMsg:
		if v.focusDetail {
			switch msg.String() {
			case "esc":
				v.focusDetail = false
				v.detail.Scroll = 0
			case "j", "down":
				v.detail.ScrollDown()
			case "k", "up":
				v.detail.ScrollUp()
			}
			return v, nil
		}

		switch msg.String() {
		case "j", "down":
			if v.cursor < len(v.snapshots)-1 {
				v.cursor++
				v.adjustOffset()
				return v, v.loadDetail(v.snapshots[v.cursor].Profile, v.snapshots[v.cursor].Rev)
			}
		case "k", "up":
			if v.cursor > 0 {
				v.cursor--
				v.adjustOffset()
				return v, v.loadDetail(v.snapshots[v.cursor].Profile, v.snapshots[v.cursor].Rev)
			}
		case "enter":
			v.focusDetail = true
		}
	}
	return v, nil
}

func (v *SnapshotsView) adjustOffset() {
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

func (v SnapshotsView) ViewList() string {
	if v.loading {
		return "Loading snapshots..."
	}
	if v.err != nil {
		return fmt.Sprintf("Error: %v", v.err)
	}
	if len(v.snapshots) == 0 {
		return "No snapshots yet."
	}

	cursorStyle := lipgloss.NewStyle().Bold(true).Reverse(true)
	treeWidth := v.width * 60 / 100

	var b strings.Builder
	end := v.offset + v.height
	if end > len(v.snapshots) {
		end = len(v.snapshots)
	}

	for i := v.offset; i < end; i++ {
		s := v.snapshots[i]
		ts := s.CreatedAt.Local().Format("2006-01-02 15:04")
		label := ""
		if s.Name != "" {
			label = " " + s.Name
		}
		line := fmt.Sprintf("  %s  (%d tabs)%s", ts, s.TabCount, label)

		if i == v.cursor {
			for len(line) < treeWidth {
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

func (v SnapshotsView) ViewDetail() string {
	if v.selected == nil {
		return ""
	}

	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	groupStyle := lipgloss.NewStyle().Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	var b strings.Builder

	b.WriteString(labelStyle.Render("Snapshot") + "\n")
	b.WriteString(fmt.Sprintf("Rev %d · %s · %d tabs\n",
		v.selected.Rev,
		v.selected.CreatedAt.Local().Format("2006-01-02 15:04"),
		v.selected.TabCount))
	if v.selected.Name != "" {
		b.WriteString(fmt.Sprintf("Label: %s\n", v.selected.Name))
	}
	b.WriteString("\n")

	// Group tabs by group name
	type groupEntry struct {
		name string
		tabs []storage.SnapshotTab
	}
	groupMap := make(map[string]*groupEntry)
	var groupOrder []string

	for _, tab := range v.selected.Tabs {
		gname := tab.GroupName
		if gname == "" {
			gname = "Ungrouped"
		}
		if _, ok := groupMap[gname]; !ok {
			groupMap[gname] = &groupEntry{name: gname}
			groupOrder = append(groupOrder, gname)
		}
		groupMap[gname].tabs = append(groupMap[gname].tabs, tab)
	}

	for _, gname := range groupOrder {
		ge := groupMap[gname]
		b.WriteString(groupStyle.Render(fmt.Sprintf("▼ %s (%d tabs)", ge.name, len(ge.tabs))) + "\n")
		for _, tab := range ge.tabs {
			title := tab.Title
			maxLen := v.detail.Width - 6
			if maxLen > 0 && len(title) > maxLen {
				title = title[:maxLen-1] + "…"
			}
			b.WriteString(dimStyle.Render("    "+title) + "\n")
		}
		b.WriteString("\n")
	}

	content := b.String()
	return v.detail.ViewScrolled(content)
}

func (v SnapshotsView) FocusDetail() bool { return v.focusDetail }
