package server

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/types"
)

type wireTab struct {
	ID           int    `json:"id"`
	URL          string `json:"url"`
	Title        string `json:"title"`
	LastAccessed int64  `json:"lastAccessed"`
	GroupID      int    `json:"groupId"`
	WindowID     int    `json:"windowId"`
	Index        int    `json:"index"`
	FavIconURL   string `json:"favIconUrl"`
}

type wireGroup struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	Color     string `json:"color"`
	Collapsed bool   `json:"collapsed"`
}

// ParseSnapshot converts an IncomingMsg of type "snapshot" into a SessionData.
func ParseSnapshot(msg IncomingMsg) (*types.SessionData, error) {
	var tabs []wireTab
	if err := json.Unmarshal(msg.Tabs, &tabs); err != nil {
		return nil, fmt.Errorf("parse tabs: %w", err)
	}
	var groups []wireGroup
	if err := json.Unmarshal(msg.Groups, &groups); err != nil {
		return nil, fmt.Errorf("parse groups: %w", err)
	}

	groupMap := make(map[int]*types.TabGroup)
	var result []*types.TabGroup
	for _, g := range groups {
		tg := &types.TabGroup{
			ID:        strconv.Itoa(g.ID),
			Name:      g.Title,
			Color:     g.Color,
			Collapsed: g.Collapsed,
		}
		groupMap[g.ID] = tg
		result = append(result, tg)
	}

	ungrouped := &types.TabGroup{ID: "ungrouped", Name: "Ungrouped"}

	var allTabs []*types.Tab
	for _, wt := range tabs {
		tab := &types.Tab{
			BrowserID:    wt.ID,
			URL:          wt.URL,
			Title:        wt.Title,
			LastAccessed: time.UnixMilli(wt.LastAccessed),
			GroupID:      strconv.Itoa(wt.GroupID),
			Favicon:      wt.FavIconURL,
			WindowIndex:  wt.WindowID,
			TabIndex:     wt.Index,
		}
		allTabs = append(allTabs, tab)

		if g, ok := groupMap[wt.GroupID]; ok {
			g.Tabs = append(g.Tabs, tab)
		} else {
			tab.GroupID = ""
			ungrouped.Tabs = append(ungrouped.Tabs, tab)
		}
	}

	if len(ungrouped.Tabs) > 0 {
		result = append(result, ungrouped)
	}

	return &types.SessionData{
		Groups:   result,
		AllTabs:  allTabs,
		ParsedAt: time.Now(),
	}, nil
}

// ParseTab converts a raw JSON tab into a Tab.
func ParseTab(raw json.RawMessage) (*types.Tab, error) {
	var wt wireTab
	if err := json.Unmarshal(raw, &wt); err != nil {
		return nil, err
	}
	return &types.Tab{
		BrowserID:    wt.ID,
		URL:          wt.URL,
		Title:        wt.Title,
		LastAccessed: time.UnixMilli(wt.LastAccessed),
		GroupID:      strconv.Itoa(wt.GroupID),
		Favicon:      wt.FavIconURL,
		WindowIndex:  wt.WindowID,
		TabIndex:     wt.Index,
	}, nil
}
