package firefox

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/lotas/tabsordnung/internal/analyzer"
	"github.com/pierrec/lz4/v4"
)

func TestIntegration_FullPipeline(t *testing.T) {
	// Create a fake profile directory with a session file
	profileDir := t.TempDir()
	backupDir := filepath.Join(profileDir, "sessionstore-backups")
	os.MkdirAll(backupDir, 0755)

	sessionJSON := `{
		"version": ["sessionrestore", 1],
		"windows": [{
			"tabs": [
				{
					"entries": [{"url": "https://example.com", "title": "Example"}],
					"index": 1,
					"lastAccessed": 1000000000000,
					"groupId": "g1"
				},
				{
					"entries": [{"url": "https://example.com", "title": "Example Dup"}],
					"index": 1,
					"lastAccessed": 1000000000000,
					"groupId": "g1"
				},
				{
					"entries": [{"url": "https://other.com/page", "title": "Other"}],
					"index": 1,
					"lastAccessed": 1707654321000
				}
			],
			"groups": [
				{"id": "g1", "name": "Test Group", "color": "blue", "collapsed": false}
			]
		}]
	}`

	// Compress to mozlz4
	jsonBytes := []byte(sessionJSON)
	compressed := make([]byte, lz4.CompressBlockBound(len(jsonBytes)))
	n, err := lz4.CompressBlock(jsonBytes, compressed, nil)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}

	mozlz4 := make([]byte, 0, 12+n)
	mozlz4 = append(mozlz4, []byte("mozLz40\x00")...)
	sizeBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(sizeBuf, uint32(len(jsonBytes)))
	mozlz4 = append(mozlz4, sizeBuf...)
	mozlz4 = append(mozlz4, compressed[:n]...)

	os.WriteFile(filepath.Join(backupDir, "recovery.jsonlz4"), mozlz4, 0644)

	// Run the full pipeline
	data, err := ReadSessionFile(profileDir)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}

	// Run analyzers
	analyzer.AnalyzeStale(data.AllTabs, 7)
	analyzer.AnalyzeDuplicates(data.AllTabs)
	stats := analyzer.ComputeStats(data)

	// Verify results
	if stats.TotalTabs != 3 {
		t.Errorf("expected 3 tabs, got %d", stats.TotalTabs)
	}
	if stats.TotalGroups != 2 {
		t.Errorf("expected 2 groups, got %d", stats.TotalGroups)
	}
	if stats.DuplicateTabs != 2 {
		t.Errorf("expected 2 duplicates, got %d", stats.DuplicateTabs)
	}
	// First two tabs have very old lastAccessed, should be stale
	if stats.StaleTabs < 2 {
		t.Errorf("expected at least 2 stale tabs, got %d", stats.StaleTabs)
	}

	t.Logf("Pipeline passed: %d tabs, %d groups, %d stale, %d dead, %d dup",
		stats.TotalTabs, stats.TotalGroups, stats.StaleTabs, stats.DeadTabs, stats.DuplicateTabs)
}
