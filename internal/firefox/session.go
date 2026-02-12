package firefox

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/types"
	"github.com/pierrec/lz4/v4"
)

// mozlz4 header: 8-byte magic "mozLz40\x00"
var mozLz4Magic = []byte("mozLz40\x00")

// DecompressMozLz4 decompresses data in Mozilla's mozlz4 format.
// The format is: 8-byte magic "mozLz40\x00" + 4-byte LE uint32 uncompressed size + lz4 block data.
func DecompressMozLz4(data []byte) ([]byte, error) {
	const headerSize = 12 // 8 magic + 4 size

	if len(data) < headerSize {
		return nil, fmt.Errorf("mozlz4: data too short (%d bytes)", len(data))
	}

	// Verify magic header.
	for i := 0; i < len(mozLz4Magic); i++ {
		if data[i] != mozLz4Magic[i] {
			return nil, fmt.Errorf("mozlz4: invalid header magic")
		}
	}

	// Read uncompressed size (4-byte little-endian uint32).
	uncompressedSize := binary.LittleEndian.Uint32(data[8:12])

	// Decompress using raw lz4 block decompression.
	dst := make([]byte, uncompressedSize)
	n, err := lz4.UncompressBlock(data[headerSize:], dst)
	if err != nil {
		return nil, fmt.Errorf("mozlz4: decompress failed: %w", err)
	}

	return dst[:n], nil
}

// Raw JSON types for Firefox session file parsing.
type rawEntry struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

type rawTab struct {
	Entries      []rawEntry `json:"entries"`
	Index        int        `json:"index"`
	LastAccessed int64      `json:"lastAccessed"`
	Image        string     `json:"image"`
	Group        string     `json:"groupId"`
}

type rawGroup struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	Collapsed bool   `json:"collapsed"`
}

type rawWindow struct {
	Tabs   []rawTab   `json:"tabs"`
	Groups []rawGroup `json:"groups"`
}

type rawSession struct {
	Windows []rawWindow `json:"windows"`
}

// ParseSession parses raw JSON session data into a SessionData structure.
func ParseSession(data []byte) (*types.SessionData, error) {
	var raw rawSession
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse session JSON: %w", err)
	}

	sd := &types.SessionData{
		ParsedAt: time.Now(),
	}

	// Process each window.
	for winIdx, window := range raw.Windows {
		// Build a map from group ID to TabGroup for named groups.
		groupMap := make(map[string]*types.TabGroup)
		for _, rg := range window.Groups {
			tg := &types.TabGroup{
				ID:        rg.ID,
				Name:      rg.Name,
				Color:     rg.Color,
				Collapsed: rg.Collapsed,
			}
			groupMap[rg.ID] = tg
			sd.Groups = append(sd.Groups, tg)
		}

		// Virtual "Ungrouped" group for tabs without a group.
		ungrouped := &types.TabGroup{
			ID:   "",
			Name: "Ungrouped",
		}

		for tabIdx, rt := range window.Tabs {
			if len(rt.Entries) == 0 {
				continue
			}

			// index is 1-based; current page is entries[index-1].
			entryIdx := rt.Index - 1
			if entryIdx < 0 || entryIdx >= len(rt.Entries) {
				entryIdx = len(rt.Entries) - 1
			}
			entry := rt.Entries[entryIdx]

			tab := &types.Tab{
				URL:          entry.URL,
				Title:        entry.Title,
				LastAccessed: time.UnixMilli(rt.LastAccessed),
				Favicon:      rt.Image,
				GroupID:      rt.Group,
				WindowIndex:  winIdx,
				TabIndex:     tabIdx,
			}

			sd.AllTabs = append(sd.AllTabs, tab)

			// Assign tab to named group or ungrouped.
			if rt.Group != "" {
				if tg, ok := groupMap[rt.Group]; ok {
					tg.Tabs = append(tg.Tabs, tab)
				} else {
					// Group referenced but not defined; put in ungrouped.
					ungrouped.Tabs = append(ungrouped.Tabs, tab)
				}
			} else {
				ungrouped.Tabs = append(ungrouped.Tabs, tab)
			}
		}

		// Append Ungrouped group at the end if it has any tabs.
		if len(ungrouped.Tabs) > 0 {
			sd.Groups = append(sd.Groups, ungrouped)
		}
	}

	return sd, nil
}

// ReadSessionFile reads and parses a Firefox session recovery file from the given profile directory.
// It tries recovery.jsonlz4 first (active session), then previous.jsonlz4 (last closed session).
func ReadSessionFile(profileDir string) (*types.SessionData, error) {
	backupDir := filepath.Join(profileDir, "sessionstore-backups")
	var data []byte
	var err error
	for _, name := range []string{"recovery.jsonlz4", "previous.jsonlz4"} {
		data, err = os.ReadFile(filepath.Join(backupDir, name))
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("no session file found in %s", backupDir)
	}

	decompressed, err := DecompressMozLz4(data)
	if err != nil {
		return nil, fmt.Errorf("decompress session file: %w", err)
	}

	return ParseSession(decompressed)
}
