package firefox

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lotas/tabsordnung/internal/types"
)

// FindFirefoxDir returns the platform-specific Firefox profile directory.
func FindFirefoxDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "linux":
		return filepath.Join(home, ".mozilla", "firefox")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Firefox")
	default:
		return ""
	}
}

// ParseProfilesINI reads profiles.ini and returns all profiles found.
func ParseProfilesINI(iniPath, firefoxDir string) ([]types.Profile, error) {
	f, err := os.Open(iniPath)
	if err != nil {
		return nil, fmt.Errorf("open profiles.ini: %w", err)
	}
	defer f.Close()

	var profiles []types.Profile
	var current *types.Profile

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if current != nil {
				profiles = append(profiles, *current)
				current = nil
			}
			section := line[1 : len(line)-1]
			if strings.HasPrefix(section, "Profile") {
				current = &types.Profile{}
			}
			continue
		}

		if current == nil {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, value := parts[0], parts[1]

		switch key {
		case "Name":
			current.Name = value
		case "Path":
			current.Path = value
		case "IsRelative":
			current.IsRelative = value == "1"
		case "Default":
			current.IsDefault = value == "1"
		}
	}

	if current != nil {
		profiles = append(profiles, *current)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan profiles.ini: %w", err)
	}

	for i := range profiles {
		if profiles[i].IsRelative {
			profiles[i].Path = filepath.Join(firefoxDir, profiles[i].Path)
		}
	}

	// Filter to profiles that have a session file (recovery or previous).
	var usable []types.Profile
	for _, p := range profiles {
		backupDir := filepath.Join(p.Path, "sessionstore-backups")
		for _, name := range []string{"recovery.jsonlz4", "previous.jsonlz4"} {
			if _, err := os.Stat(filepath.Join(backupDir, name)); err == nil {
				usable = append(usable, p)
				break
			}
		}
	}

	return usable, nil
}

// DiscoverProfiles finds and parses Firefox profiles on this system.
func DiscoverProfiles() ([]types.Profile, error) {
	dir := FindFirefoxDir()
	if dir == "" {
		return nil, fmt.Errorf("could not find Firefox directory for %s", runtime.GOOS)
	}
	iniPath := filepath.Join(dir, "profiles.ini")
	return ParseProfilesINI(iniPath, dir)
}
