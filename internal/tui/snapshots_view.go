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

type snapshotNode struct {
	IsHeader bool
	Header   string
	DayKey   string // "2026-03-13" used for expand/collapse
	Snapshot *storage.SnapshotSummary
}

type SnapshotsView struct {
	db        *sql.DB
	snapshots []storage.SnapshotSummary
	nodes     []snapshotNode
	selected  *storage.SnapshotFull
	cursor    int
	offset    int
	detail    DetailModel
	width     int
	treeWidth int
	height    int
	loading   bool
	loaded    bool
	err       error

	// Tree state
	dayExpanded map[string]bool

	// Right pane state
	groupExpanded map[string]bool
	focusDetail   bool
}

func NewSnapshotsView(db *sql.DB) SnapshotsView {
	return SnapshotsView{
		db:            db,
		dayExpanded:   make(map[string]bool),
		groupExpanded: make(map[string]bool),
	}
}

func (v SnapshotsView) Init() tea.Cmd { return nil }

func (v *SnapshotsView) LoadAll() tea.Cmd {
	v.cursor = 0
	v.offset = 0
	v.selected = nil
	v.loading = true
	v.loaded = false
	return v.loadSnapshots()
}

func (v *SnapshotsView) loadSnapshots() tea.Cmd {
	db := v.db
	return func() tea.Msg {
		snaps, err := storage.ListSnapshots(db)
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
	v.treeWidth = w * TreeWidthPct / 100
	v.detail.Width = w - v.treeWidth - 4
	v.detail.Height = h
}

func (v *SnapshotsView) buildNodes() {
	v.nodes = nil

	// Group snapshots by day
	type dayGroup struct {
		key       string // "2026-03-13"
		label     string // "2026-03-13 (Thu)"
		snapshots []*storage.SnapshotSummary
	}
	var days []*dayGroup
	dayMap := make(map[string]*dayGroup)

	for i := range v.snapshots {
		s := &v.snapshots[i]
		key := s.CreatedAt.Local().Format("2006-01-02")
		if _, ok := dayMap[key]; !ok {
			label := s.CreatedAt.Local().Format("2006-01-02 (Mon)")
			d := &dayGroup{key: key, label: label}
			dayMap[key] = d
			days = append(days, d)
		}
		dayMap[key].snapshots = append(dayMap[key].snapshots, s)
	}

	for _, d := range days {
		icon := "▸"
		if v.dayExpanded[d.key] {
			icon = "▼"
		}
		header := fmt.Sprintf("%s %s (%d)", icon, d.label, len(d.snapshots))
		v.nodes = append(v.nodes, snapshotNode{
			IsHeader: true,
			Header:   header,
			DayKey:   d.key,
		})
		if v.dayExpanded[d.key] {
			for _, s := range d.snapshots {
				v.nodes = append(v.nodes, snapshotNode{
					Snapshot: s,
					DayKey:   d.key,
				})
			}
		}
	}
}

func (v *SnapshotsView) selectedSnapshot() *storage.SnapshotSummary {
	if v.cursor >= 0 && v.cursor < len(v.nodes) {
		return v.nodes[v.cursor].Snapshot
	}
	return nil
}

func (v SnapshotsView) Update(msg tea.Msg) (SnapshotsView, tea.Cmd) {
	switch msg := msg.(type) {
	case snapshotsLoadedMsg:
		v.loading = false
		v.loaded = true
		if msg.err != nil {
			v.err = msg.err
			return v, nil
		}
		v.snapshots = msg.snapshots
		v.err = nil
		v.buildNodes()
		// Auto-select first snapshot if available
		for i, n := range v.nodes {
			if n.Snapshot != nil {
				v.cursor = i
				return v, v.loadDetail(n.Snapshot.Profile, n.Snapshot.Rev)
			}
		}
		return v, nil

	case snapshotDetailMsg:
		if msg.err != nil {
			v.err = msg.err
			return v, nil
		}
		v.selected = msg.snap
		// Auto-expand all groups in detail
		v.groupExpanded = make(map[string]bool)
		if msg.snap != nil {
			for _, g := range msg.snap.Groups {
				v.groupExpanded[g.Name] = true
			}
			v.groupExpanded["Ungrouped"] = true
		}
		v.detail.Scroll = 0
		v.detail.ContentLen = v.computeDetailLineCount()
		return v, nil

	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonLeft:
			onDetail := msg.X > v.treeWidth+1
			v.focusDetail = onDetail
			if !onDetail && len(v.nodes) > 0 {
				row := msg.Y - 2
				idx := v.offset + row
				if row >= 0 && row < v.height && idx >= 0 && idx < len(v.nodes) && idx != v.cursor {
					v.cursor = idx
					v.adjustOffset()
					node := v.nodes[v.cursor]
					if node.IsHeader {
						v.dayExpanded[node.DayKey] = !v.dayExpanded[node.DayKey]
						v.buildNodes()
						if v.cursor >= len(v.nodes) {
							v.cursor = len(v.nodes) - 1
						}
					} else if node.Snapshot != nil {
						return v, v.loadDetail(node.Snapshot.Profile, node.Snapshot.Rev)
					}
				}
			}
		case tea.MouseButtonWheelUp:
			onDetail := msg.X > v.treeWidth+1
			if onDetail {
				v.detail.ScrollUp()
			} else if v.cursor > 0 {
				v.cursor--
				v.adjustOffset()
				if s := v.selectedSnapshot(); s != nil {
					v.detail.Scroll = 0
					return v, v.loadDetail(s.Profile, s.Rev)
				}
			}
		case tea.MouseButtonWheelDown:
			onDetail := msg.X > v.treeWidth+1
			if onDetail {
				v.detail.ScrollDown()
			} else if v.cursor < len(v.nodes)-1 {
				v.cursor++
				v.adjustOffset()
				if s := v.selectedSnapshot(); s != nil {
					v.detail.Scroll = 0
					return v, v.loadDetail(s.Profile, s.Rev)
				}
			}
		}
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
			if v.cursor < len(v.nodes)-1 {
				v.cursor++
				v.adjustOffset()
				v.detail.Scroll = 0
				if s := v.selectedSnapshot(); s != nil {
					return v, v.loadDetail(s.Profile, s.Rev)
				}
			}
		case "k", "up":
			if v.cursor > 0 {
				v.cursor--
				v.adjustOffset()
				v.detail.Scroll = 0
				if s := v.selectedSnapshot(); s != nil {
					return v, v.loadDetail(s.Profile, s.Rev)
				}
			}
		case "h":
			// Collapse current day or jump to parent header
			if v.cursor >= 0 && v.cursor < len(v.nodes) {
				node := v.nodes[v.cursor]
				if node.IsHeader {
					v.dayExpanded[node.DayKey] = false
					v.buildNodes()
				} else {
					// Jump to parent header
					for i := v.cursor - 1; i >= 0; i-- {
						if v.nodes[i].IsHeader {
							v.cursor = i
							v.adjustOffset()
							break
						}
					}
				}
			}
		case "l":
			// Expand header or move down
			if v.cursor >= 0 && v.cursor < len(v.nodes) {
				node := v.nodes[v.cursor]
				if node.IsHeader && !v.dayExpanded[node.DayKey] {
					v.dayExpanded[node.DayKey] = true
					v.buildNodes()
				} else if v.cursor < len(v.nodes)-1 {
					v.cursor++
					v.adjustOffset()
					v.detail.Scroll = 0
					if s := v.selectedSnapshot(); s != nil {
						return v, v.loadDetail(s.Profile, s.Rev)
					}
				}
			}
		case "enter", " ":
			if v.cursor >= 0 && v.cursor < len(v.nodes) {
				node := v.nodes[v.cursor]
				if node.IsHeader {
					v.dayExpanded[node.DayKey] = !v.dayExpanded[node.DayKey]
					v.buildNodes()
					if v.cursor >= len(v.nodes) {
						v.cursor = len(v.nodes) - 1
					}
				} else {
					v.focusDetail = true
				}
			}
		}
	}
	return v, nil
}

