package tui

import (
	"database/sql"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/lotas/tabsordnung/internal/server"
	"github.com/lotas/tabsordnung/internal/signal"
	"github.com/lotas/tabsordnung/internal/storage"
	"github.com/lotas/tabsordnung/internal/summarize"
	"github.com/lotas/tabsordnung/internal/types"
)

// Messages returned by TabsView for root Model to handle.
type showGroupPickerMsg struct{ ids []int }
type showFilterPickerMsg struct{}
type reloadSessionMsg struct{}

type TabsView struct {
	// Navigation / display
	tree        TreeModel
	detail      DetailModel
	focusDetail bool
	selected    map[int]bool // BrowserID -> selected (live mode multi-select)

	// Signal list in detail pane
	signals      []storage.SignalRecord
	signalCursor int
	signalSource string

	// Analysis progress
	deadChecking   bool
	githubChecking bool

	// Signal capture pipeline
	signalQueue  []*SignalJob
	signalActive *SignalJob
	signalErrors map[string]string

	// Summarization pipeline
	summarizeJobs   map[string]*SummarizeJob
	summarizeErrors map[string]string

	// Dependencies (set at construction, shared by pointer)
	server      *server.Server
	db          *sql.DB
	summaryDir  string
	ollamaModel string
	ollamaHost  string

	// Shared state (set by root before Update/View)
	session   *types.SessionData
	mode      SourceMode
	connected bool
	stats     types.Stats
	staleDays int
	width     int
	height    int
}

func NewTabsView(srv *server.Server, db *sql.DB, summaryDir, ollamaModel, ollamaHost string) TabsView {
	return TabsView{
		selected:        make(map[int]bool),
		summarizeJobs:   make(map[string]*SummarizeJob),
		summarizeErrors: make(map[string]string),
		signalErrors:    make(map[string]string),
		server:          srv,
		db:              db,
		summaryDir:      summaryDir,
		ollamaModel:     ollamaModel,
		ollamaHost:      ollamaHost,
	}
}

func (v *TabsView) SetSize(w, h int) {
	v.width = w
	v.height = h
	treeWidth := w * 60 / 100
	paneHeight := h - 4
	v.tree.Width = treeWidth
	v.tree.Height = paneHeight
	detailWidth := w - treeWidth - 3
	v.detail.Width = detailWidth
	v.detail.Height = paneHeight
}

func (v TabsView) FocusDetail() bool { return v.focusDetail }

// --- Helper methods (moved from Model) ---

func (v *TabsView) selectedOrCurrentTabIDs() []int {
	if len(v.selected) > 0 {
		ids := make([]int, 0, len(v.selected))
		for id := range v.selected {
			ids = append(ids, id)
		}
		return ids
	}
	node := v.tree.SelectedNode()
	if node != nil && node.Tab != nil && node.Tab.BrowserID != 0 {
		return []int{node.Tab.BrowserID}
	}
	return nil
}

func (v *TabsView) processNextSignal() tea.Cmd {
	if v.signalActive != nil || len(v.signalQueue) == 0 {
		return nil
	}
	v.signalActive = v.signalQueue[0]
	v.signalQueue = v.signalQueue[1:]

	id, cmd := sendCmdWithID(v.server, server.OutgoingMsg{
		Action: "scrape-activity",
		TabID:  v.signalActive.Tab.BrowserID,
		Source: v.signalActive.Source,
	})
	v.signalActive.ContentID = id
	return cmd
}

func (v *TabsView) queueSignalPoll() tea.Cmd {
	if v.session == nil || !v.connected {
		return signalPollTick()
	}

	sourceTabs := make(map[string]*types.Tab)
	for _, tab := range v.session.AllTabs {
		src := signal.DetectSource(tab.URL)
		if src == "" {
			continue
		}
		if _, ok := sourceTabs[src]; !ok {
			sourceTabs[src] = tab
		}
	}

	if v.signalActive != nil {
		delete(sourceTabs, v.signalActive.Source)
	}
	for _, j := range v.signalQueue {
		delete(sourceTabs, j.Source)
	}

	for src, tab := range sourceTabs {
		v.signalQueue = append(v.signalQueue, &SignalJob{Tab: tab, Source: src})
	}

	return tea.Batch(v.processNextSignal(), signalPollTick())
}

