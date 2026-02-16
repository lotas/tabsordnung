package signal

import (
	"encoding/json"
	"strings"
)

type SignalItem struct {
	Title     string `json:"title"`
	Preview   string `json:"preview"`
	Timestamp string `json:"timestamp"`
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
	return result
}