func (v *SnapshotsView) adjustOffset() {
	if v.cursor < v.offset {
		v.offset = v.cursor
	}
	visible := v.height
	if visible < 1 {
		visible = 1
	}
	if v.cursor >= v.offset+visible {
		v.offset = v.cursor - visible + 1
	}
}

func (v SnapshotsView) computeDetailLineCount() int {
	if v.selected == nil {
		return 0
	}
	lines := 3
	if v.selected.Name != "" {
		lines++
	}
	seen := make(map[string]bool)
	for _, tab := range v.selected.Tabs {
		gname := tab.GroupName
		if gname == "" {
			gname = "Ungrouped"
		}
		if !seen[gname] {
			seen[gname] = true
			lines += 2
		}
		lines++
	}
	return lines
}

func (v SnapshotsView) ViewList() string {
	if v.loading {
		return "Loading snapshots..."
	}
	if v.err != nil {
		return fmt.Sprintf("Error: %v", v.err)
	}
	if len(v.nodes) == 0 {
		return "No snapshots yet."
	}

	cursorStyle := lipgloss.NewStyle().Bold(true).Reverse(true)
	headerStyle := lipgloss.NewStyle().Bold(true)
	treeWidth := v.treeWidth

	var b strings.Builder
	end := v.offset + v.height
	if end > len(v.nodes) {
		end = len(v.nodes)
	}

	for i := v.offset; i < end; i++ {
		node := v.nodes[i]
		var line string

		if node.IsHeader {
			line = headerStyle.Render(truncateString(node.Header, treeWidth-1))
		} else if node.Snapshot != nil {
			s := node.Snapshot
			ts := s.CreatedAt.Local().Format("15:04")
			label := ""
			if s.Name != "" {
				label = " " + s.Name
			}
			line = fmt.Sprintf("    %s  %s  (%d tabs)%s", ts, s.Profile, s.TabCount, label)
			if len(line) > treeWidth {
				line = line[:treeWidth-1] + "…"
			}
		}

		if i == v.cursor {
			// Strip styling for width padding, re-render with cursor
			plain := line
			for len(plain) < treeWidth {
				plain += " "
			}
			line = cursorStyle.Render(plain)
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
	summaryLine := fmt.Sprintf("Rev %d · %s · %s · %d tabs",
		v.selected.Rev,
		v.selected.Profile,
		v.selected.CreatedAt.Local().Format("2006-01-02 15:04"),
		v.selected.TabCount)
	b.WriteString(truncateString(summaryLine, v.detail.Width) + "\n")
	if v.selected.Name != "" {
		b.WriteString(truncateString("Label: "+v.selected.Name, v.detail.Width) + "\n")
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
		groupHeader := fmt.Sprintf("▼ %s (%d tabs)", ge.name, len(ge.tabs))
		b.WriteString(groupStyle.Render(truncateString(groupHeader, v.detail.Width)) + "\n")
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
