package tui

import (
	"database/sql"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lotas/tabsordnung/internal/bugzilla"
	"github.com/lotas/tabsordnung/internal/storage"
)

type bugzillaViewLoadedMsg struct {
	entities []storage.BugzillaEntity
	err      error
}

type bugzillaRefreshDoneMsg struct{ err error }

type bugzillaNode struct {
	IsHeader bool
	Header   string
	Entity   *storage.BugzillaEntity
	Group    string // status bucket key used for tree expand/collapse
}

type BugzillaView struct {
	db       *sql.DB
	entities []storage.BugzillaEntity
	nodes    []bugzillaNode
	cursor   int
	offset   int
	detail   DetailModel
	width    int
	height   int
	loading  bool
	err      error

	treeMode       bool
	groupExpanded  map[string]bool
	focusDetail    bool
	filter         string
	discoveredHosts []string
}

func NewBugzillaView(db *sql.DB) BugzillaView {
	return BugzillaView{
		db:            db,
		groupExpanded: map[string]bool{},
	}
}

func (v *BugzillaView) Reload() tea.Cmd {
	v.loading = true
	db := v.db
	return func() tea.Msg {
		entities, err := storage.ListBugzillaEntities(db)
		return bugzillaViewLoadedMsg{entities: entities, err: err}
	}
}

func (v *BugzillaView) forceRefresh() tea.Cmd {
	db := v.db
	entities := v.entities
	return func() tea.Msg {
		err := bugzilla.RefreshEntities(db, entities, true)
		return bugzillaRefreshDoneMsg{err: err}
	}
}

func (v *BugzillaView) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.detail.Width = w - (w * TreeWidthPct / 100) - 3
	v.detail.Height = h
}

// bugzillaStatusBucket maps a Bugzilla status to a display group.
func bugzillaStatusBucket(status string) string {
	switch strings.ToUpper(status) {
	case "RESOLVED", "VERIFIED", "CLOSED":
		return "resolved"
	default:
		return "open"
	}
}

var bugzillaStatusOrder = []string{"open", "resolved"}

var bugzillaStatusLabels = map[string]string{
	"open":     "Open",
	"resolved": "Resolved",
}

func (v *BugzillaView) buildNodes() {
	v.nodes = nil

	// Track discovered hosts for filter cycling.
	hostSeen := make(map[string]bool)
	for _, e := range v.entities {
		hostSeen[e.Host] = true
	}
	v.discoveredHosts = v.discoveredHosts[:0]
	for host := range hostSeen {
		v.discoveredHosts = append(v.discoveredHosts, host)
	}

	var filtered []storage.BugzillaEntity
	for _, e := range v.entities {
		if v.filter != "" && e.Host != v.filter {
			continue
		}
		filtered = append(filtered, e)
	}

	if !v.treeMode {
		for i := range filtered {
			v.nodes = append(v.nodes, bugzillaNode{Entity: &filtered[i]})
		}
		return
	}

	// Tree mode: group by status bucket.
	buckets := make(map[string][]*storage.BugzillaEntity)
	for i := range filtered {
		e := &filtered[i]
		bucket := bugzillaStatusBucket(e.Status)
		buckets[bucket] = append(buckets[bucket], e)
	}

	for _, key := range bugzillaStatusOrder {
		list := buckets[key]
		if len(list) == 0 {
			continue
		}
		if _, ok := v.groupExpanded[key]; !ok {
			v.groupExpanded[key] = true
		}
		icon := "▸"
		if v.groupExpanded[key] {
			icon = "▼"
		}
		label := bugzillaStatusLabels[key]
		v.nodes = append(v.nodes, bugzillaNode{
			IsHeader: true,
			Header:   fmt.Sprintf("%s %s (%d)", icon, label, len(list)),
			Group:    key,
		})
		if v.groupExpanded[key] {
			for _, e := range list {
				v.nodes = append(v.nodes, bugzillaNode{Entity: e, Group: key})
			}
		}
	}
}

func (v *BugzillaView) selectedEntity() *storage.BugzillaEntity {
	if v.cursor >= 0 && v.cursor < len(v.nodes) {
		return v.nodes[v.cursor].Entity
	}
	return nil
}