func (v *TabsView) refreshSignals() {
	node := v.tree.SelectedNode()
	var source string
	if node != nil && node.Tab != nil {
		source = signal.DetectSource(node.Tab.URL)
	}
	if source != v.signalSource {
		v.signalSource = source
		v.signalCursor = 0
		if source != "" && v.db != nil {
			v.signals, _ = storage.ListSignals(v.db, source, true)
		} else {
			v.signals = nil
		}
	}
}

func (v *TabsView) scrollDetailToSignalCursor() {
	headerLines := 10
	cursorLine := headerLines + v.signalCursor
	if cursorLine < v.detail.Scroll {
		v.detail.Scroll = cursorLine
	} else if cursorLine >= v.detail.Scroll+v.detail.Height-1 {
		v.detail.Scroll = cursorLine - v.detail.Height + 2
	}
	if v.detail.Scroll < 0 {
		v.detail.Scroll = 0
	}
}

func (v *TabsView) findTabForSource(source string) *types.Tab {
	if v.session == nil {
		return nil
	}
	for _, tab := range v.session.AllTabs {
		if signal.DetectSource(tab.URL) == source {
			return tab
		}
	}
	return nil
}

func (v *TabsView) RebuildTree() {
	oldCursor := v.tree.Cursor
	oldOffset := v.tree.Offset
	oldExpanded := v.tree.Expanded
	oldFilter := v.tree.Filter
	oldSavedExpanded := v.tree.SavedExpanded

	v.tree = NewTreeModel(v.session.Groups)
	v.tree.Width = v.width * 60 / 100
	v.tree.Height = v.height - 4
	v.tree.Filter = oldFilter
	v.tree.SavedExpanded = oldSavedExpanded
	v.tree.SummaryDir = v.summaryDir
	if v.db != nil {
		v.tree.SignalCounts, _ = storage.ActiveSignalCounts(v.db)
	}

	if oldExpanded != nil {
		for id, exp := range oldExpanded {
			v.tree.Expanded[id] = exp
		}
	}

	if v.tree.Filter != types.FilterAll {
		for _, g := range v.session.Groups {
			if _, exists := oldExpanded[g.ID]; !exists {
				v.tree.Expanded[g.ID] = true
			}
		}
	}

	nodes := v.tree.VisibleNodes()
	if oldCursor >= len(nodes) {
		oldCursor = len(nodes) - 1
	}
	if oldCursor < 0 {
		oldCursor = 0
	}
	v.tree.Cursor = oldCursor
	v.tree.Offset = oldOffset
	v.refreshSignals()
}

// --- Update method ---

