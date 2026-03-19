package tui

import (
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// noteSubmitMsg is sent when the user submits a note (Ctrl+Enter).
type noteSubmitMsg struct {
	body string
}

// noteCancelMsg is sent when the user cancels the editor (Esc).
type noteCancelMsg struct{}

// NoteEditor is a multi-line text input for writing notes.
// col tracks the rune (character) offset, not byte offset.
type NoteEditor struct {
	lines  []string
	cursor int // cursor line
	col    int // cursor column (rune index)
	Width  int
	Height int
}

// NewNoteEditor creates a new editor with a single empty line.
func NewNoteEditor(width, height int) NoteEditor {
	return NoteEditor{
		lines:  []string{""},
		Width:  width,
		Height: height,
	}
}

// runeLen returns the number of runes in s.
func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}

// runeSlice returns the substring from rune index start to end.
func runeSlice(s string, start, end int) string {
	runes := []rune(s)
	if start > len(runes) {
		start = len(runes)
	}
	if end > len(runes) {
		end = len(runes)
	}
	return string(runes[start:end])
}

// runeInsert inserts ins at rune position pos in s.
func runeInsert(s string, pos int, ins string) string {
	runes := []rune(s)
	if pos > len(runes) {
		pos = len(runes)
	}
	result := make([]rune, 0, len(runes)+utf8.RuneCountInString(ins))
	result = append(result, runes[:pos]...)
	result = append(result, []rune(ins)...)
	result = append(result, runes[pos:]...)
	return string(result)
}

// Update handles key input for the editor.
func (e NoteEditor) Update(msg tea.KeyMsg) (NoteEditor, tea.Cmd) {
	switch msg.String() {
	case "ctrl+enter", "ctrl+s":
		body := strings.TrimSpace(strings.Join(e.lines, "\n"))
		if body == "" {
			return e, func() tea.Msg { return noteCancelMsg{} }
		}
		return e, func() tea.Msg { return noteSubmitMsg{body: body} }
	case "esc":
		return e, func() tea.Msg { return noteCancelMsg{} }
	case "enter":
		line := e.lines[e.cursor]
		before := runeSlice(line, 0, e.col)
		after := runeSlice(line, e.col, runeLen(line))
		e.lines[e.cursor] = before
		newLines := make([]string, len(e.lines)+1)
		copy(newLines, e.lines[:e.cursor+1])
		newLines[e.cursor+1] = after
		copy(newLines[e.cursor+2:], e.lines[e.cursor+1:])
		e.lines = newLines
		e.cursor++
		e.col = 0
	case "backspace":
		if e.col > 0 {
			line := e.lines[e.cursor]
			e.lines[e.cursor] = runeSlice(line, 0, e.col-1) + runeSlice(line, e.col, runeLen(line))
			e.col--
		} else if e.cursor > 0 {
			prevLen := runeLen(e.lines[e.cursor-1])
			e.lines[e.cursor-1] += e.lines[e.cursor]
			e.lines = append(e.lines[:e.cursor], e.lines[e.cursor+1:]...)
			e.cursor--
			e.col = prevLen
		}
	case "left":
		if e.col > 0 {
			e.col--
		} else if e.cursor > 0 {
			e.cursor--
			e.col = runeLen(e.lines[e.cursor])
		}
	case "right":
		if e.col < runeLen(e.lines[e.cursor]) {
			e.col++
		} else if e.cursor < len(e.lines)-1 {
			e.cursor++
			e.col = 0
		}
	case "up":
		if e.cursor > 0 {
			e.cursor--
			if e.col > runeLen(e.lines[e.cursor]) {
				e.col = runeLen(e.lines[e.cursor])
			}
		}
	case "down":
		if e.cursor < len(e.lines)-1 {
			e.cursor++
			if e.col > runeLen(e.lines[e.cursor]) {
				e.col = runeLen(e.lines[e.cursor])
			}
		}
	default:
		r := msg.String()
		if len(r) == 1 && r[0] >= 32 {
			e.lines[e.cursor] = runeInsert(e.lines[e.cursor], e.col, r)
			e.col++
		} else if len(r) > 1 && !strings.HasPrefix(r, "ctrl+") && !strings.HasPrefix(r, "alt+") && !strings.HasPrefix(r, "shift+") {
			e.lines[e.cursor] = runeInsert(e.lines[e.cursor], e.col, r)
			e.col += utf8.RuneCountInString(r)
		}
	}
	return e, nil
}

// View renders the editor.
func (e NoteEditor) View() string {
	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	cursorLineStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("255"))

	var b strings.Builder
	b.WriteString(labelStyle.Render("New Note") + "\n")
	b.WriteString(dimStyle.Render("Ctrl+S to save \u00b7 Esc to cancel") + "\n\n")

	maxLines := e.Height - 5
	if maxLines < 3 {
		maxLines = 3
	}

	for i, line := range e.lines {
		if i >= maxLines {
			b.WriteString(dimStyle.Render("...") + "\n")
			break
		}
		if i == e.cursor {
			displayLine := runeSlice(line, 0, e.col) + "\u2588" + runeSlice(line, e.col, runeLen(line))
			b.WriteString(cursorLineStyle.Render(displayLine) + "\n")
		} else {
			b.WriteString(line + "\n")
		}
	}

	return b.String()
}
