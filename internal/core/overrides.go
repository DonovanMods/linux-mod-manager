package core

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// ApplyProfileOverrides writes a profile's configuration overrides to the game install directory.
// Each key in profile.Overrides is a path relative to game.InstallPath; the value is written as file content.
// Used on deploy and profile switch so INI tweaks and other overrides are applied.
func ApplyProfileOverrides(game *domain.Game, profile *domain.Profile) error {
	if len(profile.Overrides) == 0 {
		return nil
	}
	base := game.InstallPath
	for relPath, content := range profile.Overrides {
		dest := filepath.Join(base, filepath.FromSlash(relPath))
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