func (v TabsView) Update(msg tea.Msg) (TabsView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseMsg:
		treeWidth := v.width * 60 / 100
		onDetail := msg.X > treeWidth+1
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if onDetail {
				v.detail.ScrollUp()
			} else {
				v.tree.MoveUp()
				v.detail.Scroll = 0
				v.refreshSignals()
			}
		case tea.MouseButtonWheelDown:
			if onDetail {
				v.detail.ScrollDown()
			} else {
				v.tree.MoveDown()
				v.detail.Scroll = 0
				v.refreshSignals()
			}
		}
		return v, nil

	case tea.KeyMsg:
		// Tab toggles pane focus
		switch msg.String() {
		case "tab", "shift+tab":
			if v.focusDetail {
				v.focusDetail = false
				v.detail.Scroll = 0
			} else {
				node := v.tree.SelectedNode()
				if node != nil && (node.Tab != nil || node.Group != nil) {
					v.focusDetail = true
				}
			}
			return v, nil
		}

		// Detail pane focus mode
		if v.focusDetail {
			if v.signalSource != "" && len(v.signals) > 0 {
				switch msg.String() {
				case "j", "down":
					if v.signalCursor < len(v.signals)-1 {
						v.signalCursor++
					}
					v.scrollDetailToSignalCursor()
					return v, nil
				case "k", "up":
					if v.signalCursor > 0 {
						v.signalCursor--
					}
					v.scrollDetailToSignalCursor()
					return v, nil
				case "enter":
					if v.mode == ModeLive && v.connected {
						sig := v.signals[v.signalCursor]
						tab := v.findTabForSource(v.signalSource)
						if tab != nil && tab.BrowserID != 0 {
							return v, navigateSignalCmd(v.server, tab.BrowserID, v.signalSource, sig.Title)
						}
					}
					return v, nil
				case "x":
					sig := v.signals[v.signalCursor]
					if sig.CompletedAt == nil {
						return v, completeSignalCmd(v.db, sig.ID, v.signalSource)
					}
					return v, nil
				case "u":
					sig := v.signals[v.signalCursor]
					if sig.CompletedAt != nil {
						return v, reopenSignalCmd(v.db, sig.ID, v.signalSource)
					}
					return v, nil
				case "esc":
					v.focusDetail = false
					v.detail.Scroll = 0
					return v, nil
				case "c":
					// Fall through to main 'c' handler
				default:
					return v, nil
				}
			} else {
				switch msg.String() {
				case "esc":
					v.focusDetail = false
					v.detail.Scroll = 0
					return v, nil
				case "j", "down":
					v.detail.ScrollDown()
					return v, nil
				case "k", "up":
					v.detail.ScrollUp()
					return v, nil
				case "s":
					// fall through to main handler
				default:
					return v, nil
				}
			}
		}

		// Main key handling
		switch msg.String() {
		case "up", "k":
			v.tree.MoveUp()
			v.refreshSignals()
		case "down", "j":
			v.tree.MoveDown()
			v.refreshSignals()
		case "enter":
			if v.mode == ModeLive && v.connected {
				node := v.tree.SelectedNode()
				if node != nil && node.Tab != nil {
					return v, sendCmd(v.server, server.OutgoingMsg{
						Action: "focus",
						TabID:  node.Tab.BrowserID,
					})
				}
			}
			node := v.tree.SelectedNode()
			if node != nil && node.Group != nil {
				v.tree.Toggle()
			} else if node != nil && node.Tab != nil {
				v.focusDetail = true
			}
		case "h":
			v.tree.CollapseOrParent()
		case "l":
			v.tree.ExpandOrEnter()
		case "s":
			node := v.tree.SelectedNode()
			if node != nil && node.Tab != nil {
				url := node.Tab.URL
				if _, exists := v.summarizeJobs[url]; exists {
					break
				}
				delete(v.summarizeErrors, url)
				job := &SummarizeJob{Tab: node.Tab}
				v.summarizeJobs[url] = job
				if v.mode == ModeLive && v.connected {
					id, cmd := sendCmdWithID(v.server, server.OutgoingMsg{
						Action: "get-content",
						TabID:  node.Tab.BrowserID,
					})
					job.ContentID = id
					return v, cmd
				}
				return v, runSummarizeTab(node.Tab, v.summaryDir, v.ollamaModel, v.ollamaHost)
			}
		case "c":
			if v.mode != ModeLive || !v.connected {
				break
			}
			node := v.tree.SelectedNode()
			if node == nil || node.Tab == nil {
				break
			}
			source := signal.DetectSource(node.Tab.URL)
			if source == "" {
				break
			}
			alreadyQueued := false
			for _, j := range v.signalQueue {
				if j.Tab.BrowserID == node.Tab.BrowserID {
					alreadyQueued = true
					break
				}
			}
			if alreadyQueued {
				break
			}
			if v.signalActive != nil && v.signalActive.Tab.BrowserID == node.Tab.BrowserID {
				break
			}
			delete(v.signalErrors, source)
			job := &SignalJob{Tab: node.Tab, Source: source}
			v.signalQueue = append(v.signalQueue, job)
			return v, v.processNextSignal()
		case "f":
			return v, func() tea.Msg { return showFilterPickerMsg{} }
		case "r":
			if v.mode == ModeLive {
				return v, nil
			}
			return v, func() tea.Msg { return reloadSessionMsg{} }
		case "x":
			if v.mode != ModeLive || !v.connected {
				return v, nil
			}
			ids := v.selectedOrCurrentTabIDs()
			if len(ids) == 0 {
				return v, nil
			}
			return v, sendCmd(v.server, server.OutgoingMsg{
				Action: "close",
				TabIDs: ids,
			})
		case " ":
			if v.mode != ModeLive || !v.connected {
				return v, nil
			}
			node := v.tree.SelectedNode()
			if node != nil && node.Tab != nil && node.Tab.BrowserID != 0 {
				id := node.Tab.BrowserID
				if v.selected[id] {
					delete(v.selected, id)
				} else {
					v.selected[id] = true
				}
			}
			v.tree.MoveDown()
			v.refreshSignals()
		case "g":
			if v.mode != ModeLive || !v.connected || v.session == nil {
				return v, nil
			}
			ids := v.selectedOrCurrentTabIDs()
			if len(ids) == 0 {
				return v, nil
			}
			return v, func() tea.Msg { return showGroupPickerMsg{ids: ids} }
		case "esc":
			v.selected = make(map[int]bool)
		}
		return v, nil
	}
	return v, nil
}

