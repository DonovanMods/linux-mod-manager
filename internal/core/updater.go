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
// id, matching the search-side semantics in Service.SearchMods/GetMod). sink
// receives an UpdateCheckEvent per mod from sources that implement
// source.UpdateProgressReporter (nexusmods, curseforge); a nil sink, or a
// source without the optional interface, emits nothing.
func (u *Updater) CheckUpdates(ctx context.Context, game *domain.Game, installed []domain.InstalledMod, sink EventSink) ([]domain.Update, error) {
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

	// GlobalIndex/GlobalTotal (Ruling 6, #283) span every source's batch, so
	// a two-source run reports 1/5..5/5 instead of the per-source Index/Total
	// restarting at 1 for the second source. globalOffset is reserved for
	// each source's batch up front, before that source's own success/failure
	// is known, so the running total stays the sum of every batch size
	// regardless of which sources actually report progress or error out.
	//
	// The reservation is unconditional, so a source that emits NO event
	// leaves its slice of 1..N unpopulated - the printed sequence has a
	// hole. Two causes, and the second is the common one offline: a source
	// whose registry lookup or check fails, and a source that simply does
	// not implement source.UpdateProgressReporter (only nexusmods and
	// curseforge do, so an all-directory-source run prints no counter line
	// at all). What IS guaranteed: each source's ticks occupy a fixed,
	// non-overlapping, ascending slice of 1..N; N is the total checkable
	// count regardless of failures; and each source's GlobalIndex-Index
	// offset is constant across its batch. Anything stronger would have to
	// defer the reservation until a source's first event, which would make
	// the numbers depend on map-iteration order (unit Q review, parked
	// item 2).
	globalTotal := len(checkable)
	globalOffset := 0

	// Check each source
	for sourceID, mods := range bySource {
		select {
		case <-ctx.Done():
			return allUpdates, ctx.Err()
		default:
		}

		batchOffset := globalOffset
		globalOffset += len(mods)

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

		var updates []domain.Update
		if rep, ok := src.(source.UpdateProgressReporter); ok && sink != nil {
			updates, err = rep.CheckUpdatesWithProgress(ctx, mods, func(n, total int, name string) {
				sink(UpdateCheckEvent{
					Scope: Scope{Op: OpUpdateCheck, ModName: name, Index: n, Total: total}, SourceID: sourceID,
					GlobalIndex: batchOffset + n, GlobalTotal: globalTotal,
				})
			})
		} else {
			updates, err = src.CheckUpdates(ctx, mods)
		}
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
	Pinned int `json:"pinned"`
	Local  int `json:"local"`
}

// Total is the number of mods that will not be checked at all.
func (s UpdateSkips) Total() int { return s.Pinned + s.Local }

// UpdateCheckReport is everything a bulk `lmm update` check renders: the
// updates found, the mods that were never checked, and - when the check
// itself failed partway through - why.
//
// Updates is CheckGameUpdates' own result verbatim (each entry carries the
// whole installed mod, the new version, the changelog and the #196
// recompile/#289 lock flags), never a projection of it. ErrorMessage is set
// when the check did not complete: an empty Updates list otherwise means
// "nothing to update", and with this set it means the answer is unknown -
// the one distinction a consumer reading stdout alone cannot make
// otherwise, since the human warning goes to stderr.
type UpdateCheckReport struct {
	GameID       string          `json:"game_id"`
	Profile      string          `json:"profile"`
	Updates      []domain.Update `json:"updates"`
	Skipped      UpdateSkips     `json:"skipped"`
	ErrorMessage string          `json:"error,omitempty"`
}

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

// lockStateFromProfile is lockState's variant for a caller that has already
// loaded the profile - CheckGameUpdates' stamping loop below uses this to
// load the profile ONCE for the whole call rather than once per listed mod
// (#289 review, Minor 2: reloading the same profile from disk N times for a
// listing of N mods was needless I/O). prof may be nil (a missing/
// unreadable profile), in which case every ref reports unlocked - matching
// lockState's own convention.
func lockStateFromProfile(prof *domain.Profile, sourceID, modID string) (locked bool, lockedVersion string) {
	if prof == nil {
		return false, ""
	}
	ref := prof.FindRef(sourceID, modID)
	if ref != nil && ref.Locked {
		return true, ref.Version
	}
	return false, ""
}

// lockState reports whether (sourceID, modID) is locked in gameID/
// profileName's profile and, if so, at what version - the "load profile ->
// FindRef -> check .Locked" pattern PlanUpdate/PlanUpdateFrom/PlanRollback
// need for a SINGLE mod (each already reads the profile once per call, so
// there is nothing to batch). A missing/unreadable profile reports unlocked
// rather than an error - matching every other lock gate's convention (a
// lock cannot exist in a profile that doesn't load); err is always nil
// today, kept for symmetry with this file's other ctx-taking Service
// methods.
func (s *Service) lockState(ctx context.Context, gameID, profileName, sourceID, modID string) (locked bool, lockedVersion string, err error) {
	prof, _ := s.NewProfileManager().Get(ctx, gameID, profileName)
	locked, lockedVersion = lockStateFromProfile(prof, sourceID, modID)
	return locked, lockedVersion, nil
}

// CheckGameUpdates is the single seam CLI checks updates through
// (#196/#197): it combines Updater.CheckUpdates' remote version
// checks with CheckMergedPakStaleness' local merged-pak staleness scan
// (#197's generalization of #196's per-mod base-pak check to the merged
// model). profileName is required (#197): staleness is scoped to
// ONE profile's merged pak, not the whole game.
//
// Errors from either half are tolerated the same way CheckUpdates already
// tolerates a single source failing: whatever updates were found are still
// returned, with the first non-nil error surfaced (checkErr takes priority
// as the richer, multi-source diagnostic when both fail). sink is passed
// straight through to Updater.CheckUpdates.
func (s *Service) CheckGameUpdates(ctx context.Context, game *domain.Game, profileName string, installed []domain.InstalledMod, sink EventSink) ([]domain.Update, error) {
	updates, checkErr := s.NewUpdater().CheckUpdates(ctx, game, installed, sink)

	staleUpd, staleErr := s.CheckMergedPakStaleness(ctx, game, profileName)
	if staleErr != nil && checkErr == nil {
		checkErr = staleErr
	}

	if staleUpd != nil {
		reported := false
		for _, u := range updates {
			if u.InstalledMod.SourceID == staleUpd.InstalledMod.SourceID && u.InstalledMod.ID == staleUpd.InstalledMod.ID {
				reported = true
				break
			}
		}
		if !reported {
			updates = append(updates, *staleUpd)
		}
	}

	// #289: stamp each entry's lock state via lockStateFromProfile - the
	// single seam that lets every caller (cmd/lmm's bulk table/JSON,
	// PlanUpdate/PlanUpdateFrom) drop its own profile scan. The profile is
	// loaded ONCE here (#289 review, Minor 2), not once per entry - a
	// missing/unreadable profile leaves every entry unlocked, matching every
	// other "profile load failure means unlocked" precedent (ApplyUpdate's
	// own lock gate, applySingleUpdate before this task).
	prof, _ := s.NewProfileManager().Get(ctx, game.ID, profileName)
	for i := range updates {
		locked, lockedVersion := lockStateFromProfile(prof, updates[i].InstalledMod.SourceID, updates[i].InstalledMod.ID)
		if locked {
			updates[i].Locked = true
			updates[i].LockedVersion = lockedVersion
		}
	}

	return updates, checkErr
}
