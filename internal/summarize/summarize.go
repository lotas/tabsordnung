package summarize

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/lotas/tabsordnung/internal/types"
)

// Config holds configuration for the summarize command.
type Config struct {
	OutDir     string
	Model      string
	OllamaHost string
	GroupName  string
	Session    *types.SessionData
}

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeFilename converts a page title into a safe filename (without extension).
func sanitizeFilename(title string) string {
	s := strings.TrimSpace(strings.ToLower(title))
	if s == "" {
		return "untitled"
	}
	s = nonAlphanumeric.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 100 {
		s = s[:100]
		s = strings.TrimRight(s, "-")
	}
	if s == "" {
		return "untitled"
	}
	return s
}

// findGroup returns the first group matching the given name, or nil.
func findGroup(session *types.SessionData, name string) *types.TabGroup {
	for _, g := range session.Groups {
		if g.Name == name {
			return g
		}
	}
	return nil
}

// Run executes the summarize workflow.
func Run(cfg Config) error {
	group := findGroup(cfg.Session, cfg.GroupName)
	if group == nil {
		return fmt.Errorf("tab group %q not found", cfg.GroupName)
	}

	if len(group.Tabs) == 0 {
		fmt.Fprintf(os.Stderr, "Group %q has no tabs.\n", cfg.GroupName)
		return nil
	}

	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Summarizing %d tabs from %q:\n", len(group.Tabs), cfg.GroupName)
	for i, tab := range group.Tabs {
		fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, tab.Title)
	}
	fmt.Fprintln(os.Stderr)

	var newCount, skipCount, errCount int
	ctx := context.Background()

	for i, tab := range group.Tabs {
		fmt.Fprintf(os.Stderr, "[%d/%d] %s\n", i+1, len(group.Tabs), tab.Title)

		filename := sanitizeFilename(tab.Title) + ".md"
		outPath := filepath.Join(cfg.OutDir, filename)

		// Dedup: skip if file already exists.
		if _, err := os.Stat(outPath); err == nil {
			fmt.Fprintf(os.Stderr, "        – skipped (exists)\n")
			skipCount++
			continue
		}

		// Fetch readable content.
		fmt.Fprintf(os.Stderr, "        fetching...")
		title, text, err := FetchReadable(tab.URL)
		if err != nil {
			fmt.Fprintf(os.Stderr, " ✗ %v\n", err)
			errCount++
			continue
		}
		fmt.Fprintf(os.Stderr, " ok\n")

		if len(strings.TrimSpace(text)) < 50 {
			fmt.Fprintf(os.Stderr, "        ✗ not enough readable content\n")
			errCount++
			continue
		}

		// Use fetched title if available, fall back to tab title.
		if title == "" {
			title = tab.Title
		}

		// Summarize via Ollama.
		fmt.Fprintf(os.Stderr, "        summarizing...")
		summary, err := OllamaSummarize(ctx, cfg.Model, cfg.OllamaHost, text)
		if err != nil {
			fmt.Fprintf(os.Stderr, " ✗ ollama: %v\n", err)
			errCount++
			continue
		}
		fmt.Fprintf(os.Stderr, " ok\n")

		// Write markdown file.
		content := fmt.Sprintf("# %s\n\n**Source:** %s\n**Summarized:** %s\n\n## Summary\n\n%s\n",
			title, tab.URL, time.Now().Format("2006-01-02"), summary)

		if err := os.WriteFile(outPath, []byte(content), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "        ✗ write: %v\n", err)
			errCount++
			continue
		}

		fmt.Fprintf(os.Stderr, "        ✓ saved %s\n", filename)
		newCount++
	}

	fmt.Fprintf(os.Stderr, "\nDone: %d new, %d skipped, %d errors\n", newCount, skipCount, errCount)
	return nil
}