// --- View methods ---

func (v TabsView) ViewList() string {
	if v.session == nil {
		return "No session loaded"
	}
	v.tree.Selected = v.selected
	summarizingURLs := make(map[string]bool, len(v.summarizeJobs))
	for url := range v.summarizeJobs {
		summarizingURLs[url] = true
	}
	v.tree.SummarizingURLs = summarizingURLs
	return v.tree.View()
}

func (v TabsView) ViewDetail() string {
	if v.session == nil {
		return ""
	}
	node := v.tree.SelectedNode()
	if node == nil {
		return ""
	}

	detailWidth := v.width - (v.width * 60 / 100) - 3
	var detailContent string

	if node.Tab != nil {
		if v.signalSource != "" {
			isCapturing := v.signalActive != nil && v.signalActive.Source == v.signalSource
			if !isCapturing {
				for _, j := range v.signalQueue {
					if j.Source == v.signalSource {
						isCapturing = true
						break
					}
				}
			}
			sigErr := v.signalErrors[v.signalSource]
			detailContent = v.detail.ViewTabWithSignal(node.Tab, v.signals, v.signalCursor, isCapturing, sigErr)
		} else {
			var summaryText string
			sumPath := summarize.SummaryPath(v.summaryDir, node.Tab.URL, node.Tab.Title)
			if raw, err := summarize.ReadSummary(sumPath); err == nil {
				r, _ := glamour.NewTermRenderer(
					glamour.WithStylePath("dark"),
					glamour.WithWordWrap(detailWidth-2),
				)
				if rendered, err := r.Render(raw); err == nil {
					summaryText = rendered
				} else {
					summaryText = raw
				}
			}
			_, isSummarizing := v.summarizeJobs[node.Tab.URL]
			tabErr := v.summarizeErrors[node.Tab.URL]
			detailContent = v.detail.ViewTabWithSummary(node.Tab, summaryText, isSummarizing, tabErr)
		}
	} else if node.Group != nil {
		detailContent = v.detail.ViewGroup(node.Group)
	}

	return v.detail.ViewScrolled(detailContent)
}

func (v TabsView) StatsString() string {
	s := fmt.Sprintf("%d tabs \u00b7 %d groups", v.stats.TotalTabs, v.stats.TotalGroups)
	if v.stats.DeadTabs > 0 {
		s += fmt.Sprintf(" \u00b7 %d dead", v.stats.DeadTabs)
	}
	if v.stats.StaleTabs > 0 {
		s += fmt.Sprintf(" \u00b7 %d stale", v.stats.StaleTabs)
	}
	if v.stats.DuplicateTabs > 0 {
		s += fmt.Sprintf(" \u00b7 %d dup", v.stats.DuplicateTabs)
	}
	if v.stats.GitHubDoneTabs > 0 {
		s += fmt.Sprintf(" \u00b7 %d done", v.stats.GitHubDoneTabs)
	}
	if v.deadChecking {
		s += " \u00b7 checking links..."
	}
	if v.githubChecking {
		s += " \u00b7 checking github..."
	}
	if n := len(v.summarizeJobs); n == 1 {
		s += " \u00b7 summarizing 1 tab..."
	} else if n > 1 {
		s += fmt.Sprintf(" \u00b7 summarizing %d tabs...", n)
	}
	if v.signalActive != nil {
		s += " \u00b7 checking signals..."
	}
	return s
}

func (v TabsView) BottomBar() string {
	var s string
	if v.mode == ModeLive && v.connected {
		selCount := len(v.selected)
		if selCount > 0 {
			s = fmt.Sprintf("%d selected \u00b7 x close \u00b7 g move \u00b7 esc clear \u00b7 ", selCount)
		}
		s += "space select \u00b7 enter focus \u00b7 "
	}
	filterNames := []string{"all", "stale", "dead", "duplicate", ">7d", ">30d", ">90d", "gh done", "summarized", "unsummarized"}
	filterStr := fmt.Sprintf("[filter: %s]", filterNames[v.tree.Filter])
	s += "\u2191\u2193/jk navigate \u00b7 tab focus \u00b7 s summarize \u00b7 c signal \u00b7 f filter \u00b7 r refresh \u00b7 p source \u00b7 q quit  " + filterStr
	return s
}
