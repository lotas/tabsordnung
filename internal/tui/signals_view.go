package tui

import (
	"database/sql"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lotas/tabsordnung/internal/storage"
)

type signalsViewLoadedMsg struct {
	signals []storage.SignalRecord
	err     error
}

// signalNode represents a row in the signals tree.
type signalNode struct {
	IsHeader    bool   // true for source/section headers
	Header      string // e.g. "Gmail (3 active)" or "Completed (5)"
	Signal      *storage.SignalRecord
	Source      string // source name (set on headers and their children)
	IsCompleted bool   // true for the "Completed" section header
}

type SignalsView struct {
	db      *sql.DB
	signals []storage.SignalRecord
	nodes   []signalNode
	cursor  int
	offset  int
	detail  DetailModel
	width   int
	height  int
	loading bool
	err     error

	// Source expansion
	sourceExpanded    map[string]bool
	completedExpanded bool
	focusDetail       bool
}

func NewSignalsView(db *sql.DB) SignalsView {
	return SignalsView{
		db:             db,
		sourceExpanded: make(map[string]bool),
	}
}

func (v SignalsView) Init() tea.Cmd { return nil }

func (v *SignalsView) Reload() tea.Cmd {
	v.loading = true
	db := v.db
	return func() tea.Msg {
		signals, err := storage.ListSignals(db, "", true)
		return signalsViewLoadedMsg{signals: signals, err: err}
	}
}

func (v *SignalsView) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.detail.Width = w - (w * TreeWidthPct / 100) - 3
	v.detail.Height = h
}

func (v *SignalsView) buildNodes() {
	v.nodes = nil

	// Group active signals by source
	type sourceGroup struct {
		source  string
		signals []*storage.SignalRecord
	}
	sourceMap := make(map[string]*sourceGroup)
	var sourceOrder []string
	var completed []*storage.SignalRecord

	for i := range v.signals {
		s := &v.signals[i]
		if s.CompletedAt != nil {
			completed = append(completed, s)
			continue
		}
		if _, ok := sourceMap[s.Source]; !ok {
			sourceMap[s.Source] = &sourceGroup{source: s.Source}
			sourceOrder = append(sourceOrder, s.Source)
		}
		sourceMap[s.Source].signals = append(sourceMap[s.Source].signals, s)
	}

	// Active sources
	for _, src := range sourceOrder {
		sg := sourceMap[src]
		if _, ok := v.sourceExpanded[src]; !ok {
			v.sourceExpanded[src] = true
		}
		icon := "▸"
		if v.sourceExpanded[src] {
			icon = "▼"
		}
		v.nodes = append(v.nodes, signalNode{
			IsHeader: true,
			Header:   fmt.Sprintf("%s %s (%d active)", icon, sg.source, len(sg.signals)),
			Source:   src,
		})
		if v.sourceExpanded[src] {
			for _, s := range sg.signals {
				v.nodes = append(v.nodes, signalNode{Signal: s, Source: src})
			}
		}
	}

	// Completed section
	if len(completed) > 0 {
		icon := "▸"
		if v.completedExpanded {
			icon = "▼"
		}
		v.nodes = append(v.nodes, signalNode{
			IsHeader:    true,
			Header:      fmt.Sprintf("%s Completed (%d)", icon, len(completed)),
			IsCompleted: true,
		})
		if v.completedExpanded {
			for _, s := range completed {
				v.nodes = append(v.nodes, signalNode{Signal: s, IsCompleted: true})
			}
		}
	}
}

func (v *SignalsView) selectedSignal() *storage.SignalRecord {
	if v.cursor >= 0 && v.cursor < len(v.nodes) {
		return v.nodes[v.cursor].Signal
	}
	return nil
}

