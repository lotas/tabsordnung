package export

import (
	"encoding/json"
	"net/url"
	"time"

	"github.com/lotas/tabsordnung/internal/types"
)

type jsonExport struct {
	Profile    string       `json:"profile"`
	ExportedAt time.Time   `json:"exported_at"`
	Groups     []jsonGroup `json:"groups"`
}

type jsonGroup struct {
	Name  string    `json:"name"`
	Color string    `json:"color,omitempty"`
	Tabs  []jsonTab `json:"tabs"`
}

type jsonTab struct {
	Title              string    `json:"title"`
	URL                string    `json:"url"`
	Category           string    `json:"category"`
	Domain             string    `json:"domain"`
	LastAccessed       time.Time `json:"last_accessed"`
	LastAccessedPretty string    `json:"last_accessed_pretty"`
	LastAccessedDays   int       `json:"last_accessed_days"`
	IsStale            bool      `json:"is_stale,omitempty"`
	IsDead             bool      `json:"is_dead,omitempty"`
	IsDuplicate        bool      `json:"is_duplicate,omitempty"`
	DeadReason         string    `json:"dead_reason,omitempty"`
	StaleDays          int       `json:"stale_days,omitempty"`
}

// JSON formats session data as a JSON document.
func JSON(data *types.SessionData) (string, error) {
	out := jsonExport{
		Profile:    data.Profile.Name,
		ExportedAt: time.Now(),
		Groups:     make([]jsonGroup, 0, len(data.Groups)),
	}

	for _, g := range data.Groups {
		group := jsonGroup{
			Name:  g.Name,
			Color: g.Color,
			Tabs:  make([]jsonTab, 0, len(g.Tabs)),
		}
		for _, tab := range g.Tabs {
			group.Tabs = append(group.Tabs, jsonTab{
				Title:              tab.Title,
				URL:                tab.URL,
				Category:           g.Name,
				Domain:             extractDomain(tab.URL),
				LastAccessed:       tab.LastAccessed,
				LastAccessedPretty: relativeTime(tab.LastAccessed),
				LastAccessedDays:   int(time.Since(tab.LastAccessed).Hours() / 24),
				IsStale:            tab.IsStale,
				IsDead:             tab.IsDead,
				IsDuplicate:        tab.IsDuplicate,
				DeadReason:         tab.DeadReason,
				StaleDays:          tab.StaleDays,
			})
		}
		out.Groups = append(out.Groups, group)
	}

	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

func extractDomain(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	return u.Hostname()
}

