package export

import (
	"fmt"
	"strings"
	"time"

	"github.com/lotas/tabsordnung/internal/types"
)

// Markdown formats session data as a markdown document.
func Markdown(data *types.SessionData) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Firefox Tabs — %s\n", data.Profile.Name)
	fmt.Fprintf(&b, "> Exported %s\n", time.Now().Format("2006-01-02 15:04"))

	for _, g := range data.Groups {
		n := len(g.Tabs)
		noun := "tabs"
		if n == 1 {
			noun = "tab"
		}
		fmt.Fprintf(&b, "\n## %s (%d %s)\n\n", g.Name, n, noun)

		for _, tab := range g.Tabs {
			title := tab.Title
			if title == "" {
				title = tab.URL
			}
			fmt.Fprintf(&b, "- [%s](%s) — %s\n", title, tab.URL, relativeTime(tab.LastAccessed))
		}
	}

	return b.String()
}

func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
