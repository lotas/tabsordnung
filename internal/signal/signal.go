package signal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type SignalItem struct {
	Title   string `json:"title"`
	Preview string `json:"preview"`
}

type Signal struct {
	Source     string
	CapturedAt time.Time
	Items      []SignalItem
}

func DetectSource(url string) string {
	lower := strings.ToLower(url)
	switch {
	case strings.Contains(lower, "mail.google.com"):
		return "gmail"
	case strings.Contains(lower, "slack.com"):
		return "slack"
	case strings.Contains(lower, "element.io"),
		strings.Contains(lower, "matrix."):
		return "matrix"
	}
	return ""
}

func ParseItemsJSON(raw string) ([]SignalItem, error) {
	var items []SignalItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, err
	}
	return deduplicateItems(items), nil
}

func deduplicateItems(items []SignalItem) []SignalItem {
	seen := make(map[string]bool)
	result := make([]SignalItem, 0, len(items))
	for _, item := range items {
		key := item.Title + "\x00" + item.Preview
		if !seen[key] {
			seen[key] = true
			result = append(result, item)
		}
	}
	return result
}

func ItemsEqual(a, b []SignalItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Title != b[i].Title || a[i].Preview != b[i].Preview {
			return false
		}
	}
	return true
}

func WriteSignal(dir string, sig Signal) (string, error) {
	// Dedup: skip write if items match the most recent signal for this source
	existing, _ := ReadSignals(dir, sig.Source)
	if len(existing) > 0 && ItemsEqual(existing[0].Items, sig.Items) {
		return "", nil
	}

	subDir := filepath.Join(dir, sig.Source)
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		return "", err
	}

	filename := sig.CapturedAt.Format("2006-01-02T15-04-05") + ".md"
	path := filepath.Join(subDir, filename)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("# %s Signal — %s\n\n", capitalize(sig.Source), sig.CapturedAt.Format("2006-01-02 15:04")))
	for _, item := range sig.Items {
		if item.Title != "" {
			b.WriteString(fmt.Sprintf("- **%s** — %s\n", item.Title, item.Preview))
		} else {
			b.WriteString(fmt.Sprintf("- %s\n", item.Preview))
		}
	}

	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func ReadSignals(dir, source string) ([]Signal, error) {
	subDir := filepath.Join(dir, source)
	entries, err := os.ReadDir(subDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() > entries[j].Name()
	})

	var signals []Signal
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(subDir, entry.Name()))
		if err != nil {
			continue
		}
		sig := parseSignalMarkdown(source, entry.Name(), string(data))
		signals = append(signals, sig)
	}
	return signals, nil
}

func parseSignalMarkdown(source, filename, content string) Signal {
	name := strings.TrimSuffix(filename, ".md")
	ts, _ := time.Parse("2006-01-02T15-04-05", name)

	var items []SignalItem
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		line = strings.TrimPrefix(line, "- ")
		if strings.HasPrefix(line, "**") {
			parts := strings.SplitN(line, "** — ", 2)
			if len(parts) == 2 {
				title := strings.TrimPrefix(parts[0], "**")
				items = append(items, SignalItem{Title: title, Preview: parts[1]})
			}
		} else {
			items = append(items, SignalItem{Preview: line})
		}
	}

	return Signal{Source: source, CapturedAt: ts, Items: items}
}

func AppendSignalLog(dir string, sig Signal) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "signals.md")

	var b strings.Builder
	b.WriteString(fmt.Sprintf("\n## %s — %s\n\n", capitalize(sig.Source), sig.CapturedAt.Format("2006-01-02 15:04")))
	for _, item := range sig.Items {
		if item.Title != "" {
			b.WriteString(fmt.Sprintf("- **%s** — %s\n", item.Title, item.Preview))
		} else {
			b.WriteString(fmt.Sprintf("- %s\n", item.Preview))
		}
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(b.String())
	return err
}

func RenderSignalsMarkdown(signals []Signal) string {
	var b strings.Builder
	for i, sig := range signals {
		if i > 0 {
			b.WriteString("\n---\n\n")
		}
		b.WriteString(fmt.Sprintf("### %s — %s\n\n", capitalize(sig.Source), sig.CapturedAt.Format("2006-01-02 15:04")))
		for _, item := range sig.Items {
			if item.Title != "" {
				b.WriteString(fmt.Sprintf("- **%s** — %s\n", item.Title, item.Preview))
			} else {
				b.WriteString(fmt.Sprintf("- %s\n", item.Preview))
			}
		}
	}
	return b.String()
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