func (v SignalsView) Update(msg tea.Msg) (SignalsView, tea.Cmd) {
	switch msg := msg.(type) {
	case signalsViewLoadedMsg:
		v.loading = false
		if msg.err != nil {
			v.err = msg.err
			return v, nil
		}
		v.signals = msg.signals
		v.err = nil
		v.buildNodes()
		if v.cursor >= len(v.nodes) {
			v.cursor = len(v.nodes) - 1
		}
		if v.cursor < 0 {
			v.cursor = 0
		}
		return v, nil

	case signalActionMsg:
		// Reload after complete/reopen
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
			// Collapse current header, or move to parent header
			if v.cursor >= 0 && v.cursor < len(v.nodes) {
				node := v.nodes[v.cursor]
				if node.IsHeader {
					v.toggleHeader(node)
					v.buildNodes()
				} else {
					// Move cursor to parent header
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
			// Expand current header, or move down
			if v.cursor >= 0 && v.cursor < len(v.nodes) {
				node := v.nodes[v.cursor]
				if node.IsHeader && !v.isExpanded(node) {
					v.toggleHeader(node)
					v.buildNodes()
				} else if v.cursor < len(v.nodes)-1 {
					v.cursor++
					v.adjustOffset()
					v.detail.Scroll = 0
				}
			}
		case "enter", " ":
			// Toggle header expansion
			if v.cursor >= 0 && v.cursor < len(v.nodes) && v.nodes[v.cursor].IsHeader {
				v.toggleHeader(v.nodes[v.cursor])
				v.buildNodes()
			} else if msg.String() == "enter" {
				sig := v.selectedSignal()
				if sig != nil {
					return v, func() tea.Msg {
						return signalNavigateMsg{Source: sig.Source, Title: sig.Title}
					}
				}
				v.focusDetail = true
			}
		case "x":
			// Complete signal
			sig := v.selectedSignal()
			if sig != nil && sig.CompletedAt == nil {
				return v, completeSignalCmd(v.db, sig.ID, sig.Source)
			}
		case "u":
			// Reopen signal
			sig := v.selectedSignal()
			if sig != nil && sig.CompletedAt != nil {
				return v, reopenSignalCmd(v.db, sig.ID, sig.Source)
			}
		}
	}
	return v, nil
}

func (v *SignalsView) toggleHeader(node signalNode) {
	if node.IsCompleted {
		v.completedExpanded = !v.completedExpanded
	} else if node.Source != "" {
		v.sourceExpanded[node.Source] = !v.sourceExpanded[node.Source]
	}
}

func (v *SignalsView) isExpanded(node signalNode) bool {
	if node.IsCompleted {
		return v.completedExpanded
	}
	return v.sourceExpanded[node.Source]
}

func (v *SignalsView) adjustOffset() {
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

func (v SignalsView) ViewList() string {
	if v.loading {
		return "Loading signals..."
	}
	if v.err != nil {
		return fmt.Sprintf("Error: %v", v.err)
	}
	if len(v.nodes) == 0 {
		return "No signals yet.\n\n  Press 'c' on a signal source tab\n  (Gmail, Slack, Matrix) to capture."
	}

	treeWidth := v.width * TreeWidthPct / 100
	cursorStyle := lipgloss.NewStyle().Bold(true).Reverse(true)
	groupStyle := lipgloss.NewStyle().Bold(true)
	completedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	var b strings.Builder
	end := v.offset + v.height
	if end > len(v.nodes) {
		end = len(v.nodes)
	}

	for i := v.offset; i < end; i++ {
		node := v.nodes[i]
		var line string

		if node.IsHeader {
			line = groupStyle.Render(node.Header)
		} else if node.Signal != nil {
			s := node.Signal
			age := formatSignalAge(s.CapturedAt)
			text := fmt.Sprintf("    %s", s.Title)
			if s.Preview != "" {
				text += " — " + s.Preview
			}
			suffix := "  " + age

			maxLen := treeWidth - len(suffix) - 2
			if maxLen > 0 && len(text) > maxLen {
				text = text[:maxLen-1] + "…"
			}
			line = text + suffix

			if s.CompletedAt != nil {
				line = completedStyle.Render("  ✓ " + line[4:])
			}
		}

		if i == v.cursor {
			// Strip ANSI for width padding, then re-render
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

func (v SignalsView) ViewDetail() string {
	sig := v.selectedSignal()
	if sig == nil {
		return ""
	}

	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	valueStyle := lipgloss.NewStyle()
	activeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	completedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	var b strings.Builder

	b.WriteString(labelStyle.Render("Source") + "\n")
	b.WriteString(valueStyle.Render(sig.Source) + "\n\n")

	b.WriteString(labelStyle.Render("Title") + "\n")
	b.WriteString(valueStyle.Render(sig.Title) + "\n\n")

	if sig.Preview != "" {
		b.WriteString(labelStyle.Render("Preview") + "\n")
		b.WriteString(valueStyle.Render(sig.Preview) + "\n\n")
	}

	if sig.Snippet != "" {
		snippetStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Italic(true)
		b.WriteString(labelStyle.Render("Snippet") + "\n")
		b.WriteString(snippetStyle.Render(sig.Snippet) + "\n\n")
	}

	if sig.SourceTS != "" {
		b.WriteString(labelStyle.Render("Timestamp") + "\n")
		b.WriteString(valueStyle.Render(sig.SourceTS) + "\n\n")
	}

	b.WriteString(labelStyle.Render("Captured") + "\n")
	b.WriteString(valueStyle.Render(sig.CapturedAt.Local().Format("2006-01-02 15:04") + " (" + formatSignalAge(sig.CapturedAt) + ")") + "\n\n")

	b.WriteString(labelStyle.Render("Status") + "\n")
	if sig.CompletedAt != nil {
		b.WriteString(completedStyle.Render("Completed") + "\n")
	} else {
		b.WriteString(activeStyle.Render("Active") + "\n")
	}
	if sig.Pinned {
		b.WriteString(valueStyle.Render("Pinned (won't auto-complete)") + "\n")
	}

	content := b.String()
	return v.detail.ViewScrolled(content)
}

func (v SignalsView) FocusDetail() bool { return v.focusDetail }
