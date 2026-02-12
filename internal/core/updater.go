package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

// Updater checks for and applies mod updates
type Updater struct {
	registry *source.Registry
}

// NewUpdater creates a new updater
func NewUpdater(registry *source.Registry) *Updater {
	return &Updater{
		registry: registry,
	}
}

// CheckUpdates checks for available updates for installed mods
func (u *Updater) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	// Filter out pinned mods and local mods (local mods have no remote source)
	var checkable []domain.InstalledMod
	for _, mod := range installed {
		if mod.UpdatePolicy != domain.UpdatePinned && mod.SourceID != domain.SourceLocal {
			checkable = append(checkable, mod)
		}
	}

	if len(checkable) == 0 {
		return nil, nil
	}

	// Group mods by source
	bySource := make(map[string][]domain.InstalledMod)
	for _, mod := range checkable {
		bySource[mod.SourceID] = append(bySource[mod.SourceID], mod)
	}

	var allUpdates []domain.Update
	var checkErrs []error

	// Check each source
	for sourceID, mods := range bySource {
		select {
		case <-ctx.Done():
			return allUpdates, ctx.Err()
		default:
		}

		src, err := u.registry.Get(sourceID)
		if err != nil {
			checkErrs = append(checkErrs, fmt.Errorf("source %s: %w", sourceID, err))
			continue
		}

		updates, err := src.CheckUpdates(ctx, mods)
		allUpdates = append(allUpdates, updates...)
		if err != nil {
			checkErrs = append(checkErrs, fmt.Errorf("source %s: %w", sourceID, err))
		}
	}

	if len(checkErrs) > 0 {
		return allUpdates, fmt.Errorf("update check had %d source error(s): %w", len(checkErrs), errors.Join(checkErrs...))
	}
	return allUpdates, nil
}

// GetAutoUpdateMods filters installed mods to those with auto-update enabled
func (u *Updater) GetAutoUpdateMods(installed []domain.InstalledMod) []domain.InstalledMod {
	var autoUpdate []domain.InstalledMod
	for _, mod := range installed {
		if mod.UpdatePolicy == domain.UpdateAuto {
			autoUpdate = append(autoUpdate, mod)
		}
	}
	return autoUpdate
}

// CompareVersions delegates to domain.CompareVersions.
// Kept for backward compatibility with existing callers.
func CompareVersions(v1, v2 string) int {
	return domain.CompareVersions(v1, v2)
}

// IsNewerVersion delegates to domain.IsNewerVersion.
// Kept for backward compatibility with existing callers.
func IsNewerVersion(currentVersion, newVersion string) bool {
	return domain.IsNewerVersion(currentVersion, newVersion)
}
