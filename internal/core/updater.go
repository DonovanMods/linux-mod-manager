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

// CheckUpdates checks for available updates for installed mods. game supplies
// the source-ID mapping: installed rows persist the lmm game ID, but sources
// like NexusMods address games by their own domain, so each source's batch is
// translated via game.SourceIDs before the call (empty mapping = keep the lmm
// id, matching the search-side semantics in Service.SearchMods/GetMod).
// UpdateCheckable reports whether CheckUpdates will query a source for mod.
// Two reasons it will not: the mod is pinned (a user choice, reversible with
// `lmm mod set-update`), or it is a local import with no remote to ask.
//
// Exported because both interfaces need to explain the gap between "mods
// installed" and "mods checked" - without it, a filtered mod silently vanishes
// from update output and the user is told everything is up to date. Callers
// must use this rather than re-testing the fields, so the reported counts can
// never drift from what CheckUpdates actually skipped.
func UpdateCheckable(mod domain.InstalledMod) bool {
	return mod.UpdatePolicy != domain.UpdatePinned && mod.SourceID != domain.SourceLocal
}

// UpdateSkips counts the mods CheckUpdates will filter out, by reason. The two
// are reported separately because the remedies differ: a pin can be lifted, a
// local mod can never be checked.
type UpdateSkips struct {
	Pinned int
	Local  int
}

// Total is the number of mods that will not be checked at all.
func (s UpdateSkips) Total() int { return s.Pinned + s.Local }

// CountUpdateSkips tallies why CheckUpdates will skip mods in installed. A mod
// that is both pinned and local counts once, as pinned, so Total never exceeds
// len(installed).
func CountUpdateSkips(installed []domain.InstalledMod) UpdateSkips {
	var s UpdateSkips
	for _, mod := range installed {
		switch {
		case mod.UpdatePolicy == domain.UpdatePinned:
			s.Pinned++
		case mod.SourceID == domain.SourceLocal:
			s.Local++
		}
	}
	return s
}

func (u *Updater) CheckUpdates(ctx context.Context, game *domain.Game, installed []domain.InstalledMod) ([]domain.Update, error) {
	var checkable []domain.InstalledMod
	for _, mod := range installed {
		if UpdateCheckable(mod) {
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

		if game != nil {
			if mappedID, ok := game.SourceIDs[sourceID]; ok && mappedID != "" {
				translated := make([]domain.InstalledMod, len(mods))
				copy(translated, mods)
				for i := range translated {
					translated[i].GameID = mappedID
				}
				mods = translated
			}
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