func (v BugzillaView) Update(msg tea.Msg) (BugzillaView, tea.Cmd) {
	switch msg := msg.(type) {
	case bugzillaViewLoadedMsg:
		v.loading = false
		if msg.err != nil {
			v.err = msg.err
			return v, nil
		}
		v.err = nil
		v.entities = msg.entities
		v.buildNodes()
		if v.cursor >= len(v.nodes) {
			v.cursor = len(v.nodes) - 1
		}
		if v.cursor < 0 {
			v.cursor = 0
		}
		return v, nil

	case bugzillaRefreshDoneMsg:
		if msg.err != nil {
			v.err = msg.err
		}
		return v, v.Reload()

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
			}
		case "k", "up":
			if v.cursor > 0 {
				v.cursor--
				v.adjustOffset()
				v.detail.Scroll = 0
			}
		case "h":
			if v.cursor >= 0 && v.cursor < len(v.nodes) {
				node := v.nodes[v.cursor]
				if node.IsHeader {
					v.groupExpanded[node.Group] = false
					v.buildNodes()
				} else {
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
			if v.cursor >= 0 && v.cursor < len(v.nodes) {
				node := v.nodes[v.cursor]
				if node.IsHeader && !v.groupExpanded[node.Group] {
					v.groupExpanded[node.Group] = true
					v.buildNodes()
				} else if v.cursor < len(v.nodes)-1 {
					v.cursor++
					v.adjustOffset()
					v.detail.Scroll = 0
				}
			}
		case "enter", " ":
			if v.cursor >= 0 && v.cursor < len(v.nodes) && v.nodes[v.cursor].IsHeader {
				node := v.nodes[v.cursor]
				v.groupExpanded[node.Group] = !v.groupExpanded[node.Group]
				v.buildNodes()
			} else if v.selectedEntity() != nil {
				v.focusDetail = true
			}
		case "tab":
			v.focusDetail = true
		case "t":
			v.treeMode = !v.treeMode
			v.buildNodes()
		case "f":
			// Cycle filter through known hosts + none.
			if len(v.discoveredHosts) == 0 {
				v.filter = ""
			} else if v.filter == "" {
				v.filter = v.discoveredHosts[0]
			} else {
				next := ""
				for i, host := range v.discoveredHosts {
					if host == v.filter {
						if i+1 < len(v.discoveredHosts) {
							next = v.discoveredHosts[i+1]
						}
						break
					}
				}
				v.filter = next
			}
			v.buildNodes()
			if v.cursor >= len(v.nodes) {
				v.cursor = len(v.nodes) - 1
			}
			if v.cursor < 0 {
				v.cursor = 0
			}
		case "o":
			e := v.selectedEntity()
			if e != nil {
				return v, openBugzillaInBrowser(e)
			}
		case "r":
			return v, v.forceRefresh()
		}
	}
	return v, nil
}

