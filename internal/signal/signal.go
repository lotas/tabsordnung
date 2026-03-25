package signal

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/lotas/tabsordnung/internal/applog"
)

type SignalItem struct {
	Title     string `json:"title"`
	Preview   string `json:"preview"`
	Snippet   string `json:"snippet"`
	Timestamp string `json:"timestamp"`
	Kind      string `json:"kind"` // "dm", "mention", "channel", or ""
}

func DetectSource(url string) string {
	lower := strings.ToLower(url)
	switch {
	case strings.Contains(lower, "mail.google.com"):
		return "gmail"
	case strings.Contains(lower, "slack.com"):
		return "slack"
	case strings.Contains(lower, "element.io"),
		strings.Contains(lower, "chat.mozilla.org"),
		strings.Contains(lower, "matrix."):
		return "matrix"
	}
	return ""
}

// ExtractAccount derives an account identifier from a URL.
// Used as a fallback when the extension doesn't provide an account string.
func ExtractAccount(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	lower := strings.ToLower(u.Hostname())

	switch {
	case strings.Contains(lower, "mail.google.com"):
		// Gmail multi-account URLs contain /u/N/ in the path
		parts := strings.Split(u.Path, "/")
		for i, p := range parts {
			if p == "u" && i+1 < len(parts) {
				return parts[i+1]
			}
		}
		return "0"
	case strings.Contains(lower, "slack.com"):
		// app.slack.com/client/TWORKSPACE/... or workspace.slack.com
		if lower == "app.slack.com" {
			parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
			if len(parts) >= 2 && parts[0] == "client" {
				return parts[1]
			}
		}
		// subdomain-based: workspace.slack.com
		sub := strings.TrimSuffix(lower, ".slack.com")
		if sub != "" && sub != "app" && sub != "www" {
			return sub
		}
		return ""
	case strings.Contains(lower, "element.io"),
		strings.Contains(lower, "chat.mozilla.org"),
		strings.Contains(lower, "matrix."):
		return u.Hostname()
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
		key := item.Title + "\x00" + item.Preview + "\x00" + item.Timestamp
		if !seen[key] {
			seen[key] = true
			result = append(result, item)
		}
	}
	if len(result) != len(items) {
		applog.Info("signal.dedup", "before", len(items), "after", len(result))
	}
	return result
}
