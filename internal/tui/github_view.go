package tui

import (
	"database/sql"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lotas/tabsordnung/internal/github"
	"github.com/lotas/tabsordnung/internal/storage"
)

// --- Messages ---

type githubViewLoadedMsg struct {
	entities []storage.GitHubEntity
	err      error
}

type githubRefreshDoneMsg struct{ err error }

// --- Node type for tree/flat rendering ---

type githubNode struct {
	IsHeader bool
	Header   string
	Entity   *storage.GitHubEntity
	State    string // state group key for tree mode
}

// --- GitHubView ---

type GitHubView struct {
	db       *sql.DB
	entities []storage.GitHubEntity
	nodes    []githubNode
	cursor   int
	offset   int
	detail   DetailModel
	width    int
	height   int
	loading  bool
	err      error

	treeMode      bool
	stateExpanded map[string]bool // "open", "merged", "closed"
	focusDetail   bool
	filter        string // "", "open", "closed", "pull", "issue"
}

func NewGitHubView(db *sql.DB) GitHubView {
	return GitHubView{
		db: db,
		stateExpanded: map[string]bool{
			"open":   true,
			"merged": true,
			"closed": false,
		},
	}
}

func (v *GitHubView) Reload() tea.Cmd {
	v.loading = true
	db := v.db
	return func() tea.Msg {
		entities, err := storage.ListGitHubEntities(db, storage.GitHubFilter{})
		return githubViewLoadedMsg{entities: entities, err: err}
	}
}

func (v *GitHubView) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.detail.Width = w - (w * TreeWidthPct / 100) - 3
	v.detail.Height = h
}

func (v *GitHubView) buildNodes() {
	v.nodes = nil

	// Apply filter
	var filtered []storage.GitHubEntity
	for _, e := range v.entities {
		if v.filter != "" {
			switch v.filter {
			case "open":
				if e.State != "open" && e.State != "" {
					continue
				}
			case "closed":
				if e.State != "closed" {
					continue
				}
			case "pull":
				if e.Kind != "pull" {
					continue
				}
			case "issue":
				if e.Kind != "issue" {
					continue
				}
			}
		}
		filtered = append(filtered, e)
	}

	if !v.treeMode {
		// Flat mode: just list entities
		for i := range filtered {
			v.nodes = append(v.nodes, githubNode{
				Entity: &filtered[i],
			})
		}
		return
	}

	// Tree mode: group by state
	type stateGroup struct {
		state    string
		entities []*storage.GitHubEntity
	}
	groups := map[string]*stateGroup{
		"open":   {state: "open"},
		"merged": {state: "merged"},
		"closed": {state: "closed"},
	}
	stateOrder := []string{"open", "merged", "closed"}

	for i := range filtered {
		e := &filtered[i]
		st := e.State
		if st == "" {
			st = "open" // treat unknown as open
		}
		if g, ok := groups[st]; ok {
			g.entities = append(g.entities, e)
		} else {
			// Unknown state, put in open
			groups["open"].entities = append(groups["open"].entities, e)
		}
	}

	for _, st := range stateOrder {
		g := groups[st]
		if len(g.entities) == 0 {
			continue
		}
		icon := "▸"
		if v.stateExpanded[st] {
			icon = "▼"
		}
		header := fmt.Sprintf("%s %s (%d)", icon, strings.Title(st), len(g.entities))
		v.nodes = append(v.nodes, githubNode{
			IsHeader: true,
			Header:   header,
			State:    st,
		})
		if v.stateExpanded[st] {
			for _, e := range g.entities {
				v.nodes = append(v.nodes, githubNode{
					Entity: e,
					State:  st,
				})
			}
		}
	}
}

func (v *GitHubView) selectedEntity() *storage.GitHubEntity {
	if v.cursor >= 0 && v.cursor < len(v.nodes) {
		return v.nodes[v.cursor].Entity
	}
	return nil
}