func (v *BugzillaView) adjustOffset() {
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

func (v BugzillaView) ViewList() string {
	if v.loading {
		return "Loading Bugzilla issues..."
	}
	if v.err != nil {
		return fmt.Sprintf("Error: %v", v.err)
	}
	if len(v.nodes) == 0 {
		if v.filter != "" {
			return fmt.Sprintf("No Bugzilla issues matching filter: %s", v.filter)
		}
		return "No Bugzilla issues yet.\n\n  Bugzilla links are auto-detected\n  from tabs and signals."
	}

	treeWidth := v.width * TreeWidthPct / 100
	cursorStyle := lipgloss.NewStyle().Bold(true).Reverse(true)
	headerStyle := lipgloss.NewStyle().Bold(true)
	idStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("44"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	filterStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)

	var b strings.Builder
	if v.filter != "" {
		b.WriteString(filterStyle.Render(fmt.Sprintf("  Filter: %s", v.filter)) + "\n")
	}

	end := v.offset + v.height
	if v.filter != "" {
		end--
	}
	if end > len(v.nodes) {
		end = len(v.nodes)
	}

	for i := v.offset; i < end; i++ {
		node := v.nodes[i]
		var line string
		if node.IsHeader {
			line = headerStyle.Render(node.Header)
		} else {
			e := node.Entity
			indent := "  "
			if v.treeMode {
				indent = "    "
			}
			ref := fmt.Sprintf("#%d", e.BugID)
			// In tree mode, status is implied by the group header.
			statusStr := ""
			statusLen := 0
			if !v.treeMode && e.Status != "" {
				statusTag := " [" + e.Status + "]"
				statusLen = len(statusTag)
				switch e.Status {
				case "RESOLVED", "VERIFIED", "CLOSED":
					statusStr = " " + dimStyle.Render("["+e.Status+"]")
				default:
					statusStr = " " + idStyle.Render("["+e.Status+"]")
				}
			}
			titleStr := ""
			if e.Title != "" {
				// indent(2-4) + "● "(2) + ref + "  " + title + status must fit treeWidth
				maxTitle := treeWidth - len(indent) - 2 - len(ref) - 2 - statusLen
				t := e.Title
				if maxTitle > 3 && len(t) > maxTitle {
					t = t[:maxTitle-1] + "…"
				}
				if maxTitle > 0 {
					titleStr = "  " + t
				}
			}
			row := indent + idStyle.Render("●") + " " + idStyle.Render(ref) + titleStr + statusStr
			line = row
		}

		if i == v.cursor {
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

func (v BugzillaView) ViewDetail() string {
	e := v.selectedEntity()
	if e == nil {
		return ""
	}

	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	valueStyle := lipgloss.NewStyle()
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	headerBoldStyle := lipgloss.NewStyle().Bold(true)

	var b strings.Builder
	ref := fmt.Sprintf("%s#%d", e.Host, e.BugID)
	b.WriteString(headerBoldStyle.Render(ref) + "\n\n")

	if e.Title != "" {
		b.WriteString(labelStyle.Render("Title") + "\n")
		b.WriteString(valueStyle.Render(e.Title) + "\n\n")
	}

	url := fmt.Sprintf("https://%s/show_bug.cgi?id=%d", e.Host, e.BugID)
	b.WriteString(labelStyle.Render("URL") + "\n")
	b.WriteString(valueStyle.Render(url) + "\n\n")

	if e.Status != "" {
		b.WriteString(labelStyle.Render("Status") + "\n")
		statusText := e.Status
		if e.Resolution != "" {
			statusText += " (" + e.Resolution + ")"
		}
		b.WriteString(valueStyle.Render(statusText) + "\n\n")
	}

	if e.Assignee != "" {
		b.WriteString(labelStyle.Render("Assignee") + "\n")
		b.WriteString(valueStyle.Render(e.Assignee) + "\n\n")
	}

	b.WriteString(labelStyle.Render("First Seen") + "\n")
	b.WriteString(valueStyle.Render(e.FirstSeenAt.Local().Format("2006-01-02 15:04")) + "\n")
	b.WriteString(dimStyle.Render("Source: "+e.FirstSeenSource) + "\n\n")

	if e.LastRefreshedAt != nil {
		b.WriteString(labelStyle.Render("Last Refreshed") + "\n")
		b.WriteString(valueStyle.Render(e.LastRefreshedAt.Local().Format("2006-01-02 15:04")) + "\n\n")
	}

	if v.db != nil {
		events, err := storage.ListBugzillaEntityEvents(v.db, e.ID)
		if err == nil && len(events) > 0 {
			b.WriteString(labelStyle.Render("Timeline") + "\n")
			for _, ev := range events {
				ts := ev.CreatedAt.Local().Format("2006-01-02 15:04")
				detail := ev.Detail
				if detail == "" {
					detail = ev.EventType
				} else {
					detail = ev.EventType + ": " + detail
				}
				b.WriteString(dimStyle.Render(ts+" "+detail) + "\n")
			}
		}
	}

	return v.detail.ViewScrolled(b.String())
}

func (v BugzillaView) FocusDetail() bool { return v.focusDetail }

func openBugzillaInBrowser(e *storage.BugzillaEntity) tea.Cmd {
	return func() tea.Msg {
		url := fmt.Sprintf("https://%s/show_bug.cgi?id=%d", e.Host, e.BugID)
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", url)
		case "linux":
			cmd = exec.Command("xdg-open", url)
		default:
			cmd = exec.Command("open", url)
		}
		_ = cmd.Start()
		return nil
	}
}
