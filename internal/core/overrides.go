package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// ApplyProfileOverrides writes a profile's configuration overrides to the game install directory.
// Each key in profile.Overrides is a path relative to game.InstallPath; the value is written as file content.
// Used on deploy and profile switch so INI tweaks and other overrides are applied.
// Paths that escape the game install directory (e.g. ../../../etc/passwd) are rejected to prevent abuse.
func ApplyProfileOverrides(game *domain.Game, profile *domain.Profile) error {
	if len(profile.Overrides) == 0 {
		return nil
	}
	base, err := filepath.Abs(game.InstallPath)
	if err != nil {
		return fmt.Errorf("resolving game path: %w", err)
	}
	base = filepath.Clean(base)
	for relPath, content := range profile.Overrides {
		// Reject absolute or empty paths
		cleaned := filepath.Clean(filepath.FromSlash(relPath))
		if cleaned == "" || filepath.IsAbs(cleaned) {
			return fmt.Errorf("invalid override path: %q", relPath)
		}
		dest := filepath.Join(base, cleaned)
		dest = filepath.Clean(dest)
		// Ensure dest is under base (no path traversal)
		rel, err := filepath.Rel(base, dest)
		if err != nil {
			return fmt.Errorf("override path %q: %w", relPath, err)
		}
		if strings.HasPrefix(rel, "..") || rel == ".." {
			return fmt.Errorf("override path escapes game directory: %q", relPath)
		}
		dir := filepath.Dir(dest)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating override dir %s: %w", dir, err)
		}
		if err := os.WriteFile(dest, content, 0644); err != nil {
			return fmt.Errorf("writing override %s: %w", relPath, err)
		}
	}
	return nil
}