func (v GitHubView) Update(msg tea.Msg) (GitHubView, tea.Cmd) {
	switch msg := msg.(type) {
	case githubViewLoadedMsg:
		v.loading = false
		if msg.err != nil {
			v.err = msg.err
			return v, nil
		}
		v.entities = msg.entities
		v.err = nil
		v.buildNodes()
		if v.cursor >= len(v.nodes) {
			v.cursor = len(v.nodes) - 1
		}
		if v.cursor < 0 {
			v.cursor = 0
		}
		return v, nil

	case githubRefreshDoneMsg:
		if msg.err != nil {
			v.err = msg.err
		}
		// Reload from DB after refresh
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
			// Collapse header or jump to parent header
			if v.cursor >= 0 && v.cursor < len(v.nodes) {
				node := v.nodes[v.cursor]
				if node.IsHeader {
					v.stateExpanded[node.State] = false
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
				if node.IsHeader && !v.stateExpanded[node.State] {
					v.stateExpanded[node.State] = true
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
				v.stateExpanded[node.State] = !v.stateExpanded[node.State]
				v.buildNodes()
			} else {
				e := v.selectedEntity()
				if e != nil {
					v.focusDetail = true
				}
			}
		case "t":
			v.treeMode = !v.treeMode
			v.buildNodes()
			if v.cursor >= len(v.nodes) {
				v.cursor = len(v.nodes) - 1
			}
			if v.cursor < 0 {
				v.cursor = 0
			}
		case "f":
			// Cycle filter
			switch v.filter {
			case "":
				v.filter = "open"
			case "open":
				v.filter = "closed"
			case "closed":
				v.filter = "pull"
			case "pull":
				v.filter = "issue"
			case "issue":
				v.filter = ""
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
				return v, openGitHubInBrowser(e)
			}
		case "r":
			return v, v.forceRefresh()
		case "tab":
			v.focusDetail = true
		}
	}
	return v, nil
}

func (v *GitHubView) adjustOffset() {
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

func (v GitHubView) ViewList() string {
	if v.loading {
		return "Loading GitHub entities..."
	}
	if v.err != nil {
		return fmt.Sprintf("Error: %v", v.err)
	}
	if len(v.nodes) == 0 {
		if v.filter != "" {
			return fmt.Sprintf("No GitHub entities matching filter: %s", v.filter)
		}
		return "No GitHub entities yet.\n\n  GitHub PRs and issues are auto-detected\n  from tabs and signals."
	}

	treeWidth := v.width * TreeWidthPct / 100
	cursorStyle := lipgloss.NewStyle().Bold(true).Reverse(true)
	headerStyle := lipgloss.NewStyle().Bold(true)
	openStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	mergedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("135"))
	closedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	filterStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)

	var b strings.Builder

	// Filter indicator
	if v.filter != "" {
		b.WriteString(filterStyle.Render(fmt.Sprintf("  Filter: %s", v.filter)) + "\n")
	}

	end := v.offset + v.height
	if v.filter != "" {
		end-- // account for filter line
	}
	if end > len(v.nodes) {
		end = len(v.nodes)
	}

	for i := v.offset; i < end; i++ {
		node := v.nodes[i]
		var line string

		if node.IsHeader {
			line = headerStyle.Render(node.Header)
		} else if node.Entity != nil {
			e := node.Entity
			// State prefix
			var prefix string
			var style lipgloss.Style
			switch e.State {
			case "open", "":
				prefix = "○"
				style = openStyle
			case "merged":
				prefix = "●"
				style = mergedStyle
			case "closed":
				prefix = "✕"
				style = closedStyle
			default:
				prefix = "?"
				style = closedStyle
			}

			ref := fmt.Sprintf("%s/%s#%d", e.Owner, e.Repo, e.Number)
			indent := "  "
			if v.treeMode {
				indent = "    "
			}

			title := e.Title
			maxRef := treeWidth - len(indent) - 2 - 2 // prefix + spaces
			maxTitle := maxRef - len(ref) - 2
			if maxTitle > 0 && len(title) > maxTitle {
				title = title[:maxTitle-1] + "…"
			}

			line = indent + style.Render(prefix) + " " + style.Render(ref) + "  " + title
		}

		if i == v.cursor {
			// Pad line for full-width highlight
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

func (v GitHubView) ViewDetail() string {
	e := v.selectedEntity()
	if e == nil {
		return ""
	}

	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	valueStyle := lipgloss.NewStyle()
	openStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	mergedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("135")).Bold(true)
	closedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	headerBoldStyle := lipgloss.NewStyle().Bold(true)

	var b strings.Builder

	// Header: owner/repo#number
	ref := fmt.Sprintf("%s/%s#%d", e.Owner, e.Repo, e.Number)
	b.WriteString(headerBoldStyle.Render(ref) + "\n")
	if e.Title != "" {
		b.WriteString(valueStyle.Render(e.Title) + "\n")
	}
	b.WriteString("\n")

	// State
	b.WriteString(labelStyle.Render("State") + "\n")
	switch e.State {
	case "open", "":
		state := e.State
		if state == "" {
			state = "open"
		}
		b.WriteString(openStyle.Render(state) + "\n\n")
	case "merged":
		b.WriteString(mergedStyle.Render("merged") + "\n\n")
	case "closed":
		b.WriteString(closedStyle.Render("closed") + "\n\n")
	default:
		b.WriteString(valueStyle.Render(e.State) + "\n\n")
	}

	// Type
	b.WriteString(labelStyle.Render("Type") + "\n")
	kindLabel := "Issue"
	if e.Kind == "pull" {
		kindLabel = "Pull Request"
	}
	b.WriteString(valueStyle.Render(kindLabel) + "\n\n")

	// Author
	if e.Author != "" {
		b.WriteString(labelStyle.Render("Author") + "\n")
		b.WriteString(valueStyle.Render(e.Author) + "\n\n")
	}

	// Assignees
	if e.Assignees != "" {
		b.WriteString(labelStyle.Render("Assignees") + "\n")
		b.WriteString(valueStyle.Render(e.Assignees) + "\n\n")
	}

	// Review status (PRs only)
	if e.Kind == "pull" && e.ReviewStatus != nil {
		b.WriteString(labelStyle.Render("Review") + "\n")
		b.WriteString(valueStyle.Render(*e.ReviewStatus) + "\n\n")
	}

	// CI Checks (PRs only)
	if e.Kind == "pull" && e.ChecksStatus != nil {
		b.WriteString(labelStyle.Render("CI Checks") + "\n")
		b.WriteString(valueStyle.Render(*e.ChecksStatus) + "\n\n")
	}

	// First seen
	b.WriteString(labelStyle.Render("First Seen") + "\n")
	b.WriteString(valueStyle.Render(e.FirstSeenAt.Local().Format("2006-01-02 15:04")) + "\n")
	b.WriteString(dimStyle.Render("Source: "+e.FirstSeenSource) + "\n\n")

	// Last refreshed
	if e.LastRefreshedAt != nil {
		b.WriteString(labelStyle.Render("Last Refreshed") + "\n")
		b.WriteString(valueStyle.Render(e.LastRefreshedAt.Local().Format("2006-01-02 15:04")) + "\n\n")
	}

	// GitHub updated
	if e.GHUpdatedAt != nil {
		b.WriteString(labelStyle.Render("GitHub Updated") + "\n")
		b.WriteString(valueStyle.Render(e.GHUpdatedAt.Local().Format("2006-01-02 15:04")) + "\n\n")
	}

	// Timeline
	if v.db != nil {
		events, err := storage.ListGitHubEntityEvents(v.db, e.ID)
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

	content := b.String()
	return v.detail.ViewScrolled(content)
}

func (v GitHubView) FocusDetail() bool { return v.focusDetail }

// --- Helper functions ---

func openGitHubInBrowser(e *storage.GitHubEntity) tea.Cmd {
	return func() tea.Msg {
		url := fmt.Sprintf("https://github.com/%s/%s/", e.Owner, e.Repo)
		if e.Kind == "pull" {
			url += fmt.Sprintf("pull/%d", e.Number)
		} else {
			url += fmt.Sprintf("issues/%d", e.Number)
		}
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

func resolveGHToken() string {
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (v *GitHubView) forceRefresh() tea.Cmd {
	db := v.db
	entities := v.entities
	return func() tea.Msg {
		token := resolveGHToken()
		err := github.RefreshEntities(db, entities, token, true)
		return githubRefreshDoneMsg{err: err}
	}
}
