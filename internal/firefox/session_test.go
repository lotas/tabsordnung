package firefox

import (
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/pierrec/lz4/v4"
)

func TestDecompressMozLz4(t *testing.T) {
	t.Run("valid mozlz4 payload", func(t *testing.T) {
		original := []byte(`{"windows":[{"tabs":[]}]}`)

		// Compress with lz4 block compression.
		dst := make([]byte, lz4.CompressBlockBound(len(original)))
		n, err := lz4.CompressBlock(original, dst, nil)
		if err != nil {
			t.Fatalf("lz4.CompressBlock failed: %v", err)
		}
		compressed := dst[:n]

		// Build mozlz4 payload: 8-byte magic + 4-byte LE uint32 size + compressed data.
		magic := []byte("mozLz40\x00")
		sizeBytes := make([]byte, 4)
		binary.LittleEndian.PutUint32(sizeBytes, uint32(len(original)))

		payload := make([]byte, 0, len(magic)+len(sizeBytes)+len(compressed))
		payload = append(payload, magic...)
		payload = append(payload, sizeBytes...)
		payload = append(payload, compressed...)

		result, err := DecompressMozLz4(payload)
		if err != nil {
			t.Fatalf("DecompressMozLz4 returned error: %v", err)
		}
		if string(result) != string(original) {
			t.Errorf("expected %q, got %q", string(original), string(result))
		}
	})

	t.Run("invalid header returns error", func(t *testing.T) {
		// Wrong magic bytes.
		bad := []byte("BADMAGIC\x00\x00\x00\x00some data here")
		_, err := DecompressMozLz4(bad)
		if err == nil {
			t.Fatal("expected error for invalid header, got nil")
		}
	})

	t.Run("too short data returns error", func(t *testing.T) {
		short := []byte("mozLz40")
		_, err := DecompressMozLz4(short)
		if err == nil {
			t.Fatal("expected error for too-short data, got nil")
		}
	})
}

func TestParseSession(t *testing.T) {
	// Build a session JSON with:
	// - 1 window, 2 tabs, 1 group
	// - Tab 0: single entry, group="group-1", lastAccessed=1707654321000
	// - Tab 1: 2 entries, index=2 (current page is entries[1]), no group
	// - Group: id="group-1", name="Work", color="blue", collapsed=false
	session := map[string]interface{}{
		"windows": []map[string]interface{}{
			{
				"tabs": []map[string]interface{}{
					{
						"entries": []map[string]interface{}{
							{"url": "https://example.com", "title": "Example"},
						},
						"index":        1,
						"lastAccessed": 1707654321000,
						"image":        "https://example.com/favicon.ico",
						"groupId":      "group-1",
					},
					{
						"entries": []map[string]interface{}{
							{"url": "https://old.com", "title": "Old Page"},
							{"url": "https://current.com", "title": "Current Page"},
						},
						"index":        2,
						"lastAccessed": 1707654999000,
						"image":        "",
					},
				},
				"groups": []map[string]interface{}{
					{
						"id":        "group-1",
						"name":      "Work",
						"color":     "blue",
						"collapsed": false,
					},
				},
			},
		},
	}

	data, err := json.Marshal(session)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	sd, err := ParseSession(data)
	if err != nil {
		t.Fatalf("ParseSession returned error: %v", err)
	}

	// Should have 2 groups: "Work" first, then "Ungrouped".
	if len(sd.Groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(sd.Groups))
	}

	workGroup := sd.Groups[0]
	ungrouped := sd.Groups[1]

	if workGroup.Name != "Work" {
		t.Errorf("expected first group name 'Work', got %q", workGroup.Name)
	}
	if workGroup.ID != "group-1" {
		t.Errorf("expected first group ID 'group-1', got %q", workGroup.ID)
	}
	if workGroup.Color != "blue" {
		t.Errorf("expected group color 'blue', got %q", workGroup.Color)
	}
	if workGroup.Collapsed {
		t.Error("expected group collapsed=false")
	}

	if ungrouped.Name != "Ungrouped" {
		t.Errorf("expected second group name 'Ungrouped', got %q", ungrouped.Name)
	}

	// Work group should have 1 tab (tab 0).
	if len(workGroup.Tabs) != 1 {
		t.Fatalf("expected 1 tab in Work group, got %d", len(workGroup.Tabs))
	}
	tab0 := workGroup.Tabs[0]
	if tab0.URL != "https://example.com" {
		t.Errorf("tab0 URL: expected 'https://example.com', got %q", tab0.URL)
	}
	if tab0.Title != "Example" {
		t.Errorf("tab0 Title: expected 'Example', got %q", tab0.Title)
	}
	if tab0.Favicon != "https://example.com/favicon.ico" {
		t.Errorf("tab0 Favicon: expected 'https://example.com/favicon.ico', got %q", tab0.Favicon)
	}
	if tab0.GroupID != "group-1" {
		t.Errorf("tab0 GroupID: expected 'group-1', got %q", tab0.GroupID)
	}
	// lastAccessed=1707654321000 -> time.UnixMilli(1707654321000)
	if tab0.LastAccessed.UnixMilli() != 1707654321000 {
		t.Errorf("tab0 LastAccessed: expected 1707654321000, got %d", tab0.LastAccessed.UnixMilli())
	}

	// Ungrouped should have 1 tab (tab 1).
	if len(ungrouped.Tabs) != 1 {
		t.Fatalf("expected 1 tab in Ungrouped group, got %d", len(ungrouped.Tabs))
	}
	tab1 := ungrouped.Tabs[0]
	// index=2 means entries[1] is the current page.
	if tab1.URL != "https://current.com" {
		t.Errorf("tab1 URL: expected 'https://current.com', got %q", tab1.URL)
	}
	if tab1.Title != "Current Page" {
		t.Errorf("tab1 Title: expected 'Current Page', got %q", tab1.Title)
	}

	// AllTabs should have 2 tabs total.
	if len(sd.AllTabs) != 2 {
		t.Fatalf("expected 2 AllTabs, got %d", len(sd.AllTabs))
	}
}
