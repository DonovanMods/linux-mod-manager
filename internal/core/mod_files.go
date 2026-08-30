package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ModFileEntry is one file `lmm mod files` reports for a mod: its game-dir-
// relative path, its size in bytes (0 when Deployed is false), and whether
// it currently exists on disk. Size follows symlinks (os.Stat, not Lstat) so
// a symlink-deployed file reports the real content size, not the link's own
// - a dangling symlink is reported as not Deployed rather than a bogus
// near-zero size.
type ModFileEntry struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Deployed bool   `json:"deployed"`
}

// ModFilesReport is the pure, displayable result of ModFiles: every datum
// `lmm mod files` prints about the files an installed mod owns.
type ModFilesReport struct {
	Mod   domain.InstalledMod `json:"mod"`
	Files []ModFileEntry      `json:"files"`
	// MergedPakOnly reports the DeployCompile "this mod owns no files of
	// its own - it only participates in the profile's merged pak" case
	// (HasRetainedCompileSource) - Files is always empty when this is true.
	MergedPakOnly bool `json:"merged_pak_only,omitempty"`
}

// ModFiles reports the files sourceID/modID has deployed in profileName -
// the read-only query behind `lmm mod files` (replaces the CLI's own direct
// GetGameCache call, v2 Phase 3 Task 10, #303, Ruling 10). Error text
// matches doModFiles' pre-extraction wrapping exactly: a missing mod always
// reports "mod not found: %s" regardless of the underlying cause, mirroring
// every other mod subcommand's convention.
func (s *Service) ModFiles(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (*ModFilesReport, error) {
	mod, err := s.GetInstalledMod(ctx, sourceID, modID, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("mod not found: %s", modID)
	}

	paths, err := s.GetDeployedFilesForMod(ctx, game.ID, profileName, sourceID, modID)
	if err != nil {
		return nil, fmt.Errorf("getting deployed files: %w", err)
	}

	report := &ModFilesReport{Mod: *mod}

	if len(paths) == 0 {
		gameCache := s.GetGameCache(game)
		report.MergedPakOnly = game.DeployMode == domain.DeployCompile &&
			HasRetainedCompileSource(gameCache, game.ID, mod.SourceID, modID, mod.Version, mod.FileIDs)
		return report, nil
	}

	report.Files = make([]ModFileEntry, 0, len(paths))
	for _, p := range paths {
		entry := ModFileEntry{Path: p}
		if info, statErr := os.Stat(filepath.Join(game.ModPath, p)); statErr == nil {
			entry.Deployed = true
			entry.Size = info.Size()
		}
		report.Files = append(report.Files, entry)
	}

	return report, nil
}
