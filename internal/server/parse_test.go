package server

import (
	"encoding/json"
	"testing"
)

func TestParseSnapshot(t *testing.T) {
	snapshot := `{
		"type": "snapshot",
		"tabs": [
			{"id": 1, "url": "https://example.com", "title": "Example", "lastAccessed": 1700000000000, "groupId": 5, "windowId": 1, "index": 0},
			{"id": 2, "url": "https://other.com", "title": "Other", "lastAccessed": 1700000060000, "groupId": -1, "windowId": 1, "index": 1}
		],
		"groups": [
			{"id": 5, "title": "Work", "color": "blue", "collapsed": false}
		]
	}`

	var msg IncomingMsg
	if err := json.Unmarshal([]byte(snapshot), &msg); err != nil {
		t.Fatal(err)
	}

	data, err := ParseSnapshot(msg)
	if err != nil {
		t.Fatal(err)
	}

	if len(data.AllTabs) != 2 {
		t.Errorf("got %d tabs, want 2", len(data.AllTabs))
	}
	if data.AllTabs[0].BrowserID != 1 {
		t.Errorf("tab BrowserID = %d, want 1", data.AllTabs[0].BrowserID)
	}
	if data.AllTabs[0].URL != "https://example.com" {
		t.Errorf("tab URL = %q", data.AllTabs[0].URL)
	}
	if data.AllTabs[0].LastAccessed.IsZero() {
		t.Error("tab LastAccessed is zero")
	}

	// Should have 2 groups: "Work" + "Ungrouped"
	if len(data.Groups) != 2 {
		t.Errorf("got %d groups, want 2", len(data.Groups))
	}

	// Work group should have 1 tab
	var workFound bool
	for _, g := range data.Groups {
		if g.Name == "Work" {
			workFound = true
			if len(g.Tabs) != 1 {
				t.Errorf("Work group has %d tabs, want 1", len(g.Tabs))
			}
		}
	}
	if !workFound {
		t.Fatal("Work group not found")
	}
}

func TestParseSnapshotNoGroups(t *testing.T) {
	snapshot := `{
		"type": "snapshot",
		"tabs": [
			{"id": 1, "url": "https://example.com", "title": "Example", "lastAccessed": 1700000000000, "groupId": -1, "windowId": 1, "index": 0}
		],
		"groups": []
	}`

	var msg IncomingMsg
	json.Unmarshal([]byte(snapshot), &msg)
	data, err := ParseSnapshot(msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Groups) != 1 || data.Groups[0].Name != "Ungrouped" {
		t.Errorf("expected single Ungrouped group, got %v", data.Groups)
	}
}
