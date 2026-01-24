package core

import (
	"context"
	"strconv"
	"strings"

	"lmm/internal/domain"
	"lmm/internal/source"
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
	// Filter out pinned mods
	var checkable []domain.InstalledMod
	for _, mod := range installed {
		if mod.UpdatePolicy != domain.UpdatePinned {
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

	// Check each source
	for sourceID, mods := range bySource {
		select {
		case <-ctx.Done():
			return allUpdates, ctx.Err()
		default:
		}

		src, err := u.registry.Get(sourceID)
		if err != nil {
			// Skip unknown sources
			continue
		}

		updates, err := src.CheckUpdates(ctx, mods)
		if err != nil {
			// Log but continue with other sources
			continue
		}

		allUpdates = append(allUpdates, updates...)
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

// CompareVersions compares two version strings
// Returns: -1 if v1 < v2, 0 if v1 == v2, 1 if v1 > v2
func CompareVersions(v1, v2 string) int {
	parts1 := parseVersion(v1)
	parts2 := parseVersion(v2)

	// Pad to same length
	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var p1, p2 int
		if i < len(parts1) {
			p1 = parts1[i]
		}
		if i < len(parts2) {
			p2 = parts2[i]
		}

		if p1 < p2 {
			return -1
		}
		if p1 > p2 {
			return 1
		}
	}

	return 0
}

// parseVersion splits a version string into numeric parts
func parseVersion(v string) []int {
	// Remove common prefixes
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")

	parts := strings.Split(v, ".")
	result := make([]int, 0, len(parts))

	for _, part := range parts {
		// Extract numeric portion (handle things like "1.0.0-beta")
		numStr := ""
		for _, c := range part {
			if c >= '0' && c <= '9' {
				numStr += string(c)
			} else {
				break
			}
		}

		if numStr == "" {
			result = append(result, 0)
		} else {
			n, _ := strconv.Atoi(numStr)
			result = append(result, n)
		}
	}

	return result
}

// IsNewerVersion returns true if newVersion is newer than currentVersion
func IsNewerVersion(currentVersion, newVersion string) bool {
	return CompareVersions(currentVersion, newVersion) < 0
}
