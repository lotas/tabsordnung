package firefox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseProfilesINI(t *testing.T) {
	dir := t.TempDir()
	absProfileDir := t.TempDir()
	iniContent := `[General]
StartWithLastProfile=1
Version=2

[Profile0]
Name=default-release
IsRelative=1
Path=abc123.default-release
Default=1

[Profile1]
Name=dev-edition
IsRelative=0
Path=` + absProfileDir + `
Default=0

[Install308046B0AF4A39CB]
Default=abc123.default-release
Locked=1
`
	iniPath := filepath.Join(dir, "profiles.ini")
	os.WriteFile(iniPath, []byte(iniContent), 0644)

	// Create profile dirs with session recovery files so they pass the filter.
	os.MkdirAll(filepath.Join(dir, "abc123.default-release", "sessionstore-backups"), 0755)
	os.WriteFile(filepath.Join(dir, "abc123.default-release", "sessionstore-backups", "recovery.jsonlz4"), []byte("dummy"), 0644)
	os.MkdirAll(filepath.Join(absProfileDir, "sessionstore-backups"), 0755)
	os.WriteFile(filepath.Join(absProfileDir, "sessionstore-backups", "recovery.jsonlz4"), []byte("dummy"), 0644)

	profiles, err := ParseProfilesINI(iniPath, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(profiles))
	}

	// First profile: relative path
	if profiles[0].Name != "default-release" {
		t.Errorf("expected name 'default-release', got %q", profiles[0].Name)
	}
	if profiles[0].Path != filepath.Join(dir, "abc123.default-release") {
		t.Errorf("expected resolved path, got %q", profiles[0].Path)
	}
	if !profiles[0].IsDefault {
		t.Error("expected profile 0 to be default")
	}

	// Second profile: absolute path
	if profiles[1].Name != "dev-edition" {
		t.Errorf("expected name 'dev-edition', got %q", profiles[1].Name)
	}
	if profiles[1].Path != absProfileDir {
		t.Errorf("expected absolute path %q, got %q", absProfileDir, profiles[1].Path)
	}
	if profiles[1].IsDefault {
		t.Error("expected profile 1 to not be default")
	}
}

func TestFindFirefoxDir(t *testing.T) {
	dir := FindFirefoxDir()
	if dir == "" {
		t.Skip("no Firefox directory found on this system")
	}
	t.Logf("found Firefox dir: %s", dir)
}
